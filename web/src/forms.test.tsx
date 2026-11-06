// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, expect, it, vi } from "vitest";
import { Catalog, CreditApplicability, Members, SettingsPage, Wallets } from "./forms";
import type { ActionSpec, Shared } from "./App";
import type { Invitation, Offer, Quote, Subscription } from "./types";
// jsdom does not implement native dialog methods used by the real Modal.
beforeAll(() => {
  HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  HTMLDialogElement.prototype.close = function () { this.open = false; };
});
afterAll(() => {
  Reflect.deleteProperty(HTMLDialogElement.prototype, "showModal");
  Reflect.deleteProperty(HTMLDialogElement.prototype, "close");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const shared: Shared = {
  account: { id: "A", name: "账户 A", kind: "personal", role: "owner" },
  session: { authenticated: true, sandbox: true, csrf_token: "csrf" },
  products: [{ id: "product-a", name: "产品 A" }],
  refresh: () => {},
  act: () => {},
};
const membership: Offer = {
  id: "feature-year",
  product_id: "product-a",
  name: "功能年费",
  account_kind: "both",
  kind: "membership",
  period: "year",
  price: "10000000",
  monthly_credit: "0",
  entitlements: { workspace: "enabled" },
  rank: 2,
  validity_months: 0,
  status: "published",
  sandbox: false,
};
it.each([{ loading: true }, { loading: false, error: "不可用" }, { loading: false, data: {} }, { loading: true, data: { real_payments_configured: true } }, { loading: false, error: "不可用", data: { real_payments_configured: true } }])("blocks real purchases until capability is explicitly confirmed: %j", (capabilities) => {
  const act = vi.fn();
  render(<Catalog {...shared} session={{ ...shared.session, sandbox: false }} act={act} kind="membership" offers={{ data: [membership], loading: false }} subscriptions={[]} capabilities={capabilities} />);
  const purchase = screen.getByRole("button", { name: "购买" }) as HTMLButtonElement;
  expect(purchase.disabled).toBe(true);
  fireEvent.click(purchase);
  expect(act).not.toHaveBeenCalled();
});
it("describes zero-credit annual memberships as feature subscriptions with manual refunds", () => {
  const act = vi.fn<(spec: ActionSpec) => void>();
  render(
    <Catalog {...shared} act={act} kind="membership" offers={{ data: [membership], loading: false }} subscriptions={[]} />,
  );
  expect(screen.getByText(/年付会员 · 纯功能订阅/)).toBeDefined();
  expect(screen.getByText("纯功能订阅，不含模型额度")).toBeDefined();
  expect(screen.queryByText(/按月发放额度|每月包含/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "购买" }));
  render(<>{act.mock.calls[0][0].content}</>);
  expect(screen.getByText("需人工核实权益使用情况")).toBeDefined();
  expect(screen.queryByText("额度发放")).toBeNull();
  expect(screen.queryByText("每月包含额度")).toBeNull();
});
it("keeps monthly grants and add-on quantities visible for credit offers", () => {
  const credited = { ...membership, monthly_credit: "12000000", name: "额度年费" };
  render(
    <>
      <Catalog {...shared} kind="membership" offers={{ data: [credited], loading: false }} subscriptions={[]} />
      <Catalog {...shared} kind="addon" offers={{ data: [{ ...credited, id: "addon", kind: "addon", validity_months: 3 }], loading: false }} subscriptions={[]} />
    </>,
  );
  expect(screen.getByText(/年付会员 · 按月发放额度/)).toBeDefined();
  expect(screen.getByText(/每月包含/)).toBeDefined();
  expect(screen.getByText(/购买后 3 个月有效/)).toBeDefined();
  expect(screen.getAllByText("12.000000")).toHaveLength(2);
  expect(screen.queryByText(/纯功能订阅/)).toBeNull();
});
it("keeps zero-credit upgrade quotes and confirmations free of monthly grant promises", async () => {
  const act = vi.fn<(spec: ActionSpec) => void>();
  const subscription: Subscription = {
    id: "subscription",
    account_id: "A",
    product_id: "product-a",
    offer: { ...membership, id: "basic", rank: 1 },
    version: 1,
    anchor: "2026-01-01T00:00:00Z",
    paid_through: "2099-01-01T00:00:00Z",
    status: "active",
  };
  const quote: Quote = {
    id: "quote",
    account_id: "A",
    subscription_id: subscription.id,
    subscription_version: 1,
    offer: membership,
    amount: "5000000",
    grant_amount: "0",
    period_start: "2026-01-01T00:00:00Z",
    expires_at: "2099-01-01T00:00:00Z",
  };
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(quote), { status: 200 })));
  render(
    <Catalog {...shared} act={act} kind="membership" offers={{ data: [membership], loading: false }} subscriptions={[subscription]} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "获取升级报价" }));
  await screen.findByText("会员升级报价");
  expect(screen.getByText("需人工核实权益使用情况")).toBeDefined();
  expect(screen.queryByText(/当前月增加额度|后续每月额度/)).toBeNull();
  fireEvent.click(await screen.findByRole("button", { name: "核对并继续" }));
  render(<>{act.mock.calls[0][0].content}</>);
  expect(screen.getByText(/升级保持原服务期限，不发放模型额度/)).toBeDefined();
  expect(screen.queryByText(/立即增加额度|后续每月额度|当前额度账期/)).toBeNull();
});
it("shows an account-bound checkout summary before a purchase mutation", () => {
  const act = vi.fn<(spec: ActionSpec) => void>();
  render(
    <Catalog {...shared} act={act} kind="membership" offers={{ data: [{ ...membership, monthly_credit: "12000000" }], loading: false }} subscriptions={[]} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "购买" }));
  render(<>{act.mock.calls[0][0].content}</>);
  expect(act.mock.calls[0][0].path).toBe("/v1/accounts/A/orders");
  expect(screen.getByText("资金来源")).toBeDefined();
  expect(screen.getByText(/产品钱包优先/)).toBeDefined();
});
it("offers wallet recharge presets and explains uncertain operations", () => {
  render(
    <Wallets
      {...shared}
      session={{ ...shared.session, sandbox: true }}
      wallets={{ data: [{ product_id: "", asset: "CNY", amount: "123000000" }], loading: false }}
      operations={{ data: [{ id: "op-1", account_id: "A", kind: "recharge", status: "UNKNOWN", created_at: "2026-11-03T00:00:00Z" }], loading: false }}
      capabilities={{ data: { product_wallet_recharge: true }, loading: false }}
    />,
  );
  expect(screen.getByText(/测试充值仅用于验证流程/)).toBeDefined();
  expect(screen.getByRole("button", { name: "¥ 100" })).toBeDefined();
  fireEvent.click(screen.getByRole("button", { name: "¥ 100" }));
  expect((screen.getByLabelText("充值金额（元）") as HTMLInputElement).value).toBe("100");
  expect(screen.getByText("结果待核实，请勿重复操作")).toBeDefined();
});
it("requires explicit unchecked authorization opt-in and does not auto-enable funding when a product changes", () => {
  const act = vi.fn();
  render(
    <SettingsPage
      account={{ id: "A", name: "账户 A", kind: "personal", role: "owner" }}
      session={{ authenticated: true, sandbox: true, csrf_token: "csrf" }}
      products={[{ id: "product-a", name: "产品 A" }]}
      refresh={() => {}}
      act={act}
      settings={{ data: [], loading: false }}
    />,
  );
  const checkbox = screen.getByRole("checkbox") as HTMLInputElement;
  expect(checkbox.checked).toBe(false);
  fireEvent.change(screen.getByLabelText("产品"), {
    target: { value: "product-a" },
  });
  expect(checkbox.checked).toBe(false);
  fireEvent.click(screen.getByText("核对并保存"));
  expect(act).toHaveBeenCalledOnce();
  expect(act.mock.calls[0][0].body).toMatchObject({
    enabled: false,
    product_id: "product-a",
    purpose: "funding",
    version: 0,
  });
});

it("shows incoming and team pending invitations and accepts an incoming invitation", () => {
  const act = vi.fn<(spec: ActionSpec) => void>();
  const onTeamCreated = vi.fn();
  const incoming: Invitation = { id: "incoming-1", account_id: "team-1", recipient_id: "u1", role: "member" };
  render(
    <Members
      {...shared}
      act={act}
      account={{ id: "personal", name: "个人账户", kind: "personal", role: "owner" }}
      members={{ data: [], loading: false }}
      incomingInvitations={{ data: [incoming], loading: false }}
      teamInvitations={{ data: [], loading: false }}
      canManage={false}
      onTeamCreated={onTeamCreated}
    />,
  );
  expect(screen.getByText("待处理的团队邀请")).toBeDefined();
  expect(screen.getByText("team-1")).toBeDefined();
  fireEvent.click(screen.getByRole("button", { name: "接受邀请 incoming-1" }));
  expect(act).toHaveBeenCalledWith(expect.objectContaining({
    path: "/v1/invitations/incoming-1/accept",
  }));
  act.mock.calls[0][0].onResult?.({ id: "team-1" });
  expect(onTeamCreated).toHaveBeenCalledWith("team-1");
});

it("lets an owner change non-owner roles and revoke a pending team invitation", () => {
  const act = vi.fn<(spec: ActionSpec) => void>();
  const pending: Invitation = { id: "pending-1", account_id: "team-1", recipient_id: "u2", role: "member" };
  render(
    <Members
      {...shared}
      act={act}
      account={{ id: "team-1", name: "团队", kind: "team", role: "owner" }}
      members={{ data: [{ user_id: "u2", name: "成员", email: "", role: "member" }], loading: false }}
      incomingInvitations={{ data: [], loading: false }}
      teamInvitations={{ data: [pending], loading: false }}
      canManage
      onTeamCreated={() => {}}
    />,
  );
  fireEvent.change(screen.getByLabelText("设置成员角色"), { target: { value: "admin" } });
  expect(act).toHaveBeenCalledWith(expect.objectContaining({
    method: "PATCH",
    path: "/v1/accounts/team-1/members/u2",
    body: { role: "admin" },
  }));
  fireEvent.click(screen.getByRole("button", { name: "撤销邀请 pending-1" }));
  expect(act).toHaveBeenCalledWith(expect.objectContaining({
    method: "DELETE",
    path: "/v1/accounts/team-1/invitations/pending-1",
  }));
});

it("defers wallet targeting until the product catalog confirms a hint and never falls back for unknown hints", async () => {
  const act = vi.fn();
  const props = {...shared, act, initialProductId: "llmrouter", wallets: {data: [], loading: false}, operations: {data: [], loading: false}, capabilities: {data: {product_wallet_recharge: true}, loading: false}};
  const view = render(<Wallets {...props} products={[]} />);
  expect((screen.getByLabelText("充值目标钱包") as HTMLSelectElement).value).not.toBe("");
  fireEvent.change(screen.getByLabelText("充值金额（元）"), {target: {value: "5"}});
  fireEvent.click(screen.getByRole("button", {name: "创建测试充值单"}));
  expect(act).not.toHaveBeenCalled();
  view.rerender(<Wallets {...props} products={[{id: "llmrouter", name: "LLMRouter"}]} />);
  await waitFor(() => expect((screen.getByLabelText("充值目标钱包") as HTMLSelectElement).value).toBe("llmrouter"));
  fireEvent.click(screen.getByRole("button", {name: "创建测试充值单"}));
  expect(act).toHaveBeenCalledWith(expect.objectContaining({path: "/v1/accounts/A/recharges", body: {product_id: "llmrouter", amount: "5000000"}}));
  view.unmount(); act.mockClear();
  render(<Wallets {...props} initialProductId="unlisted" />);
  fireEvent.change(screen.getByLabelText("充值金额（元）"), {target: {value: "5"}});
  fireEvent.click(screen.getByRole("button", {name: "创建测试充值单"}));
  expect(act).not.toHaveBeenCalled();
});
it("keeps manual transfer separate from recharge with the same explicit confirmation payload", () => {
  const act = vi.fn();
  render(<Wallets {...shared} act={act} wallets={{data: [], loading: false}} operations={{data: [], loading: false}} />);
  fireEvent.click(screen.getByText("手动划拨（可选）"));
  fireEvent.change(screen.getByLabelText("划拨目标钱包"), {target: {value: "product-a"}});
  fireEvent.change(screen.getByLabelText("划拨金额（元）"), {target: {value: "3"}});
  fireEvent.click(screen.getByRole("button", {name: "从统一钱包划拨"}));
  expect(act).toHaveBeenCalledWith(expect.objectContaining({path: "/v1/accounts/A/transfers", body: {product_id: "product-a", amount: "3000000"}}));
  expect((screen.getByLabelText("充值金额（元）") as HTMLInputElement).value).toBe("");
});
it("preserves failed wallet reads as errors and authoritative zero as zero", () => {
  const props = {...shared, operations: {data: [], loading: false}};
  const view = render(<Wallets {...props} wallets={{data: [{product_id: "llmrouter", asset: "CNY/6", amount: "0"}], loading: false, error: "wallet offline"}} />);
  expect(screen.getByRole("alert").textContent).toContain("wallet offline");
  expect(screen.queryByText("¥ 0.00")).toBeNull();
  view.rerender(<Wallets {...props} wallets={{data: [{product_id: "llmrouter", asset: "CNY/6", amount: "0"}], loading: false}} />);
  expect(screen.getByText("¥ 0.00")).toBeDefined();
});

it("derives CREDIT applicability from the enabled funding mode, including paid-addon products", () => {
  const profile = {product_id: "addon-only", version: 1, subscription: {enabled: false}, included_credits: {enabled: false}, addons: {enabled: true}, usage: {enabled: true, require_subscription: false, funding_modes: ["credits"], default_funding_mode: "credits"}};
  const view = render(<CreditApplicability products={[{id: "addon-only", name: "AddonProduct", billing_profile: profile}]} />);
  expect(screen.getByText(/AddonProduct/)).toBeDefined();
  view.rerender(<CreditApplicability products={[{id: "addon-only", name: "AddonProduct", billing_profile: {...profile, usage: {...profile.usage, funding_modes: ["prepaid_cash"]}}}]} />);
  expect(screen.queryByText(/AddonProduct/)).toBeNull();
});

it("hands off one selected eligible offer and requires a separate purchase confirmation", () => {
  const act = vi.fn<(spec: ActionSpec) => void>();
  const other = {...membership, id: "other", name: "另一个方案"};
  render(<Catalog {...shared} act={act} kind="membership" initialOfferId={membership.id} initialProductId="product-a" offers={{data: [membership, other], loading: false}} subscriptions={[]} />);
  expect(screen.queryByText("另一个方案")).toBeNull();
  expect(act).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", {name: "购买"}));
  expect(act).toHaveBeenCalledTimes(1);
  expect(act.mock.calls[0][0].body).toEqual({offer_id: membership.id});
  expect(act.mock.calls[0][0].path).toContain("/A/orders");
  render(<>{act.mock.calls[0][0].content}</>);
  expect(screen.getByText("产品 A / 功能年费")).toBeDefined();
});

it("keeps browsing, changing filters, and nonfinancial selection mutation-free", () => {
  const act = vi.fn();
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const offers = {data: [membership, {...membership, id: "team", name: "仅团队可买", account_kind: "team"}, {...membership, id: "test", name: "仅测试商品", sandbox: true}, {...membership, id: "draft", name: "草稿商品", status: "draft"}], loading: false};
  render(<Catalog {...shared} account={{...shared.account, role: "member"}} session={{...shared.session, sandbox: false}} act={act} kind="membership" offers={offers} subscriptions={[]} capabilities={{loading: false, data: {real_payments_configured: true}}} />);
  expect(screen.queryByText("仅团队可买")).toBeNull();
  expect(screen.queryByText("仅测试商品")).toBeNull();
  expect(screen.queryByText("草稿商品")).toBeNull();
  fireEvent.change(screen.getByRole("combobox", {name: "方案产品"}), {target: {value: "product-a"}});
  fireEvent.click(screen.getByRole("button", {name: "购买"}));
  expect(act).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
});

it("keeps an unknown product hint unresolved instead of exposing unrelated offers", () => {
  render(<Catalog {...shared} kind="membership" initialProductId="missing-product" offers={{data: [membership], loading: false}} subscriptions={[]} />);
  expect(screen.queryByRole("button", {name: "购买"})).toBeNull();
  expect(screen.getByText("暂无已发布商品")).toBeDefined();
});

it("clears a selected offer when changing products without preparing a purchase", () => {
  const act = vi.fn();
  render(<Catalog {...shared} products={[...shared.products, {id: "product-b", name: "产品 B"}]} act={act} kind="membership" initialOfferId={membership.id} initialProductId="product-a" offers={{data: [membership, {...membership, id: "second-product", product_id: "product-b", name: "产品 B 方案"}], loading: false}} subscriptions={[]} />);
  expect(screen.queryByText("产品 B 方案")).toBeNull();
  fireEvent.change(screen.getByRole("combobox", {name: "方案产品"}), {target: {value: "product-b"}});
  expect(screen.getByText("产品 B 方案")).toBeDefined();
  expect(screen.queryByText("功能年费")).toBeNull();
  expect(act).not.toHaveBeenCalled();
});

it.each([true, false])("ignores a late upgrade quote after product switching (success=%s)", async (success) => {
  let resolveQuote!: (value: Response) => void;
  const act = vi.fn();
  const fetch = vi.fn(() => new Promise<Response>(resolve => {resolveQuote = resolve;}));
  vi.stubGlobal("fetch", fetch);
  const subscription: Subscription = {id: "sub", account_id: "A", product_id: "product-a", offer: {...membership, id: "basic", rank: 1}, version: 1, status: "active", anchor: "2026-01-01T00:00:00Z", paid_through: "2099-01-01T00:00:00Z"};
  render(<Catalog {...shared} act={act} products={[...shared.products, {id: "product-b", name: "产品 B"}]} kind="membership" initialProductId="product-a" offers={{data: [membership, {...membership, id: "b", product_id: "product-b", name: "产品 B 方案"}], loading: false}} subscriptions={[subscription]} />);
  fireEvent.click(screen.getByRole("button", {name: "获取升级报价"}));
  fireEvent.change(screen.getByRole("combobox", {name: "方案产品"}), {target: {value: "product-b"}});
  expect((screen.getByRole("button", {name: "购买"}) as HTMLButtonElement).disabled).toBe(false);
  resolveQuote(new Response(JSON.stringify(success ? {id: "quote", offer: membership, amount: "1000000", grant_amount: "0", period_start: "2026-01-01T00:00:00Z", expires_at: "2099-01-01T00:00:00Z"} : {error: "quote_failed"}), {status: success ? 200 : 500}));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByText(/请求未完成/)).toBeNull();
  expect(act).not.toHaveBeenCalled();
});
it("keeps an unavailable credit offer visible with an actionable reason and no purchase", () => {
 const act = vi.fn();
 const offer = {...membership, monthly_credit: "1000000", sales_readiness: {ready:false, reason:"credit_rates_unavailable"}};
 render(<Catalog {...shared} act={act} kind="membership" offers={{data:[offer],loading:false}} subscriptions={[]} />);
 expect(screen.getByText(/暂无可用的 CREDIT 模型费率/)).toBeDefined();
 expect((screen.getByRole("button",{name:"购买"}) as HTMLButtonElement).disabled).toBe(true);
});
