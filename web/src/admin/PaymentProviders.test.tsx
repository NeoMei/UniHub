// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, expect, test, vi } from 'vitest';
import { PaymentProviders } from './PaymentProviders';
import { AdminConsole } from './AdminConsole';
import type { PaymentProvider, PaymentProviderStatus } from './paymentProviderTypes';

const principal = { user_id: 'fixture-admin', roles: ['finance_admin'], permissions: ['finance:read', 'finance:write'], enabled: true, version: 3 };
function provider(id: PaymentProvider['id']): PaymentProvider { return { id, enabled: true, configured: true, app_id: `${id}-fixture-app`, merchant_id: id === 'wechat' ? 'fixture-merchant' : '', seller_id: id === 'alipay' ? 'fixture-seller' : '', certificate_serial: id === 'wechat' ? 'fixture-serial' : '', platform_key_ids: id === 'wechat' ? ['fixture-trusted-key'] : [], notify_url: `https://pay.example.invalid/payments/${id}/notify`, return_url: id === 'alipay' ? 'https://pay.example.invalid/' : '', private_key_configured: true, api_v3_key_configured: id === 'wechat', public_key_configured: id === 'alipay', platform_keys_configured: id === 'wechat', missing_fields: [] }; }
const status: PaymentProviderStatus = {
 version: 7, mode: 'direct', managed: true, store_ready: true, configured: true,
 providers: [provider('wechat'), provider('alipay')],
};
const json = (data: unknown, code = 200) => new Response(JSON.stringify(data), { status: code, headers: { 'Content-Type': 'application/json' } });
function transport(reply = status) {
 const writes: { path: string; options: RequestInit; body: Record<string, unknown> }[] = [];
 const fetcher = vi.fn(async (path: string, options: RequestInit) => {
  if (options.method === 'PUT') { writes.push({ path, options, body: JSON.parse(String(options.body)) }); return json(reply); }
  return json(status);
 });
 vi.stubGlobal('fetch', fetcher);
 return { writes, fetcher };
}
async function edit(channel = '微信支付') { fireEvent.click(await screen.findByRole('button', { name: `配置${channel}` })); }
function change(label: string, value: string) { fireEvent.change(screen.getByLabelText(label), { target: { value } }); }
function save() { change('操作原因', 'local fixture configuration'); fireEvent.click(screen.getByRole('button', { name: '保存配置' })); }
beforeAll(() => {
 HTMLDialogElement.prototype.showModal = function () { this.open = true; };
 HTMLDialogElement.prototype.close = function () { this.open = false; };
});
afterAll(() => { Reflect.deleteProperty(HTMLDialogElement.prototype, 'showModal'); Reflect.deleteProperty(HTMLDialogElement.prototype, 'close'); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState({}, '', '/'); });

test('admin route and finance navigation render merchant status with fixed callback URLs', async () => {
 transport(); window.history.replaceState({}, '', '/admin/payment-providers');
 render(<AdminConsole principal={{ loading: false, data: principal }} csrf="fixture-csrf" onReturn={() => {}} onLogout={() => {}} />);
 expect(await screen.findByText(status.providers[0].notify_url)).toBeTruthy();
 expect(screen.getByRole('link', { name: '支付渠道' }).getAttribute('aria-current')).toBe('page');
 expect(screen.getByText(status.providers[1].return_url)).toBeTruthy();
 expect(screen.getByText('配置版本 7 · 正式支付模式 · 有可用收款渠道')).toBeTruthy();
});

test('WeChat save uses current CAS and CSRF, omits blank credentials and applies returned global version', async () => {
 const { writes } = transport({ ...status, version: 8 });
 render(<PaymentProviders principal={principal} csrf="fixture-csrf" />); await edit();
 expect((screen.getByLabelText('商户应用私钥 PEM') as HTMLTextAreaElement).value).toBe('');
 expect(screen.queryByLabelText('支付宝公钥 PEM')).toBeNull();
 save(); await screen.findByText('支付渠道配置已保存。未执行支付或退款。');
 expect(writes).toHaveLength(1);
 expect(writes[0].body).toEqual({ expected_version: 7, reason: 'local fixture configuration', enabled: true, app_id: 'wechat-fixture-app', merchant_id: 'fixture-merchant', certificate_serial: 'fixture-serial', platform_key_ids: ['fixture-trusted-key'] });
 expect(writes[0].options.credentials).toBe('same-origin');
 expect(writes[0].options.headers).toMatchObject({ 'X-CSRF-Token': 'fixture-csrf' });
 await edit('支付宝'); save(); await waitFor(() => expect(writes).toHaveLength(2));
 expect(writes[1].body.expected_version).toBe(8);
 expect(writes[1].body).not.toHaveProperty('merchant_id');
 expect(writes[1].body).not.toHaveProperty('platform_key_ids');
});

test('explicit WeChat credential replacement submits only supplied keys; identity edits clear pending secrets', async () => {
 const { writes } = transport(); render(<PaymentProviders principal={principal} csrf="fixture-csrf" />); await edit();
 change('商户应用私钥 PEM', 'fixture-private-not-a-key'); change('APIv3 密钥', 'fixture-api'); change('微信平台公钥或证书 PEM 1', 'fixture-platform');
 change('商户号', 'fixture-new-merchant');
 expect((screen.getByLabelText('商户应用私钥 PEM') as HTMLTextAreaElement).value).toBe('');
 expect((screen.getByLabelText('APIv3 密钥') as HTMLInputElement).value).toBe('');
 expect((screen.getByLabelText('微信平台公钥或证书 PEM 1') as HTMLTextAreaElement).value).toBe('');
 change('商户应用私钥 PEM', 'fixture-private-not-a-key'); change('APIv3 密钥', 'x'.repeat(32)); change('微信平台公钥或证书 PEM 1', 'fixture-platform');
 fireEvent.click(screen.getByRole('button', { name: '添加可信平台公钥' })); change('可信平台公钥 ID 2', 'next-id'); change('微信平台公钥或证书 PEM 2', 'next-platform-fixture');
 save(); await waitFor(() => expect(writes).toHaveLength(1));
 expect(writes[0].body).toMatchObject({ private_key_pem: 'fixture-private-not-a-key', api_v3_key: 'x'.repeat(32), platform_key_ids: ['fixture-trusted-key', 'next-id'], platform_public_keys: { 'fixture-trusted-key': 'fixture-platform', 'next-id': 'next-platform-fixture' } });
 await screen.findByText('支付渠道配置已保存。未执行支付或退款。'); await edit();
 expect((screen.getByLabelText('商户应用私钥 PEM') as HTMLTextAreaElement).value).toBe('');
});

test('Alipay public-key form, close, unmount and principal changes destroy draft secrets', async () => {
 const { writes } = transport(); const ui = render(<PaymentProviders principal={principal} csrf="fixture-csrf" />); await edit('支付宝');
 change('商户应用私钥 PEM', 'fixture-private'); change('支付宝公钥 PEM', 'fixture-public');
 fireEvent.click(screen.getByRole('button', { name: '关闭' })); await edit('支付宝');
 expect((screen.getByLabelText('支付宝公钥 PEM') as HTMLTextAreaElement).value).toBe('');
 change('支付宝公钥 PEM', 'second-fixture');
 ui.rerender(<PaymentProviders principal={{ ...principal, user_id: 'other-fixture-admin' }} csrf="other-csrf" />);
 await edit('支付宝'); expect((screen.getByLabelText('支付宝公钥 PEM') as HTMLTextAreaElement).value).toBe('');
 change('商户应用私钥 PEM', 'alipay-private-fixture'); change('支付宝公钥 PEM', 'alipay-public-fixture'); save();
 await waitFor(() => expect(writes).toHaveLength(1));
 expect(writes[0].body).toEqual({ expected_version: 7, reason: 'local fixture configuration', enabled: true, app_id: 'alipay-fixture-app', seller_id: 'fixture-seller', private_key_pem: 'alipay-private-fixture', public_key_pem: 'alipay-public-fixture' });
 await screen.findByText('支付渠道配置已保存。未执行支付或退款。'); await edit('支付宝'); change('支付宝公钥 PEM', 'unmount-fixture'); ui.unmount();
 render(<PaymentProviders principal={principal} csrf="fixture-csrf" />); await edit('支付宝'); expect((screen.getByLabelText('支付宝公钥 PEM') as HTMLTextAreaElement).value).toBe('');
});

test.each([409, 403, 503])('failed save %s clears secrets and requires reload before another write', async code => {
 let writes = 0;
 vi.stubGlobal('fetch', vi.fn(async (_path: string, options: RequestInit) => options.method === 'PUT' ? (writes++, json({ error: code === 409 ? 'conflict' : code === 403 ? 'forbidden' : 'payment_configuration_unavailable' }, code)) : json(status)));
 render(<PaymentProviders principal={principal} csrf="fixture-csrf" />); await edit(); change('商户应用私钥 PEM', 'failure-fixture'); save();
 await screen.findByRole('button', { name: '重新加载并核对' });
 expect((screen.getByLabelText('商户应用私钥 PEM') as HTMLTextAreaElement).value).toBe('');
 expect(screen.queryByRole('button', { name: '保存配置' })).toBeNull(); expect(writes).toBe(1);
 fireEvent.click(screen.getByRole('button', { name: '重新加载并核对' })); await edit();
 expect((screen.getByLabelText('操作原因') as HTMLTextAreaElement).value).toBe('');
});

test('read failure stays unavailable and requires successful retry; no empty settings or edit button', async () => {
 let fail = true; vi.stubGlobal('fetch', vi.fn(async () => fail ? json({ error: 'payment_configuration_unavailable' }, 503) : json(status)));
 render(<PaymentProviders principal={principal} csrf="fixture-csrf" />);
 expect(await screen.findByRole('alert')).toBeTruthy(); expect(screen.queryByText('暂无可用收款渠道')).toBeNull(); expect(screen.queryByRole('button', { name: '配置微信支付' })).toBeNull();
 fail = false; fireEvent.click(screen.getByRole('button', { name: '重新加载' })); await edit();
});

test('read-only, static mode, disabled authority and missing CSRF cannot mutate', async () => {
 const { writes } = transport(); const ui = render(<PaymentProviders principal={{ ...principal, permissions: ['finance:read'] }} csrf="fixture-csrf" />);
 await screen.findByText('当前平台角色只有读取权限。'); expect(screen.queryByRole('button', { name: '配置微信支付' })).toBeNull();
 ui.rerender(<PaymentProviders principal={{ ...principal, enabled: false }} csrf="fixture-csrf" />);
 expect(screen.getByRole('alert').textContent).toContain('读取权限');
 ui.rerender(<PaymentProviders principal={principal} />); await screen.findByText('安全凭证已失效，请刷新页面。');
 expect((screen.getByRole('button', { name: '配置微信支付' }) as HTMLButtonElement).disabled).toBe(true); expect(writes).toHaveLength(0);
 ui.unmount(); vi.stubGlobal('fetch', vi.fn(async () => json({ ...status, managed: false })));
 render(<PaymentProviders principal={principal} csrf="fixture-csrf" />); await screen.findByText('当前使用静态配置或未启用托管配置，仅可查看。'); expect(screen.queryByRole('button', { name: '配置微信支付' })).toBeNull();
});

test('old identity GET and pending PUT completions cannot replace new identity state', async () => {
 let resolveOld!: (response: Response) => void; let reads = 0;
 vi.stubGlobal('fetch', vi.fn(() => ++reads === 1 ? new Promise<Response>(resolve => { resolveOld = resolve; }) : Promise.resolve(json({ ...status, version: 12 }))));
 const ui = render(<PaymentProviders principal={principal} csrf="fixture-csrf" />);
 ui.rerender(<PaymentProviders principal={{ ...principal, user_id: 'new-admin' }} csrf="new-csrf" />);
 await screen.findByText('配置版本 12 · 正式支付模式 · 有可用收款渠道');
 await act(async () => resolveOld(json(status))); expect(screen.queryByText('配置版本 7 · 正式支付模式 · 有可用收款渠道')).toBeNull();
 let resolveWrite!: (response: Response) => void;
 vi.stubGlobal('fetch', vi.fn((_path: string, options: RequestInit) => options.method === 'PUT' ? new Promise<Response>(resolve => { resolveWrite = resolve; }) : Promise.resolve(json({ ...status, version: 20 }))));
 await edit(); save(); ui.rerender(<PaymentProviders principal={{ ...principal, user_id: 'third-admin' }} csrf="third-csrf" />);
 await screen.findByText('配置版本 20 · 正式支付模式 · 有可用收款渠道'); await act(async () => resolveWrite(json({ ...status, version: 13 })));
 expect(screen.queryByText('支付渠道配置已保存。未执行支付或退款。')).toBeNull();
 expect(screen.getByText('配置版本 20 · 正式支付模式 · 有可用收款渠道')).toBeTruthy();
});

test('closing an uncertain save cannot restore editable stale status; refresh failure remains unavailable', async () => {
 let readsFail = false;
 vi.stubGlobal('fetch', vi.fn(async (_path: string, options: RequestInit) => options.method === 'PUT' ? Promise.reject(new Error('offline')) : readsFail ? json({ error: 'payment_configuration_unavailable' }, 503) : json(status)));
 render(<PaymentProviders principal={principal} csrf="fixture-csrf" />); await edit(); save();
 await screen.findByRole('button', { name: '重新加载并核对' }); fireEvent.click(screen.getByRole('button', { name: '关闭' }));
 expect((screen.getByRole('button', { name: '配置微信支付' }) as HTMLButtonElement).disabled).toBe(true);
 expect(screen.getByRole('alert').textContent).toContain('当前显示上次读取的配置');
 readsFail = true; fireEvent.click(screen.getByRole('button', { name: '重新加载' })); await screen.findByRole('alert');
 expect(screen.queryByRole('button', { name: '配置微信支付' })).toBeNull();
});

test('invalid configuration clears write-only inputs while allowing a corrected explicit save', async () => {
 vi.stubGlobal('fetch', vi.fn(async (_path: string, options: RequestInit) => options.method === 'PUT' ? json({ error: 'invalid_request' }, 400) : json(status)));
 render(<PaymentProviders principal={principal} csrf="fixture-csrf" />); await edit(); change('商户应用私钥 PEM', 'fixture-invalid-key'); save();
 await screen.findByRole('alert'); expect((screen.getByLabelText('商户应用私钥 PEM') as HTMLTextAreaElement).value).toBe('');
 expect((screen.getByRole('button', { name: '保存配置' }) as HTMLButtonElement).disabled).toBe(false);
});
