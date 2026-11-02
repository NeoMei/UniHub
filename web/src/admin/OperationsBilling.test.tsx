// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, expect, test, vi } from "vitest";
import { Operations } from "./Operations";

const principal = { user_id: "root", roles: ["super_admin"], permissions: ["*"], enabled: true, version: 1 };
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

test("billing profile editor exposes composable sources and model policy", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/admin/v1/billing-profiles") return json({ items: [{ product_id: "notes", version: 2, subscription: { enabled: true }, included_credits: { enabled: true }, addons: { enabled: true }, usage: { enabled: true, require_subscription: true, funding_modes: ["credits"], default_funding_mode: "credits", models: ["gpt-4o"] } }], total: 1, as_of: "2026-11-03T00:00:00Z" });
    return json({});
  }));
  render(<Operations resource="billing-profiles" principal={principal} csrf="csrf" />);
  fireEvent.click(await screen.findByRole("button", { name: "更新计费配置" }));
  expect(screen.getByText("订阅能力")).toBeDefined();
  expect(screen.getAllByText("赠送额度").length).toBeGreaterThan(1);
  expect(screen.getByText("支持的资金模式")).toBeDefined();
  expect(screen.getByText(/模型白名单/)).toBeDefined();
});

test("offer editor exposes entitlements and commercial rules", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/admin/v1/offers") return json({ items: [{ id: "basic", product_id: "notes", name: "基础", account_kind: "both", kind: "membership", period: "month", price: "1", monthly_credit: "2", entitlements: { seats: "1" }, rank: 1, validity_months: 1, refund_rule: "unused_only_v1", upgrade_rule: "remaining_term_prorata_v1", status: "draft", sandbox: true, version: 1 }], total: 1, as_of: "2026-11-03T00:00:00Z" });
    return json({});
  }));
  render(<Operations resource="offers" principal={principal} csrf="csrf" />);
  fireEvent.click(await screen.findByRole("button", { name: "编辑草稿" }));
  expect(screen.getByText(/权益配置（每行/)).toBeDefined();
  expect(screen.getByText("退款规则")).toBeDefined();
  expect(screen.getByText("升级规则")).toBeDefined();
});
