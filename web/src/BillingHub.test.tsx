// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { BillingHub } from "./BillingHub";
import type { Account, Batch, Capabilities, Offer, Order, Product, Subscription, Wallet } from "./types";
import type { Resource } from "./useResource";

afterEach(cleanup);

const resource = <T,>(data: T): Resource<T> => ({ data, loading: false });
const account: Account = { id: "A", name: "个人账户", kind: "personal", role: "owner" };
const product: Product = { id: "notes", name: "Notes" };
const baseOffer: Offer = {
  id: "basic-month",
  product_id: "notes",
  name: "基础方案",
  account_kind: "both",
  kind: "membership",
  period: "month",
  price: "1000000",
  monthly_credit: "2000000",
  entitlements: { seats: "1" },
  rank: 1,
  validity_months: 1,
  refund_rule: "unused_only_v1",
  status: "published",
  sandbox: false,
};
const empty = <T,>(data: T): Resource<T> => resource(data);

it("shows current plan, grouped offers, usage and the payment boundary", () => {
  const subscription: Subscription = {
    id: "sub-1",
    account_id: "A",
    product_id: "notes",
    offer: baseOffer,
    version: 1,
    anchor: "2026-01-01T00:00:00Z",
    paid_through: "2026-12-31T00:00:00Z",
    status: "active",
  };
  const yearly = { ...baseOffer, id: "pro-year", name: "专业方案", period: "year", price: "10000000", rank: 2 };
  render(
    <BillingHub
      account={account}
      session={{ authenticated: true, sandbox: false, csrf_token: "csrf" }}
      products={[product]}
      offers={resource([baseOffer, yearly])}
      subscriptions={resource([subscription])}
      credits={resource<Batch[]>([])}
      wallets={resource<Wallet[]>([])}
      orders={resource<Order[]>([])}
      capabilities={resource<Capabilities>({ real_payments_configured: false })}
    />,
  );
  expect(screen.getByRole("heading", { name: "套餐与用量" })).toBeDefined();
  expect(screen.getAllByText("当前方案").length).toBeGreaterThan(0);
  expect(screen.getByText("基础方案")).toBeDefined();
  expect(screen.getAllByText("月付方案").length).toBeGreaterThan(0);
  expect(screen.getAllByText("年付方案").length).toBeGreaterThan(0);
  expect(screen.getByText(/真实支付尚未开通/)).toBeDefined();
  expect(screen.getByRole("button", { name: "浏览更多方案" })).toBeDefined();
});

it("shows a clear empty-state path when the account has no active plan", () => {
  render(
    <BillingHub
      account={account}
      session={{ authenticated: true, sandbox: true }}
      products={[product]}
      offers={empty<Offer[]>([])}
      subscriptions={empty<Subscription[]>([])}
      credits={empty<Batch[]>([])}
      wallets={empty<Wallet[]>([])}
      orders={empty<Order[]>([])}
      capabilities={resource<Capabilities>({ sandbox: true })}
    />,
  );
  expect(screen.getByText("还没有生效中的方案")).toBeDefined();
  expect(screen.getByRole("button", { name: "浏览会员方案" })).toBeDefined();
});

it("keeps failed finance resources distinct from valid zero and separates purpose wallets from CREDIT", () => {
  const props = {account, session: {authenticated: true, sandbox: true}, products: [product], offers: resource<Offer[]>([]), subscriptions: resource<Subscription[]>([]), credits: resource<Batch[]>([]), wallets: resource<Wallet[]>([{product_id: "llmrouter", asset: "CNY/6", amount: "123456"}, {product_id: "superwagie", asset: "CNY/6", amount: "0"}]), orders: resource<Order[]>([]), capabilities: resource<Capabilities>({})};
  const view = render(<BillingHub {...props} wallets={{...props.wallets, error: "wallet offline"}} subscriptions={{...props.subscriptions, error: "subscription offline"}} credits={{...props.credits, error: "credit offline"}} />);
  expect(screen.getByText("wallet offline")).toBeDefined();
  expect(screen.getByText("subscription offline")).toBeDefined();
  expect(screen.getByText("credit offline")).toBeDefined();
  expect(screen.queryByText("¥ 0.00")).toBeNull();
  expect(screen.queryByText("还没有生效中的方案")).toBeNull();
  view.rerender(<BillingHub {...props} />);
  expect(screen.getAllByText("¥ 0.123456").length).toBeGreaterThan(0);
  expect(screen.getByText("¥ 0.00")).toBeDefined();
  expect(screen.getByText("0.000000")).toBeDefined();
});

it("filters account/product eligibility and hands off only the chosen offer", () => {
  const selected: Offer[] = [];
  render(<BillingHub account={account} session={{authenticated: true, sandbox: false}} products={[product]} initialProductId="notes" offers={resource([baseOffer, {...baseOffer, id: "team", name: "团队限定", account_kind: "team"}, {...baseOffer, id: "other", name: "其他产品", product_id: "other"}])} subscriptions={resource([])} credits={resource([])} wallets={resource([])} orders={resource([])} capabilities={resource({})} onSelectOffer={offer => selected.push(offer)} />);
  expect(screen.queryByText("团队限定")).toBeNull();
  expect(screen.queryByText("其他产品")).toBeNull();
  fireEvent.click(screen.getByRole("button", {name: "查看方案"}));
  expect(selected).toEqual([baseOffer]);
});

it("shows paid-through and switches the visible billing period", () => {
  const props = {account, session: {authenticated: true, sandbox: true}, products: [product], offers: resource([baseOffer, {...baseOffer, id: "annual", name: "年度方案", period: "year"}]), subscriptions: resource<Subscription[]>([{id: "sub", account_id: "A", product_id: "notes", offer: baseOffer, status: "active", anchor: "2026-01-01T00:00:00Z", paid_through: "2099-12-31T00:00:00Z", version: 1}]), credits: resource<Batch[]>([]), wallets: resource<Wallet[]>([]), orders: resource<Order[]>([]), capabilities: resource<Capabilities>({})};
  render(<BillingHub {...props} />);
  expect(screen.getByText(/已付费至.*2099/)).toBeDefined();
  expect(screen.queryByText("年度方案")).toBeNull();
  fireEvent.click(screen.getByRole("button", {name: "年付方案"}));
  expect(screen.getByText("年度方案")).toBeDefined();
  expect(screen.queryByText("基础方案")).toBeNull();
});

it("front-loads unified cash without claiming it is universally model-spendable", () => {
  render(<BillingHub account={account} session={{authenticated: true, sandbox: true}} products={[product]} offers={resource([])} subscriptions={resource([])} credits={resource([])} wallets={resource([{product_id: "", asset: "CNY/6", amount: "25000000"}])} orders={resource([])} capabilities={resource({})} />);
  expect(screen.getByRole("heading", {name: "统一钱包可用现金"})).toBeDefined();
  expect(screen.getByText(/原生共享现金调用按接入规则使用统一钱包/)).toBeDefined();
});
