// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterAll, afterEach, beforeAll, expect, test, vi } from 'vitest';
import { AdminConsole } from './AdminConsole';

const principal = { loading: false, data: { enabled: true, user_id: 'root', roles: ['super_admin'], permissions: ['*'], version: 1 } };
const manifests = [
 { product_id: 'llmrouter', instance_id: 'default', name: 'LLMRouter', version: 7, roles: [
  { id: 'channel_operator', name: 'Channel operator', privileged: true, permissions: ['channel:write'] },
  { id: 'member', name: 'Member', permissions: ['product:use'] },
 ] },
 { product_id: 'notes', instance_id: 'default', name: 'Notes', version: 1, roles: [{ id: 'member', name: 'Member', permissions: ['product:use'] }] },
];
const assignments = [
 { product_id: 'llmrouter', instance_id: 'default', roles: ['member'], active: true, assignment_version: 7, explicit: true },
 { product_id: 'notes', instance_id: 'default', roles: ['member'], active: true, assignment_version: 0, explicit: false },
];
const json = (data: unknown) => new Response(JSON.stringify(data), { status: 200, headers: { 'Content-Type': 'application/json' } });
beforeAll(() => {
 HTMLDialogElement.prototype.showModal = function () { this.open = true; };
 HTMLDialogElement.prototype.close = function () { this.open = false; };
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState({}, '', '/'); });
afterAll(() => {
 Reflect.deleteProperty(HTMLDialogElement.prototype, 'showModal');
 Reflect.deleteProperty(HTMLDialogElement.prototype, 'close');
});
function renderDetail() {
 window.history.replaceState({}, '', '/admin/users/u1');
 const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const path = new URL(String(input), 'http://localhost').pathname;
  if (init?.method === 'PUT') return json({});
  if (path === '/admin/v1/users/u1') return json({ user: { id: 'u1', name: '测试用户', active: true, version: 1 }, principal: {} });
  if (path === '/admin/v1/access-products') return json(manifests);
  if (path === '/admin/v1/users/u1/product-access') return json(assignments);
  return json({ items: [], total: 0, as_of: '' });
 });
 vi.stubGlobal('fetch', fetcher);
 const view = render(<AdminConsole principal={principal} csrf="csrf" onReturn={() => {}} onLogout={() => {}} />);
 return { ...view, fetcher };
}
test('产品权限以独立卡片展示访问开关和角色网格', async () => {
 renderDetail();
 expect(await screen.findByRole('heading', { name: 'LLMRouter' })).toBeTruthy();
 expect(screen.getByRole('heading', { name: 'Notes' })).toBeTruthy();
 expect(screen.getByRole('checkbox', { name: '允许访问 LLMRouter' })).toBeTruthy();
 expect(screen.getByText('已选 1 / 2')).toBeTruthy();
 expect(document.querySelectorAll('.admin-access-card')).toHaveLength(2);
 expect(document.querySelectorAll('.admin-role-option')).toHaveLength(3);
});
test('保存产品权限提交当前角色的选中状态、访问状态与授权版本', async () => {
 const { fetcher } = renderDetail();
 const operator = await screen.findByRole('checkbox', { name: /Channel operator/ });
 fireEvent.click(operator);
 fireEvent.click(screen.getAllByRole('button', { name: '保存产品权限' })[0]);
 fireEvent.change(screen.getByLabelText('操作原因'), { target: { value: '核对后调整角色' } });
 fireEvent.click(screen.getByRole('button', { name: '确认提交' }));
 await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(true));
 const [path, init] = fetcher.mock.calls.find(([, init]) => init?.method === 'PUT')!;
 expect(path).toBe('/admin/v1/users/u1/product-access/llmrouter?instance=default');
 expect(JSON.parse(String(init?.body))).toMatchObject({ roles: ['member', 'channel_operator'], active: true, version: 7, reason: '核对后调整角色' });
});
