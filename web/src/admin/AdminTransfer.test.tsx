// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, expect, test, vi } from "vitest";
import { Operations } from "./Operations";

const superAdmin = { user_id: "root", roles: ["super_admin"], permissions: ["*"], enabled: true, version: 1 };
const financeAdmin = { user_id: "fin", roles: ["finance_admin"], permissions: ["finance:read", "finance:write"], enabled: true, version: 1 };
const json = (data: unknown) => new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  HTMLDialogElement.prototype.close = function () { this.open = false; };
});
afterAll(() => {
  Reflect.deleteProperty(HTMLDialogElement.prototype, "showModal");
  Reflect.deleteProperty(HTMLDialogElement.prototype, "close");
});

function stubList() {
  return vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
    if (options?.method === "POST") return json({});
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/admin/v1/transactions") return json({ items: [], total: 0, as_of: "2026-11-12T00:00:00Z" });
    return json({});
  });
}

test("cross-account conserved transfer form is visible only to super_admin", () => {
  vi.stubGlobal("fetch", stubList());
  const { unmount } = render(<Operations resource="transactions" principal={superAdmin} csrf="csrf" />);
  expect(screen.getByRole("button", { name: "跨账户守恒划款" })).toBeDefined();
  unmount();
  render(<Operations resource="transactions" principal={financeAdmin} csrf="csrf" />);
  expect(screen.queryByRole("button", { name: "跨账户守恒划款" })).toBeNull();
});

test("cross-account transfer form posts a conserved cash amount to /admin/v1/transfers", async () => {
  let sentPath = "";
  let sentBody: Record<string, unknown> = {};
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
    if (options?.method === "POST") { sentPath = String(input); sentBody = JSON.parse(String(options.body)); return json({}); }
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/admin/v1/transactions") return json({ items: [], total: 0, as_of: "" });
    return json({});
  }));
  render(<Operations resource="transactions" principal={superAdmin} csrf="csrf" />);
  fireEvent.click(screen.getByRole("button", { name: "跨账户守恒划款" }));
  fireEvent.change(screen.getByLabelText("目标账户 ID"), { target: { value: "acct-target" } });
  fireEvent.change(screen.getByLabelText("目标产品 ID（留空为统一钱包）"), { target: { value: "llmrouter" } });
  fireEvent.change(screen.getByLabelText("金额（元）"), { target: { value: "300" } });
  fireEvent.change(screen.getByLabelText("操作原因"), { target: { value: "补款" } });
  fireEvent.click(screen.getByRole("button", { name: "确认提交" }));
  await waitFor(() => expect(sentPath).toBe("/admin/v1/transfers"));
  expect(sentBody).toMatchObject({ account_id: "acct-target", product_id: "llmrouter", amount: "300000000" });
});
