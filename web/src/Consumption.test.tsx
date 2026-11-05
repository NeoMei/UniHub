// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { Consumption } from "./Consumption";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const account = { id: "A", name: "账户 A", kind: "personal", role: "owner" };
const scope = { account_id: "A", visibility: "account" };
const filter = { from: "2026-10-01T00:00:00Z", to: "2026-10-31T00:00:00Z" };
const call = { call_id: "product_runtime:db-id", source_call_id: "native-call", source: "product_runtime", account_id: "original-payer", actor_id: "user", scope, financial_details: true, origin_product_id: "notes", instance_id: "east", operation: "image", model: "model-A", asset: "CNY/6", held: "1000000", charged: "123456", refunded: "876544", funding_mode: "prepaid_cash", funding_product_id: "", status: "UNKNOWN", created_at: "2026-10-20T12:00:00Z", time_source: "recorded", waiver: { case_id: "case-1", customer_status: "RELEASED", provider_state: "PROVIDER_UNRESOLVED" } };
const page = (items: unknown[] = [call], extra = {}) => ({ scope, filter, financial_details: true, items, has_more: false, total: "1", unknown_time: "0", as_of: "2026-10-31T00:00:00Z", accuracy: "live", consistency: "live", ...extra });
const summary = { scope, filter, financial_details: true, calls_total: "3", settled_calls: "1", pending_calls: "1", missing_usage_calls: "0", unknown_calls: "1", rejected_calls: "0", released_calls: "0", by_status: {}, asset: "", charges_by_asset: { "CNY/6": "123456", "CREDIT/6": "2000000", "LR_QUOTA/0": "9007199254740993" }, refunds_by_asset: { "CNY/6": "876544" }, usage_complete: false, unknown_time: "0", as_of: "2026-10-31T00:00:00Z", accuracy: "live", consistency: "live" };
function fetchData(value = page()) {
  return vi.fn(async (path: string) => new Response(JSON.stringify(path.includes("usage/summary") ? summary : value), { status: 200 }));
}

it("shows mixed-source calls with their original payer, exact independent assets and unresolved states", async () => {
  vi.stubGlobal("fetch", fetchData());
  render(<Consumption account={account} products={[{ id: "notes", name: "Notes" }]} />);
  expect(await screen.findByText("native-call")).toBeDefined();
  const row = screen.getByText("native-call").closest("tr")!;
  expect(row.textContent).toContain("原生产品");
  expect(row.textContent).toContain("east");
  expect(row.textContent).toContain("image");
  expect(row.textContent).toContain("original-payer");
  expect(row.textContent).toContain("待核实");
  expect(row.textContent).toContain("0.123456");
  expect(row.textContent).toContain("0.876544");
  expect(row.textContent).toContain("个人统一钱包");
  expect(row.textContent).toContain("客户费用：已释放");
  expect(screen.getByText(/供应商.*待核实/)).toBeDefined();
  expect(screen.getByText(/人民币 · 0.123456/)).toBeDefined();
  expect(screen.getByText(/返还 人民币 · 0.876544/)).toBeDefined();
  expect(screen.getByText(/权益额度 · 2.000000/)).toBeDefined();
  expect(screen.getByText(/旧额度 · 9,007,199,254,740,993/)).toBeDefined();
});

it("keeps legacy managed-model cash funding distinct from native unified and native product wallets", async () => {
  vi.stubGlobal("fetch", fetchData(page([
    { ...call, call_id: "managed", source_call_id: "managed", source: "managed_model", funding_product_id: undefined, waiver: undefined },
    { ...call, call_id: "native-product", source_call_id: "native-product", funding_product_id: "notes", waiver: undefined },
  ])));
  render(<Consumption account={account} products={[{ id: "notes", name: "Notes" }]} />);
  const managed = (await screen.findByText("managed", {selector: "span"})).closest("tr")!;
  expect(managed.textContent).toContain("llmrouter 产品钱包");
  const nativeProduct = screen.getByText("native-product", {selector: "span"}).closest("tr")!;
  expect(nativeProduct.textContent).toContain("产品现金钱包 · Notes");
  expect(nativeProduct.textContent).not.toContain("个人统一钱包");
});

it.each(["member", "admin"])("hides financial fields for %s even when a stale backend response contains them", async (role) => {
  vi.stubGlobal("fetch", fetchData());
  render(<Consumption account={{ ...account, role, kind: "team" }} products={[]} />);
  await screen.findByText("native-call");
  expect(screen.getByText(/仅显示本人调用/)).toBeDefined();
  expect(screen.queryByRole("columnheader", { name: "预占 / 实扣 / 返还" })).toBeNull();
  expect(screen.queryByText(/0.123456|0.876544|9,007,199,254,740,993/)).toBeNull();
});

it("does not upgrade an unfamiliar call status to success or expose row amounts when the server withholds them", async () => {
  vi.stubGlobal("fetch", fetchData(page([{ ...call, status: "future_state", financial_details: false, waiver: undefined }])));
  render(<Consumption account={account} products={[]} />);
  const row = (await screen.findByText("native-call")).closest("tr")!;
  expect(row.textContent).toContain("待核实");
  expect(row.textContent).not.toMatch(/已完成|已结算|0.123456|0.876544|个人统一钱包/);
});

it("uses the server restored window for cursor pages and clears the cursor when filters change", async () => {
  const paths: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    paths.push(path);
    const value = path.includes("usage/summary") ? summary : page([call], { has_more: !path.includes("cursor="), next_cursor: "opaque/+=" });
    return new Response(JSON.stringify(value), { status: 200 });
  }));
  render(<Consumption account={account} products={[]} />);
  await screen.findByText("native-call");
  fireEvent.click(screen.getByRole("button", { name: "下一页" }));
  await waitFor(() => expect(paths.some(path => path.includes("cursor="))).toBe(true));
  const next = new URL(paths.find(path => path.includes("cursor="))!, "http://local");
  expect(next.searchParams.get("from")).toBe("2026-10-01T00:00:00Z");
  expect(next.searchParams.get("to")).toBe("2026-10-31T00:00:00Z");
  expect(next.searchParams.get("cursor")).toBe("opaque/+=");
  fireEvent.change(screen.getByLabelText("资产"), { target: { value: "CREDIT/6" } });
  fireEvent.click(screen.getByRole("button", { name: "查询" }));
  await waitFor(() => expect(paths.some(path => path.includes("asset=CREDIT%2F6"))).toBe(true));
  const last = paths.filter(path => path.includes("/calls?")).at(-1)!;
  expect(last).not.toContain("cursor=");
});

it("aborts and discards late account results when the payer changes", async () => {
  let resolveA!: (value: Response) => void;
  let signalA: AbortSignal | undefined;
  vi.stubGlobal("fetch", vi.fn((path: string, options: RequestInit) => {
    if (path.includes("/A/calls")) {
      signalA = options.signal as AbortSignal;
      return new Promise<Response>(resolve => { resolveA = resolve; });
    }
    return Promise.resolve(new Response(JSON.stringify(path.includes("usage/summary") ? summary : page([{ ...call, source_call_id: "B-call" }])), { status: 200 }));
  }));
  const view = render(<Consumption account={account} products={[]} />);
  expect(screen.getByText("正在加载调用…")).toBeDefined();
  view.rerender(<Consumption account={{ ...account, id: "B", name: "账户 B" }} products={[]} />);
  expect(await screen.findByText("B-call")).toBeDefined();
  expect(signalA?.aborted).toBe(true);
  await act(async () => { resolveA(new Response(JSON.stringify(page()), { status: 200 })); });
  expect(screen.queryByText("native-call")).toBeNull();
});

it("renders an empty result and lets an errored query restart without a new mutation", async () => {
  let fail = true;
  const methods: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (path: string, options: RequestInit) => {
    methods.push(options.method || "GET");
    return new Response(JSON.stringify(fail ? { error: "service_unavailable" } : path.includes("usage/summary") ? summary : page([])), { status: fail ? 503 : 200 });
  }));
  render(<Consumption account={account} products={[]} />);
  await screen.findAllByRole("alert");
  fail = false;
  fireEvent.click(screen.getByRole("button", { name: "重新查询" }));
  expect(await screen.findByText("当前条件下暂无模型消费")).toBeDefined();
  expect(methods.every(method => method === "GET")).toBe(true);
});

it("shows task columns and reveals full financial details on demand without losing exact assets", async () => {
  vi.stubGlobal("fetch", fetchData());
  render(<Consumption account={account} products={[{id: "notes", name: "Notes"}]} />);
  await screen.findByText("native-call");
  expect(screen.getAllByRole("columnheader").map(header => header.textContent)).toEqual(["时间", "模型", "产品", "扣费", "状态"]);
  const details = screen.getByText("native-call").closest("details")!;
  expect(details).not.toBeNull();
  expect(details.open).toBe(false);
  fireEvent.click(within(details).getByText("查看调用详情"));
  expect(details.open).toBe(true);
  expect(details.textContent).toContain("original-payer");
  expect(details.textContent).toContain("个人统一钱包");
  expect(details.textContent).toContain("CNY/6");
  expect(details.textContent).toContain("预占");
  expect(screen.getByLabelText("按资产列示实扣").textContent).toContain("0.123456");
  expect(screen.getByLabelText("按资产列示实扣").textContent).toContain("9,007,199,254,740,993");
});
it("uses a consumption product hint only as origin and never as the funding wallet", async () => {
  const paths: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    paths.push(path);
    return new Response(JSON.stringify(path.includes("usage/summary") ? summary : page()), {status: 200});
  }));
  render(<Consumption account={account} products={[{id: "notes", name: "Notes"}]} initialProductId="notes" />);
  await screen.findByText("native-call");
  expect((screen.getByLabelText("产品") as HTMLSelectElement).value).toBe("notes");
  expect(paths.every(path => new URL(path, "http://local").searchParams.get("origin_product_id") === "notes")).toBe(true);
  expect(paths.some(path => path.includes("funding_product_id"))).toBe(false);
});
