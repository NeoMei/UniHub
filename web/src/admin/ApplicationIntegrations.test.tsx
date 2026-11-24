// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { AdminConsole } from './AdminConsole';

const endpoint = '/admin/v1/application-integrations';
const principal = {
  enabled: true, user_id: 'root', roles: ['super_admin'], permissions: ['*'], version: 1,
};
const fields = {
  name: '示例知识库',
  origin: 'https://wiki.example.test',
  entry_url: 'https://wiki.example.test/connect',
  callback_url: 'https://wiki.example.test/connect/return',
  native_login_url: 'https://wiki.example.test/login',
};
const draft = {
  ...fields, product_id: 'wiki', instance_id: 'default', client_id: 'bridge.wiki.default',
  source: 'managed', state: 'draft', version: 1, credential_configured: false,
  runtime_configured: false, browser_sso: true, inbound_verified: false,
  outbound_verified: false, hub_origin: 'https://hub.example.test',
};
const appPath = endpoint + '/wiki/default';
const respond = (data: unknown, status = 200) => new Response(JSON.stringify(data), {
  status, headers: { 'Content-Type': 'application/json' },
});

function mount(path = '/admin/applications', data = principal) {
  window.history.replaceState({}, '', path);
  return render(
    <AdminConsole principal={{ loading: false, data }} csrf="test-csrf" onReturn={() => {}} onLogout={() => {}} />,
  );
}

function click(name: string) {
  fireEvent.click(screen.getByRole('button', { name }));
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByRole('textbox', { name: label }), { target: { value } });
}

function submitted(fetcher: ReturnType<typeof mockApi>) {
  return fetcher.mock.calls
    .filter(([, options]) => options?.method !== 'GET')
    .map(([path, options]) => ({
      path, method: options?.method, csrf: new Headers(options?.headers).get('X-CSRF-Token'),
      body: JSON.parse(String(options?.body)),
    }));
}

function mockApi(handler: (path: string, options?: RequestInit) => Response) {
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => handler(path, options));
  vi.stubGlobal('fetch', fetcher);
  return fetcher;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.history.replaceState({}, '', '/');
});

test.each([
  { ...principal, enabled: false, roles: [], permissions: [] },
  { ...principal, roles: ['user_admin'], permissions: ['products:read', 'products:write'] },
  { ...principal, roles: ['finance_admin'], permissions: ['*'] },
])('blocks application management without the super_admin role ($roles)', (data) => {
  const fetcher = mockApi(() => respond([]));
  mount('/admin/applications', data);
  expect(screen.queryByRole('link', { name: '应用接入与管理' })).toBeNull();
  expect(screen.queryByRole('button', { name: '新建接入' })).toBeNull();
  expect(screen.getByRole('alert')).toBeTruthy();
  expect(fetcher).not.toHaveBeenCalled();
});

test('super_admin can navigate to applications and view a deployment without mutation controls', async () => {
  const deployed = {
    ...draft, name: '既有部署', source: 'deployment', state: 'configured', version: 0,
    credential_configured: true, browser_sso: true, runtime_configured: true,
    inbound_verified: false, outbound_verified: false,
  };
  mockApi(() => respond([deployed]));
  mount('/admin/onboarding', { ...principal, roles: ['auditor', 'super_admin'] });
  expect(screen.getByRole('heading', { name: '三方接入工作台' })).toBeTruthy();
  const nav = within(screen.getByRole('navigation', { name: '平台管理导航' }));
  fireEvent.click(nav.getByRole('link', { name: '应用接入与管理' }));
  expect(window.location.pathname).toBe('/admin/applications');
  expect(nav.getByRole('link', { name: '应用接入与管理' }).getAttribute('aria-current')).toBe('page');
  const row = await screen.findByRole('row', { name: /既有部署.*部署管理.*已配置/ });
  fireEvent.click(within(row).getByRole('button', { name: /查看.*既有部署/ }));
  expect(screen.getByText(fields.callback_url)).toBeTruthy();
  expect(screen.getByText('bridge.wiki.default')).toBeTruthy();
  expect(screen.getAllByText('未验证')).toHaveLength(2);
  expect(screen.queryByRole('button', { name: /签发服务凭据|编辑草稿|启用试点|正式发布|暂停接入/ })).toBeNull();
});

test('validates a draft and creates it with exact URLs and the default instance', async () => {
  const fetcher = mockApi((_, options) => options?.method === 'POST' ? respond(draft, 201) : respond([]));
  mount();
  await screen.findByText('暂无应用接入记录');
  click('新建接入');
  fill('产品标识', 'wiki');
  fill('应用名称', fields.name);
  fill('产品来源', fields.origin);
  fill('应用启动地址', fields.entry_url);
  fill('精确回调地址', 'https://elsewhere.example.test/connect/return');
  fill('原生登录地址', fields.native_login_url);
  click('创建草稿');
  expect(screen.getByRole('alert').textContent).toContain('精确回调地址');
  expect(submitted(fetcher)).toEqual([]);

  fill('精确回调地址', fields.callback_url);
  click('创建草稿');
  expect(await screen.findByRole('heading', { name: fields.name })).toBeTruthy();
  expect(submitted(fetcher)).toEqual([
    { path: endpoint, method: 'POST', csrf: 'test-csrf', body: { product_id: 'wiki', instance_id: 'default', ...fields } },
  ]);
});

test('saves a draft with its latest version and keeps its identifiers fixed', async () => {
  const fetcher = mockApi((_, options) => options?.method === 'PUT'
    ? respond({ ...draft, name: '编辑后的应用', version: 8 })
    : respond([{ ...draft, version: 7 }]));
  mount();
  fireEvent.click(await screen.findByRole('button', { name: /查看.*示例知识库/ }));
  click('编辑草稿');
  expect(screen.queryByRole('textbox', { name: '产品标识' })).toBeNull();
  fill('应用名称', '编辑后的应用');
  click('保存草稿');
  expect(await screen.findByRole('heading', { name: '编辑后的应用' })).toBeTruthy();
  expect(submitted(fetcher)).toEqual([
    { path: appPath, method: 'PUT', csrf: 'test-csrf', body: { ...fields, name: '编辑后的应用', version: 7 } },
  ]);
});

test('issues and copies a single-view credential and removes it from view when the admin dismisses it', async () => {
  const token = 'example-single-view-token';
  const copy = vi.fn(async () => {});
  vi.stubGlobal('navigator', Object.assign(Object.create(window.navigator), { clipboard: { writeText: copy } }));
  const fetcher = mockApi((path) => path.endsWith('/credential')
    ? respond({ application: { ...draft, version: 2, credential_configured: true }, token })
    : respond([draft]));
  const storageWrite = vi.spyOn(Storage.prototype, 'setItem');
  mount();
  fireEvent.click(await screen.findByRole('button', { name: /查看.*示例知识库/ }));
  click('签发服务凭据');
  expect(await screen.findByDisplayValue(token)).toBeTruthy();
  click('复制凭据');
  await waitFor(() => expect(copy).toHaveBeenCalledWith(token));
  expect(submitted(fetcher)).toEqual([
    { path: appPath + '/credential', method: 'POST', csrf: 'test-csrf', body: { version: 1 } },
  ]);
  expect(storageWrite).not.toHaveBeenCalled();
  click('隐藏凭据');
  expect(screen.queryByDisplayValue(token)).toBeNull();
  expect(screen.getByRole('button', { name: '启用试点' })).toHaveProperty('disabled', false);
});

test('publishes only after the API verifies both login directions and sends the returned versions', async () => {
  let current = { ...draft, credential_configured: true, version: 2 };
  const fetcher = mockApi((path, options) => {
    if (path.endsWith('/state')) {
      const { state } = JSON.parse(String(options?.body));
      current = { ...current, state, version: current.version + 1 };
      return respond(current);
    }
    return respond([current]);
  });
  mount();
  fireEvent.click(await screen.findByRole('button', { name: /查看.*示例知识库/ }));
  click('启用试点');
  const publish = await screen.findByRole('button', { name: '正式发布' });
  expect(publish).toHaveProperty('disabled', true);
  expect(screen.queryByRole('button', { name: /签发服务凭据/ })).toBeNull();

  current = { ...current, inbound_verified: true, version: 4 };
  click('刷新状态');
  await waitFor(() => expect(screen.getByText('已验证')).toBeTruthy());
  expect(screen.getByRole('button', { name: '正式发布' })).toHaveProperty('disabled', true);

  current = { ...current, outbound_verified: true, version: 5 };
  click('刷新状态');
  await waitFor(() => expect(screen.getByRole('button', { name: '正式发布' })).toHaveProperty('disabled', false));
  click('正式发布');
  await screen.findByRole('row', { name: /示例知识库.*正式运行/ });
  click('暂停接入');
  expect(await screen.findByRole('button', { name: '重新签发服务凭据' })).toBeTruthy();
  expect(submitted(fetcher)).toEqual([
    { path: appPath + '/state', method: 'POST', csrf: 'test-csrf', body: { version: 2, state: 'pilot' } },
    { path: appPath + '/state', method: 'POST', csrf: 'test-csrf', body: { version: 5, state: 'active' } },
    { path: appPath + '/state', method: 'POST', csrf: 'test-csrf', body: { version: 6, state: 'paused' } },
  ]);
});

test('keeps unsaved changes when a save conflicts and allows cancel after a refresh failure', async () => {
  let listStatus = 200;
  const fetcher = mockApi((_, options) => {
    if (options?.method === 'PUT') return respond({ error: 'conflict' }, 409);
    return listStatus === 200 ? respond([draft]) : respond({ error: 'service_unavailable' }, listStatus);
  });
  mount();
  fireEvent.click(await screen.findByRole('button', { name: /查看.*示例知识库/ }));
  click('编辑草稿');
  fill('应用名称', '未保存的名称');
  click('保存草稿');
  expect((await screen.findByRole('alert')).textContent).toContain('冲突');
  expect(screen.getByDisplayValue('未保存的名称')).toBeTruthy();

  listStatus = 503;
  click('刷新状态');
  expect((await screen.findByRole('alert')).textContent).toContain('服务暂时不可用');
  expect(screen.getByRole('button', { name: '保存草稿' })).toHaveProperty('disabled', true);
  expect(screen.getByRole('button', { name: '取消' })).toHaveProperty('disabled', false);
  click('取消');
  expect(screen.getByRole('heading', { name: fields.name })).toBeTruthy();
  expect(submitted(fetcher)).toHaveLength(1);
});

test('a refreshed application list never reveals the issued token again', async () => {
  const token = 'example-refresh-invalidates-reveal';
  const issued = { ...draft, version: 2, credential_configured: true };
  let current = draft;
  mockApi((path) => {
    if (path.endsWith('/credential')) {
      current = issued;
      return respond({ application: issued, token });
    }
    return respond([current]);
  });
  mount();
  fireEvent.click(await screen.findByRole('button', { name: /查看.*示例知识库/ }));
  click('签发服务凭据');
  expect(await screen.findByDisplayValue(token)).toBeTruthy();
  click('刷新状态');
  await screen.findByRole('row', { name: /示例知识库/ });
  expect(screen.queryByDisplayValue(token)).toBeNull();
  expect(screen.getByRole('button', { name: '重新签发服务凭据' })).toHaveProperty('disabled', false);
});
