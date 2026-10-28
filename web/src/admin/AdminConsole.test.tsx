// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { AdminConsole } from './AdminConsole';
const principal = { loading: false, data: { enabled: true, user_id: 'root', roles: ['super_admin'], permissions: ['*'], version: 1 } };
afterEach(() => {cleanup();vi.unstubAllGlobals();window.history.replaceState({},'', '/');});
function json(data: unknown, status = 200) { return new Response(JSON.stringify(data), {status, headers:{'Content-Type':'application/json'}}); }
test('全量用户有分页并保留当前筛选', async () => {
 window.history.replaceState({},'', '/admin/users');
 const fetcher = vi.fn(async (input: RequestInfo | URL) => {
  const url = new URL(String(input), 'http://localhost');
  if(url.pathname === '/admin/v1/users') return json({items:[{id: url.searchParams.get('cursor') ? 'newest' : 'first',name:url.searchParams.get('cursor')?'第51位用户':'首位用户',active:true,version:1}],total:51,next_cursor:url.searchParams.get('cursor')?'':'page2',as_of:'2026-10-28T00:00:00Z'});
  return json([]);
 });vi.stubGlobal('fetch',fetcher);
 render(<AdminConsole principal={principal} csrf="csrf" onReturn={()=>{}} onLogout={()=>{}} />);
 expect(await screen.findByText('首位用户')).toBeTruthy();
 expect(screen.getByText('共 51 条')).toBeTruthy();
 fireEvent.click(screen.getByRole('button',{name:'下一页'}));
 expect(await screen.findByText('第51位用户')).toBeTruthy();
 expect(fetcher.mock.calls.some(([url])=>String(url).includes('cursor=page2'))).toBe(true);
});
test('权限查询失败可重试，不误报无权限', () => {
 render(<AdminConsole principal={{loading:false,error:'服务暂时不可用，请稍后重试。'}} csrf="csrf" onReturn={()=>{}} onLogout={()=>{}} />);
 expect(screen.getByRole('alert').textContent).toContain('服务暂时不可用');
 expect(screen.queryByText('当前用户没有平台管理权限。')).toBeNull();
});
test('审计员不显示用户写操作', async () => {
 window.history.replaceState({},'', '/admin/users');
 vi.stubGlobal('fetch',vi.fn(async()=>json({items:[],total:0,as_of:''})));
 render(<AdminConsole principal={{loading:false,data:{...principal.data,roles:['auditor'],permissions:['users:read','audit:read']}}} csrf="csrf" onReturn={()=>{}} onLogout={()=>{}} />);
 await waitFor(()=>expect(screen.getByText('暂无符合条件的记录')).toBeTruthy());
 expect(screen.queryByRole('button',{name:'创建用户'})).toBeNull();
});
