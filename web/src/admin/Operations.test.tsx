// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { Overview } from './Operations';

const principal = { user_id: 'root', roles: ['super_admin'], permissions: ['*'], enabled: true, version: 1 };
function json(data: unknown, status = 200) { return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } }); }
const days = Array.from({ length: 14 }, (_, i) => ({ date: '2026-10-' + String(16 + i).padStart(2, '0'), new_users: i, calls: i * 10, failed_calls: i % 3 }));
const series = { as_of: '2026-10-29T00:00:00Z', timezone: 'UTC', finance_scope: true, days, pending_refunds: 2, top_failed_models: [{ model: 'gpt-test', calls: 40, failed_calls: 8, failure_rate: 0.2 }] };
function stubTimeseries(handler: (url: URL) => Response | Promise<Response>) {
 const fetcher = vi.fn(async (input: RequestInfo | URL) => {
  const url = new URL(String(input), 'http://localhost');
  if (url.pathname === '/admin/v1/overview/timeseries') return handler(url);
  return json({});
 });
 vi.stubGlobal('fetch', fetcher);
 return fetcher;
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

test('运营趋势在指标条下方渲染折线图与紧凑表格', async () => {
 stubTimeseries(() => json(series));
 render(<Overview principal={principal} />);
 expect(await screen.findByRole('img', { name: /每日新增用户/ })).toBeTruthy();
 expect(document.querySelectorAll('.admin-trend-line').length).toBe(3);
 expect(screen.getByText('gpt-test')).toBeTruthy();
 expect(screen.getByText('20%')).toBeTruthy();
 expect(screen.getByText('待处理退款').nextElementSibling?.textContent).toBe('2');
});

test('趋势加载失败在卡片内提示并可重试', async () => {
 let attempts = 0;
 stubTimeseries(() => {
  attempts++;
  if (attempts === 1) return json({ error: 'database_unavailable' }, 503);
  return json(series);
 });
 render(<Overview principal={principal} />);
 fireEvent.click(await screen.findByRole('button', { name: '重试' }));
 expect(await screen.findByText('gpt-test')).toBeTruthy();
 expect(attempts).toBe(2);
});

test('无财务管理范围时只显示新增用户图并提示', async () => {
 stubTimeseries(() => json({ ...series, finance_scope: false, top_failed_models: [] }));
 render(<Overview principal={{ ...principal, permissions: ['users:read', 'system:read'] }} />);
 expect(await screen.findByRole('img', { name: /每日新增用户/ })).toBeTruthy();
 expect(document.querySelectorAll('.admin-trend-line').length).toBe(1);
 expect(screen.getByText('调用与退款趋势需要财务管理权限。')).toBeTruthy();
});
