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

test.each([0, 12])("offer editor preserves add-on validity_months=%i when saving a draft", async (validityMonths) => {
  const offer = { id: "addon", product_id: "superwagie", name: "积分加量包", account_kind: "both", kind: "addon", period: "", price: "1000000", monthly_credit: "2000000", entitlements: {}, rank: 0, validity_months: validityMonths, refund_rule: "unused_only_v1", upgrade_rule: "", status: "draft", sandbox: false, version: 1 };
  let saved: { offer: { validity_months: number } } | undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
    if (options?.method === "PUT") { saved = JSON.parse(String(options.body)); return json({}); }
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/admin/v1/offers") return json({ items: [offer], total: 1, as_of: "" });
    return json({});
  }));
  render(<Operations resource="offers" principal={principal} csrf="csrf" />);
  fireEvent.click(await screen.findByRole("button", { name: "编辑草稿" }));
  expect((screen.getByLabelText(/有效月数/) as HTMLInputElement).value).toBe(String(validityMonths));
  fireEvent.change(screen.getByLabelText("操作原因"), { target: { value: "保留加量包有效期" } });
  fireEvent.click(screen.getByRole("button", { name: "确认提交" }));
  await screen.findByRole("button", { name: "编辑草稿" });
  expect(saved?.offer.validity_months).toBe(validityMonths);
});

test("credit batch list describes a null deadline as perpetual", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => json({ items: [{ id: "perpetual", account_id: "account", product_id: "superwagie", available: "1", reserved: "0", consumed: "0", expires_at: null }], total: 1, as_of: "" })));
  render(<Operations resource="credit-batches" principal={principal} csrf="csrf" />);
  expect(await screen.findByText("永久有效")).toBeDefined();
});

const nativeConversion = { version: 'sw-native-cny10-20261109-v1', numerator: '146', denominator: '1', rounding: 'lr-cumulative-ceil-v1' };
const nativeProfile = { product_id: 'superwagie', version: 2, subscription: { enabled: true }, included_credits: { enabled: true }, addons: { enabled: true }, usage: { enabled: true, require_subscription: true, require_subscription_for_credits: true, funding_modes: ['credits', 'prepaid_cash'], default_funding_mode: 'prepaid_cash', models: [] }, native_credit_conversion: nativeConversion };

test.each([[], null, ['fixture-model']])('unrelated profile edits preserve exact tariff, subscription policy, models %j and CAS', async models => {
 const writes: { options: RequestInit; body: typeof nativeProfile & { profile: typeof nativeProfile } }[] = [];
 vi.stubGlobal('fetch', vi.fn(async (_path: string, options: RequestInit) => {
  if (options.method === 'PUT') { writes.push({ options, body: JSON.parse(String(options.body)) }); return json({}); }
  return json({ items: [{ ...nativeProfile, usage: { ...nativeProfile.usage, models } }], total: 1, as_of: '' });
 }));
 render(<Operations resource="billing-profiles" principal={principal} csrf="fixture-csrf" />);
 fireEvent.click(await screen.findByRole('button', { name: '更新计费配置' }));
 fireEvent.change(screen.getByLabelText('加量包能力'), { target: { value: 'no' } });
 fireEvent.change(screen.getByLabelText('操作原因'), { target: { value: 'fixture unrelated change' } });
 fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 await screen.findByRole('button', { name: '更新计费配置' });
 expect(writes).toHaveLength(1); expect(writes[0].body.version).toBe(2);
 expect(writes[0].body.profile).toEqual({ ...nativeProfile, addons: { enabled: false }, usage: { ...nativeProfile.usage, models } });
 expect(writes[0].options.headers).toMatchObject({ 'X-CSRF-Token': 'fixture-csrf' });
});

test.each(['update', 'remove'])('native conversion %s requires an explicit action; credit-only membership edits remain independent', async action => {
 let payload: { profile: typeof nativeProfile } | undefined;
 vi.stubGlobal('fetch', vi.fn(async (_path: string, options: RequestInit) => {
  if (options.method === 'PUT') { payload = JSON.parse(String(options.body)); return json({}); }
  return json({ items: [nativeProfile], total: 1, as_of: '' });
 }));
 render(<Operations resource="billing-profiles" principal={principal} csrf="fixture-csrf" />);
 fireEvent.click(await screen.findByRole('button', { name: '更新计费配置' }));
 fireEvent.change(screen.getByLabelText(/原生 CREDIT 换算操作/), { target: { value: action } });
 fireEvent.change(screen.getByLabelText(/原生换算版本/), { target: { value: 'new-fixture-tariff' } });
 fireEvent.change(screen.getByLabelText(/原生换算分子/), { target: { value: '9007199254740993' } });
 fireEvent.change(screen.getByLabelText('使用 CREDIT 时要求订阅'), { target: { value: 'no' } });
 fireEvent.change(screen.getByLabelText('操作原因'), { target: { value: 'fixture explicit policy' } });
 fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 await screen.findByRole('button', { name: '更新计费配置' });
 expect(payload?.profile.native_credit_conversion).toEqual(action === 'remove' ? null : { ...nativeConversion, version: 'new-fixture-tariff', numerator: '9007199254740993' });
 expect(payload?.profile.usage.require_subscription_for_credits).toBe(false);
 expect(payload?.profile.usage.require_subscription).toBe(true);
 expect(payload?.profile.usage.default_funding_mode).toBe('prepaid_cash');
});

test('profile conflict preserves the displayed version and cannot report success', async () => {
 let writes = 0;
 vi.stubGlobal('fetch', vi.fn(async (_path: string, options: RequestInit) => {
  if (options.method === 'PUT') { writes++; return new Response(JSON.stringify({ error: 'conflict' }), { status: 409 }); }
  return json({ items: [nativeProfile], total: 1, as_of: '' });
 }));
 render(<Operations resource="billing-profiles" principal={principal} csrf="fixture-csrf" />);
 fireEvent.click(await screen.findByRole('button', { name: '更新计费配置' }));
 fireEvent.change(screen.getByLabelText('操作原因'), { target: { value: 'fixture conflict' } });
 fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 expect((await screen.findByRole('alert')).textContent).toContain('状态已变化');
 expect(screen.getByRole('dialog')).toBeTruthy(); expect(writes).toBe(1);
});
