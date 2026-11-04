// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeAll, expect, test, vi } from 'vitest';
import { Operations } from './Operations';

const principal = { user_id: 'root', roles: ['super_admin'], permissions: ['*'], enabled: true, version: 1 };
const json = (data: unknown) => new Response(JSON.stringify(data), { status: 200, headers: { 'Content-Type': 'application/json' } });
beforeAll(() => {
 HTMLDialogElement.prototype.showModal = function () { this.open = true; };
 HTMLDialogElement.prototype.close = function () { this.open = false; };
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState({}, '', '/admin'); });
function list(items: Record<string, unknown>[]) {
 const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => json(init?.method === 'POST' ? {} : { items, total: items.length, as_of: '2026-11-04T00:00:00Z' }));
 vi.stubGlobal('fetch', fetcher);
 return fetcher;
}
function fill(label: string, value: string) { fireEvent.change(screen.getByLabelText(label), { target: { value } }); }

test('MANUAL_REVIEW exposes only reject and sends the original refund version', async () => {
 const fetcher = list([{ id: 'order/a', account_id: 'payer', refund_status: 'MANUAL_REVIEW', version: 'v-original' }]);
 render(<Operations resource="refunds" principal={principal} csrf="csrf" />);
 fireEvent.click(await screen.findByRole('button', { name: '拒绝退款申请' }));
 expect(screen.queryByRole('button', { name: /批准|现金退款/ })).toBeNull();
 fill('操作原因', '核对后拒绝');
 fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(true));
 const [path, init] = fetcher.mock.calls.find(([, init]) => init?.method === 'POST')!;
 expect(path).toBe('/admin/v1/accounts/payer/orders/order%2Fa/refund-review');
 expect(JSON.parse(String(init?.body))).toEqual({ version: 'v-original', decision: 'reject', reason: '核对后拒绝' });
});

test('calendar date filters and export use local midnight and the next local day as an exclusive end', async () => {
 const fetcher = list([]);
 render(<Operations resource="calls" principal={principal} />);
 fill('开始日期', '2026-03-08'); fill('结束日期', '2026-03-08');
 const from = new Date(2026, 2, 8).toISOString(), to = new Date(2026, 2, 9).toISOString();
 await waitFor(() => { const params = new URL(String(fetcher.mock.calls.at(-1)![0]), 'http://localhost').searchParams; expect(params.get('from')).toBe(from); expect(params.get('to')).toBe(to); });
 const params = new URL((screen.getByRole('link', { name: '导出筛选结果' }) as HTMLAnchorElement).href).searchParams;
 expect(params.get('from')).toBe(from); expect(params.get('to')).toBe(to);
});

test.each(['billing_commands', 'wallet_operations'])('UNKNOWN %s reconciles the original command without creating a new payment', async (source) => {
 const fetcher = list([{ id: 'original/a', source, status: 'UNKNOWN', version: 'UNKNOWN', account_id: 'payer' }]);
 render(<Operations resource="transactions" principal={principal} csrf="csrf" />);
 fireEvent.click(await screen.findByRole('button', { name: '核对原操作' }));
 expect(screen.getAllByText(/原始引用/)[0]).toBeDefined();
 fill('操作原因', '核对原交易'); fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(true));
 const [path, init] = fetcher.mock.calls.find(([, init]) => init?.method === 'POST')!;
 expect(path).toBe('/admin/v1/transactions/original%2Fa/reconcile');
 expect(JSON.parse(String(init?.body))).toEqual({ source, version: 'UNKNOWN', reason: '核对原交易' });
 expect((init?.headers as Record<string, string>)['Idempotency-Key']).toBeTruthy();
});

test('reconciliation is hidden for settled records, unknown sources, and readers', async () => {
 list([{ id: 'a', source: 'billing_commands', status: 'SETTLED', version: 'SETTLED' }, { id: 'b', source: 'product_runtime_calls', status: 'UNKNOWN', version: 'UNKNOWN' }]);
 const r = render(<Operations resource="transactions" principal={principal} />);
 await screen.findAllByText('SETTLED');
 expect(screen.queryByRole('button', { name: '核对原操作' })).toBeNull();
 r.unmount(); list([{ id: 'c', source: 'billing_commands', status: 'UNKNOWN', version: 'UNKNOWN' }]);
 render(<Operations resource="transactions" principal={{ ...principal, permissions: ['finance:read'] }} />);
 await screen.findAllByText('UNKNOWN'); expect(screen.queryByRole('button', { name: '核对原操作' })).toBeNull();
});

test('metered image-edit rate submits native terms with exact price strings and no legacy token fields', async () => {
 const fetcher = list([]);
 render(<Operations resource="rates" principal={principal} csrf="csrf" />);
 fireEvent.click(screen.getByRole('button', { name: '发布费率版本' }));
 fill('费率标识', 'image-edit-v1'); fill('模型', 'image-model'); fill('资产', 'CNY/6');
 fill('计价协议', 'metered_v2'); fill('操作', 'image_edit');
 expect(screen.queryByLabelText('输入价格分子')).toBeNull();
 fill('图片价格分子', '9007199254740993123'); fill('图片价格分母', '1000000');
 fill('计费策略标识', 'successful_outputs_v1'); fill('操作原因', '发布图片编辑费率');
 fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(true));
 const body = JSON.parse(String(fetcher.mock.calls.find(([, init]) => init?.method === 'POST')![1]?.body));
 expect(body.rate).toEqual({ id: 'image-edit-v1', model: 'image-model', asset: 'CNY/6', status: 'published', semantics: 'metered_v2', metered: { version: 'metered_v2', operation: 'image_edit', policy: 'successful_outputs_v1', terms: [{ key: 'images', unit: 'image', price: { numerator: '9007199254740993123', denominator: '1000000' } }] } });
 expect(body.version).toBe(0);
});

test('video provider units require a namespace and preserve decimal step and minimum quantities', async () => {
 const fetcher = list([]);
 render(<Operations resource="rates" principal={principal} csrf="csrf" />);
 fireEvent.click(screen.getByRole('button', { name: '发布费率版本' }));
 fill('费率标识', 'video-v1'); fill('模型', 'video-model'); fill('计价协议', 'metered_v2'); fill('操作', 'video'); fill('计量单位', 'provider_unit');
 fill('供应商计量命名空间', 'wan.frames'); fill('计量价格分子', '1'); fill('计量价格分母', '2');
 fill('计费步长（可选）', '0.125'); fill('最低计费量（可选）', '1.5'); fill('操作原因', '视频原生单位');
 fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(true));
 const body = JSON.parse(String(fetcher.mock.calls.find(([, init]) => init?.method === 'POST')![1]?.body));
 expect(body.rate.metered.terms[0]).toEqual({ key: 'provider', unit: 'provider_unit', provider_unit: 'wan.frames', price: { numerator: '1', denominator: '2' }, step: { value: '125', scale: 3 }, minimum: { value: '15', scale: 1 } });
});

test('legacy token publication keeps its five exact rationals and no metered contract', async () => {
 const fetcher = list([]);
 render(<Operations resource="rates" principal={principal} csrf="csrf" />);
 fireEvent.click(screen.getByRole('button', { name: '发布费率版本' }));
 fill('费率标识', 'chat-v1'); fill('模型', 'chat-model'); fill('输入价格分子', '7'); fill('输入价格分母', '1000000'); fill('操作原因', '保持旧 token 费率');
 fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(true));
 const body = JSON.parse(String(fetcher.mock.calls.find(([, init]) => init?.method === 'POST')![1]?.body));
 expect(body.rate.semantics).toBe('exclusive_v1'); expect(body.rate.metered).toBeUndefined();
 expect(body.rate.input).toEqual({ numerator: '7', denominator: '1000000' });
 for (const dimension of ['output','cache_read','cache_write','reasoning']) expect(body.rate[dimension]).toEqual({ numerator: '0', denominator: '1' });
});

test('zero metered billing step is rejected before submitting a financial mutation', async () => {
 const fetcher = list([]);
 render(<Operations resource="rates" principal={principal} csrf="csrf" />);
 fireEvent.click(screen.getByRole('button', { name: '发布费率版本' }));
 fill('费率标识', 'speech-v1'); fill('模型', 'speech-model'); fill('计价协议', 'metered_v2'); fill('操作', 'speech');
 fill('计费步长（可选）', '0'); fill('操作原因', '无效步长'); fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 expect(await screen.findByText(/计费步长必须大于零/)).toBeDefined();
 expect(fetcher.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
});

test('call monitoring preserves native asset amounts, funding owner and safe waiver status', async () => {
 list([{ call_id: 'product_runtime:1', source: 'product_runtime_calls', product_id: 'notes', instance_id: 'prod', operation: 'native.cost', asset: 'LR_QUOTA/0', held: '900719925474099312345', charged: '7', refunded: '900719925474099312338', funding_mode: 'legacy_quota', funding_product_id: 'notes', price_version: 'native-v1', price_owner: 'product', waiver: { customer_status: 'released', provider_state: 'pending_review' } }]);
 render(<Operations resource="calls" principal={principal} />);
 expect(await screen.findByText('LR_QUOTA/0')).toBeDefined();
 expect(screen.getByText('900719925474099312345')).toBeDefined();
 expect(screen.getByText('900719925474099312338')).toBeDefined();
 expect(screen.getByText('native.cost')).toBeDefined();
 expect(screen.getByRole('columnheader', { name: '资金归属产品' })).toBeDefined();
 expect(screen.getByText('released')).toBeDefined(); expect(screen.getByText('pending_review')).toBeDefined();
});

test('rate list shows native metered terms beside explicit operation', async () => {
 list([{ id: 'image', model: 'image-model', semantics: 'metered_v2', asset: 'CNY/6', metered: { operation: 'image_edit', terms: [{ key: 'images', unit: 'image', price: { numerator: '9007199254740993123', denominator: '1000000' } }] } }]);
 render(<Operations resource="rates" principal={principal} />);
 expect(await screen.findByText('image_edit')).toBeDefined();
 expect(screen.getByText('images: 9007199254740993123/1000000 image')).toBeDefined();
});

test('native recharge preserves original receipt identity and unknown payment environment', async () => {
 list([{ id: 'product-payment:1', source: 'product_runtime', source_id: 'original-payment', asset: 'LR_QUOTA/0', amount: '100', receipt_verification: 'product_verified' }]);
 render(<Operations resource="recharges" principal={principal} />);
 expect(await screen.findByText('LR_QUOTA/0')).toBeDefined();
 expect(screen.getByText('original-payment')).toBeDefined();
 expect(screen.getByText('原产品已核验回执')).toBeDefined();
 expect(screen.getByText('环境未知')).toBeDefined();
 expect(screen.queryByText('真实收款')).toBeNull();
});
