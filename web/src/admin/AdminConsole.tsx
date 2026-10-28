import { useEffect, useState } from 'react';
import { ArrowLeft, LayoutDashboard, Users, ShieldCheck, Building2, KeyRound, Activity, Wallet, Receipt, RotateCcw, Package, SlidersHorizontal, ScrollText, HeartPulse, LogOut, Boxes } from 'lucide-react';
import type { Resource } from '../useResource';
import { Notice } from '../ui';
import { Users as UserManagement, IdentityDirectory } from './Users';
import { Operations } from './Operations';
import { can, go, roleNames } from './types';
import type { AdminPrincipal } from './types';
import './admin.css';
const nav=[
 {id:'overview',label:'管理概览',permission:'users:read',icon:LayoutDashboard},
 {id:'users',label:'用户管理',permission:'users:read',icon:Users},
 {id:'identities',label:'注册身份',permission:'identity:manage',icon:Users},
 {id:'admins',label:'平台管理员',permission:'admins:manage',icon:ShieldCheck},
 {id:'accounts',label:'账户与团队',permission:'accounts:read',icon:Building2},
 {id:'products',label:'产品目录',permission:'products:read',icon:Boxes},
 {id:'api-keys',label:'API Key',permission:'keys:read',icon:KeyRound},
 {id:'calls',label:'调用监控',permission:'calls:read',icon:Activity},
 {id:'usage-budgets',label:'使用预算',permission:'finance:read',icon:SlidersHorizontal},
 {id:'wallets',label:'钱包与资金',permission:'finance:read',icon:Wallet},
 {id:'recharges',label:'充值记录',permission:'finance:read',icon:Receipt},
 {id:'transactions',label:'账务操作',permission:'finance:read',icon:Receipt},
 {id:'orders',label:'订单',permission:'finance:read',icon:Receipt},
 {id:'refunds',label:'退款复核',permission:'finance:read',icon:RotateCcw},
 {id:'subscriptions',label:'订阅',permission:'finance:read',icon:Package},
 {id:'credit-batches',label:'额度批次',permission:'finance:read',icon:Package},
 {id:'offers',label:'商品与套餐',permission:'finance:read',icon:Package},
 {id:'rates',label:'模型费率',permission:'finance:read',icon:SlidersHorizontal},
 {id:'billing-profiles',label:'计费配置',permission:'finance:read',icon:SlidersHorizontal},
 {id:'audit-events',label:'审计日志',permission:'audit:read',icon:ScrollText},
 {id:'system-status',label:'系统状态',permission:'system:read',icon:HeartPulse},
];
export function AdminConsole({principal,csrf,onReturn,onLogout,busy=false,sandbox=false,onRetry}:{principal:Resource<AdminPrincipal>;csrf?:string;onReturn:()=>void;onLogout:()=>void;busy?:boolean;sandbox?:boolean;onRetry?:()=>void}) {
 const [route,setRoute]=useState(()=>window.location.pathname);
 useEffect(()=>{const handle=()=>setRoute(window.location.pathname);window.addEventListener('popstate',handle);return()=>window.removeEventListener('popstate',handle);},[]);
 const [, ,raw='overview',detail] = route.split('/');const current=nav.find(n=>n.id===raw);const p=principal.data;
 const enabled=p?.enabled;const permitted=current&&can(p,current.permission);
 return <div className="admin-shell"><aside className="admin-sidebar"><a className="admin-brand" href="/admin" onClick={e=>{e.preventDefault();go('/admin/overview');}}><img src="/assets/unihub-logo-v1.png" alt="归一 UniHub"/><span>平台管理</span></a><nav aria-label="平台管理导航">{nav.filter(n=>can(p,n.permission)).map(({id,label,icon:Icon})=><a key={id} href={`/admin/${id}`} aria-current={raw===id?'page':undefined} onClick={e=>{e.preventDefault();go(`/admin/${id}`);}}><Icon size={17}/><span>{label}</span></a>)}</nav><button className="admin-return" onClick={onReturn}><ArrowLeft size={17}/>返回账户工作区</button></aside><div className="admin-workspace"><header className="admin-topbar"><div><span className="admin-scope">平台范围</span><span>{p?.roles.map(r=>roleNames[r]||r).join(' · ')}{p?.legacy_user_admin?' · 旧用户管理资格':''}</span></div><button disabled={busy} onClick={onLogout}><LogOut size={16}/>退出</button></header><main className="admin-content">{sandbox?<Notice>测试环境 · 不涉及真实支付</Notice>:null}{principal.loading?<p role="status">正在核对平台管理权限…</p>:principal.error?<Notice kind="error">{principal.error}<button onClick={onRetry}>重试</button><a href="/auth/login">重新登录</a></Notice>:!enabled?<Notice kind="error">当前用户没有平台管理权限。</Notice>:!current?<Notice kind="error">管理页面不存在。<button onClick={()=>go('/admin/users')}>查看用户</button></Notice>:!permitted?<Notice kind="error">当前平台角色没有此页面的访问权限。</Notice>:raw==='users'||raw==='admins'?<UserManagement principal={p!} csrf={csrf} detail={detail} administrators={raw==='admins'}/>:raw==='identities'?<IdentityDirectory principal={p!} csrf={csrf}/>:<Operations key={raw+(detail||'')} resource={raw} principal={p!} csrf={csrf} detail={detail}/>}</main></div></div>;
}
