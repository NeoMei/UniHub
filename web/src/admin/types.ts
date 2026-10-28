export type Row = Record<string, unknown>;
export type AdminPrincipal = { user_id: string; roles: string[]; permissions: string[]; enabled: boolean; version: number; legacy_user_admin?: boolean };
export type ListPage<T = Row> = { items: T[]; total: number; next_cursor?: string; as_of: string };
export const roleNames: Record<string,string> = {super_admin:'超级管理员',user_admin:'用户管理员',finance_admin:'财务管理员',auditor:'审计员'};
export function can(p: AdminPrincipal | undefined, permission: string) {return !!p?.enabled && (p.permissions.includes('*') || p.permissions.includes(permission));}
export function value(row: Row, path: string): unknown { let v: unknown = row; for (const key of path.split('.')) {if(!v || typeof v!=='object') return undefined;v=(v as Row)[key];}return v; }
export function text(v: unknown): string {if(v===null||v===undefined||v==='')return '—';if(typeof v==='boolean')return v?'是':'否';if(Array.isArray(v))return v.map(text).join('、');if(typeof v==='object')return '查看详情';return String(v);}
export function go(path: string) {window.history.pushState({},'',path);window.dispatchEvent(new PopStateEvent('popstate'));}
export function encoded(v: unknown) { return encodeURIComponent(String(v || '')); }
