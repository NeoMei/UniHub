// @vitest-environment jsdom
import {cleanup,fireEvent,render,screen,waitFor} from '@testing-library/react';
import {afterEach,expect,it,vi} from 'vitest';
import {ProductApplications,ProductUserManagement} from './ProductUsers';
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it('launches products with current roles from server access',async()=>{vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify([{product_id:'notes',instance_id:'default',name:'Notes',entry_url:'https://notes.test',roles:['member'],permissions:['product:use'],active:true}]))));render(<ProductApplications/>);expect((await screen.findByRole('link',{name:'打开 Notes'})).getAttribute('href')).toBe('https://notes.test');expect(screen.getByText('member')).toBeDefined()});
it('persists explicit empty roles and status with independent optimistic versions and CSRF',async()=>{const calls:{path:string,init?:RequestInit}[]=[];vi.stubGlobal('fetch',vi.fn(async(path:string,init?:RequestInit)=>{calls.push({path,init});const data=path==='/v1/user-management/products'?[{product_id:'notes',instance_id:'default',name:'Notes',entry_url:'https://notes.test',version:3,default_roles:['member'],roles:[{id:'member',name:'Member',permissions:['product:use']}]}]:path.startsWith('/v1/user-management/users?')?[{id:'u1',name:'Alice',email:'alice@test',active:true,version:7}]:path.endsWith('/access')?[{product_id:'notes',instance_id:'default',roles:['member'],active:true,assignment_version:0,manifest_version:3,explicit:false}]:path.includes('/access/notes')?{product_id:'notes',instance_id:'default',roles:[],active:true,assignment_version:1,manifest_version:3,explicit:true}:{};return new Response(JSON.stringify(data),{status:200})}));render(<ProductUserManagement csrf='csrf-test'/>);fireEvent.click(await screen.findByRole('button',{name:'管理 Alice'}));await screen.findByLabelText('Member');fireEvent.click(screen.getByLabelText('Member'));fireEvent.click(screen.getByRole('button',{name:'保存 Notes 权限'}));await waitFor(()=>expect(calls.some(c=>c.init?.method==='PUT'&&c.path.includes('/access/notes'))).toBe(true));const update=calls.find(c=>c.init?.method==='PUT'&&c.path.includes('/access/notes'))!;expect(JSON.parse(update.init!.body as string)).toEqual({roles:[],active:true,version:0});expect((update.init!.headers as Record<string,string>)['X-CSRF-Token']).toBe('csrf-test');fireEvent.click(screen.getByRole('button',{name:'停用用户'}));await waitFor(()=>expect(calls.some(c=>c.path.endsWith('/status'))).toBe(true));expect(JSON.parse(calls.find(c=>c.path.endsWith('/status'))!.init!.body as string)).toEqual({active:false,version:7})});

it('preserves another product draft when one assignment saves and discards drafts when switching users', async () => {
  const manifests = ['notes', 'calendar'].map((product) => ({
    product_id: product, instance_id: 'default', name: product, entry_url: `https://${product}.test`,
    version: 1, default_roles: ['member'],
    roles: [{ id: 'member', name: `${product} Member`, permissions: ['product:use'] }],
  }));
  const defaults = manifests.map((manifest) => ({
    product_id: manifest.product_id, instance_id: 'default', roles: ['member'], active: true,
    assignment_version: 0, manifest_version: 1, explicit: false,
  }));
  const persisted = { alice: structuredClone(defaults), bob: structuredClone(defaults) };
  persisted.bob[1] = { ...persisted.bob[1], roles: [], assignment_version: 1, explicit: true };
  const updates: Array<{ path: string; body: { roles: string[]; active: boolean; version: number } }> = [];
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (init?.method === 'PUT') {
      const body = JSON.parse(init.body as string);
      updates.push({ path, body });
      const user = path.includes('/users/alice/') ? 'alice' : 'bob';
      const index = path.includes('/access/notes?') ? 0 : 1;
      const assignment = { ...persisted[user][index], roles: body.roles, active: body.active, assignment_version: body.version + 1, explicit: true };
      persisted[user][index] = assignment;
      return new Response(JSON.stringify(assignment));
    }
    const data = path === '/v1/user-management/products' ? manifests
      : path.startsWith('/v1/user-management/users?') ? [
        { id: 'alice', name: 'Alice', email: 'alice@test', active: true, version: 1 },
        { id: 'bob', name: 'Bob', email: 'bob@test', active: true, version: 1 },
      ] : path.includes('/users/alice/') ? persisted.alice : persisted.bob;
    return new Response(JSON.stringify(data));
  }));
  render(<ProductUserManagement csrf='csrf-test' />);
  fireEvent.click(await screen.findByRole('button', { name: '管理 Alice' }));
  fireEvent.click(await screen.findByLabelText('notes Member'));
  fireEvent.click(screen.getByLabelText('calendar Member'));
  fireEvent.click(screen.getByRole('button', { name: '保存 notes 权限' }));
  await screen.findByText('产品权限已保存。');
  expect((await screen.findByLabelText('calendar Member') as HTMLInputElement).checked).toBe(false);
  fireEvent.click(screen.getByRole('button', { name: '保存 notes 权限' }));
  await waitFor(() => expect(updates).toHaveLength(2));
  expect(updates[1].body).toEqual({ roles: [], active: true, version: 1 });
  fireEvent.click(screen.getByRole('button', { name: '保存 calendar 权限' }));
  await waitFor(() => expect(updates).toHaveLength(3));
  expect(updates[2].body).toEqual({ roles: [], active: true, version: 0 });
  await waitFor(() => expect((screen.getByRole('button', { name: '保存 calendar 权限' }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByLabelText('calendar Member'));
  expect((screen.getByLabelText('calendar Member') as HTMLInputElement).checked).toBe(true);
  fireEvent.click(screen.getByRole('button', { name: '管理 Bob' }));
  await waitFor(() => expect((screen.getByLabelText('calendar Member') as HTMLInputElement).checked).toBe(false));
});
