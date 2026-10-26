// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, expect, it, vi } from "vitest";
import { Catalog, Members, SettingsPage } from "./forms";
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
  session: { authenticated: true, sandbox: false, csrf_token: "csrf" },
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
  fireEvent.click(screen.getByRole("button", { name: "核对并继续" }));
  render(<>{act.mock.calls[0][0].content}</>);
  expect(screen.getByText(/升级保持原服务期限，不发放模型额度/)).toBeDefined();
  expect(screen.queryByText(/立即增加额度|后续每月额度|当前额度账期/)).toBeNull();
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
