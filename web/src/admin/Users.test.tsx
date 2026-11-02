// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { IdentityDirectory, ProductRole } from './Users';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test('product access checkboxes use the admin checkbox control style', () => {
  render(
    <ProductRole
      user="user-1"
      manifest={{
        product_id: 'llmrouter',
        instance_id: 'default',
        name: 'LLMRouter',
        roles: [
          { id: 'operator', name: 'Channel operator', privileged: true },
          { id: 'member', name: 'Member' },
        ],
      }}
      assignment={{ roles: ['member'], active: true, assignment_version: 7 }}
      principal={{ user_id: 'admin', roles: ['super_admin'], permissions: ['*'], enabled: true, version: 1 }}
      onSaved={() => {}}
    />,
  );

  expect(screen.getAllByRole('checkbox').every((input) => input.classList.contains('admin-checkbox'))).toBe(true);
});

test('identity directory explains when provider administration is disabled', async () => {
  const calls: string[] = [];
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    calls.push(path);
    return new Response(JSON.stringify({ directory: false, reason: 'identity provider administration not configured' }), { status: 200 });
  }));
  render(<IdentityDirectory principal={{ user_id: 'admin', roles: ['super_admin'], permissions: ['*'], enabled: true, version: 1 }} />);
  await waitFor(() => {
    const notice = screen.getByRole('status');
    expect(notice.textContent).toContain('身份提供方管理尚未启用。');
    expect(notice.textContent).toContain('当前部署未配置身份提供方管理能力，请先完成身份提供方部署验证。');
  });
  expect(calls.some((path) => path.includes('/admin/v1/identities?'))).toBe(false);
});
