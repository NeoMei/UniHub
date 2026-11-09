import { useState } from 'react';
import { useResource } from '../useResource';
import { Notice, Panel } from '../ui';
import { can, encoded, go, roleNames, text } from './types';
import type { AdminPrincipal, Row } from './types';
import { ActionForm, DataList, DetailResource, Info, Mutation } from './common';
export function Users({principal,csrf,detail,administrators=false}:{principal:AdminPrincipal;csrf?:string;detail?:string;administrators?:boolean}) {
 const [revision,setRevision]=useState(0);const refresh=()=>setRevision(v=>v+1);
 if(detail)return <UserDetail key={detail} id={detail} principal={principal} csrf={csrf}/>;
 return <><div className="admin-page-heading"><div><h1>{administrators?'平台管理员':'用户管理'}</h1><p>{administrators?'管理资格独立于团队与产品角色。':'查看全平台用户，管理状态、身份和产品权限。'}</p></div>{can(principal,'identity:manage')?<ActionForm primary title="创建用户" operationId path="/admin/v1/identities" csrf={csrf} onSaved={refresh} fields={[{key:'username',label:'用户名'},{key:'display_name',label:'显示名称'},{key:'email',label:'激活邮箱',type:'email'}]}/>:null}</div>
 <DataList key={administrators?'admins':'users'} endpoint={administrators?'admins':'users'} refresh={revision} columns={[["name","名称"],["email","邮箱"],["id","用户 ID"],["active","启用"],["created_at","建立时间"],["last_authenticated_at","最近登录"]]} actions={r=><button className="text-button" onClick={()=>go(`/admin/users/${encoded(r.id)}`)}>查看详情</button>}/></>;
}
function UserDetail({id,principal,csrf}:{id:string;principal:AdminPrincipal;csrf?:string}) {
 const [revision,setRevision]=useState(0);const [message,setMessage]=useState('');const refresh=()=>{setRevision(v=>v+1);setMessage('已保存，状态已更新。');};
 return <><button onClick={()=>go('/admin/users')}>返回用户列表</button><h1>用户详情</h1>{message?<Notice>{message}</Notice>:null}<DetailResource path={`/admin/v1/users/${encoded(id)}`} revision={revision}>{detail=>{
 const user=(detail.user||detail) as Row;const target=(detail.principal||{}) as Partial<AdminPrincipal>;const protectedTarget=!!target.roles?.length||!!target.legacy_user_admin;const superAdmin=principal.roles.includes('super_admin');const mayWrite=!protectedTarget||superAdmin;const self=id===principal.user_id;
 return <><Panel title={text(user.name)}><Info row={user} fields={[["id","用户 ID"],["email","邮箱"],["active","启用"],["created_at","中央建立时间"],["last_authenticated_at","最近认证时间"],["version","状态版本"]]}/><div className="admin-actions">
 {mayWrite&&can(principal,'users:status')&&!self?<Mutation title={user.active?'停用用户':'启用用户'} path={`/admin/v1/users/${encoded(id)}/status`} method="PUT" body={{active:!user.active,version:user.version}} csrf={csrf} onSaved={refresh} danger={!!user.active}><p>停用会阻止新请求并撤销该用户的 UniHub 会话与长期 Key，重新启用后须重新登录。</p></Mutation>:null}
 {mayWrite&&can(principal,'identity:manage')?<ActionForm title="修改显示名称" operationId path={`/admin/v1/users/${encoded(id)}/display-name`} method="PATCH" csrf={csrf} onSaved={refresh} initial={{display_name:String(user.name||'')}} fields={[{key:'display_name',label:'显示名称'}]}/>:null}
 {mayWrite&&can(principal,'identity:manage')?<ActionForm title="发起密码恢复" operationId path={`/admin/v1/users/${encoded(id)}/recovery`} csrf={csrf} onSaved={refresh} fields={[]}/>:null}
 {mayWrite&&can(principal,'users:sessions')?<Mutation title="撤销中央会话" path={`/admin/v1/users/${encoded(id)}/sessions/revoke-local`} body={{version:user.version}} csrf={csrf} onSaved={refresh}><p>撤销 UniHub 中央会话。产品及上游 SSO 的完整退出状态另行核实。</p></Mutation>:null}
 </div></Panel>
 <Panel title="身份来源">{((detail.identities||[]) as Row[]).map(i=><Info key={String(i.issuer)+String(i.subject)} row={i} fields={[["issuer","身份提供方"],["subject","不可变身份标识"]]}/>)}</Panel>
 <Panel title="关联账户与团队">{((detail.accounts||[]) as Row[]).map(a=><div className="admin-related" key={String(a.id)}><span>{text(a.name)} · {text(a.kind)} · {text(a.role)}</span><button className="text-button" onClick={()=>go(`/admin/accounts/${encoded(a.id)}`)}>查看账户</button></div>)}</Panel>
 {superAdmin?<AdminRoleForm user={id} current={target} csrf={csrf} onSaved={refresh} self={self}/>:null}
 {can(principal,'access:write')?<ProductAccess user={id} principal={principal} csrf={csrf} revision={revision} onSaved={refresh}/>:null}
 {can(principal,'users:sessions')?<Panel title="中央登录会话"><DataList endpoint={`users/${encoded(id)}/sessions`} refresh={revision} columns={[["created_at","创建时间"],["expires_at","过期时间"]]} actions={mayWrite?r=><Mutation title="撤销会话" path={`/admin/v1/users/${encoded(id)}/sessions/${encoded(r.id)}`} method="DELETE" body={{version:user.version}} csrf={csrf} onSaved={refresh}/>:undefined}/></Panel>:null}
 <Panel title="关联记录"><div className="admin-actions">{[['api-keys','API Key','keys:read'],['calls','调用记录','calls:read'],['orders','订单','finance:read'],['audit-events','审计记录','audit:read']].filter(([, ,p])=>can(principal,p)).map(([route,label])=><button key={route} onClick={()=>go(`/admin/${route}?user_id=${encoded(id)}`)}>{label}</button>)}</div></Panel>
 </>;
 }}</DetailResource></>;
}
function AdminRoleForm({user,current,csrf,onSaved,self}:{user:string;current:Partial<AdminPrincipal>;csrf?:string;onSaved:()=>void;self:boolean}) {
 const [roles,setRoles]=useState<string[]>(current.roles||[]);
 return <Panel title="平台管理资格"><p>当前资格：{current.roles?.map(r=>roleNames[r]||r).join('、')||'无'}{current.legacy_user_admin?'（含旧用户管理兼容资格）':''}</p><div className="admin-checks">{Object.entries(roleNames).map(([id,name])=><label key={id}><input type="checkbox" checked={roles.includes(id)} disabled={self} onChange={e=>setRoles(v=>e.target.checked?[...v,id]:v.filter(r=>r!==id))}/>{name}</label>)}</div>{self?<p>当前账号的管理资格须由另一位超管调整。</p>:<Mutation title="保存平台角色" path={`/admin/v1/admins/${encoded(user)}/roles`} method="PUT" body={{roles,version:current.version||0}} csrf={csrf} onSaved={onSaved}/>}</Panel>;
}
function accessRows(data:Row[]|{items:Row[]}|undefined):Row[] {
 return Array.isArray(data)?data:data?.items||[];
}
function ProductAccess({user,principal,csrf,revision,onSaved}:{user:string;principal:AdminPrincipal;csrf?:string;revision:number;onSaved:()=>void}) {
 const manifests=useResource<Row[]|{items:Row[]}>('/admin/v1/access-products');const assignments=useResource<Row[]|{items:Row[]}>(`/admin/v1/users/${encoded(user)}/product-access`,revision);
 if(manifests.error||assignments.error)return <Notice kind="error">{manifests.error||assignments.error}</Notice>;
 const products=accessRows(manifests.data);const grants=accessRows(assignments.data);const loading=manifests.loading||assignments.loading;
 return <Panel title="产品访问与角色" className="admin-access-panel"><p className="admin-access-intro">为这位用户设置每个接入产品的访问权限和角色。修改后逐项保存，每次保存都会记录操作原因。</p>{loading?<p role="status">正在加载产品权限…</p>:<div className="admin-access-grid">{products.map(m=>{const a=grants.find(a=>a.product_id===m.product_id&&a.instance_id===m.instance_id);return a?<ProductRole key={`${String(m.product_id)}/${String(m.instance_id)}/${revision}`} user={user} manifest={m} assignment={a} principal={principal} csrf={csrf} onSaved={onSaved}/>:null;})}{!products.length?<p>尚未配置接入产品。</p>:null}</div>}</Panel>;
}
function ProductRole({user,manifest,assignment,principal,csrf,onSaved}:{user:string;manifest:Row;assignment:Row;principal:AdminPrincipal;csrf?:string;onSaved:()=>void}) {
 const [selected,setSelected]=useState((assignment.roles||[]) as string[]);const [active,setActive]=useState(!!assignment.active);
 const roles=((manifest.roles||[]) as Row[]);const canEditPrivileged=can(principal,'access:privileged');
 return <article className={`admin-access-card${active?'':' is-inactive'}`}>
  <header className="admin-access-card-head"><div className="admin-access-identity"><h3>{text(manifest.name)}</h3><span className="admin-access-instance">实例 {text(manifest.instance_id)}</span></div><span className={`admin-access-state${active?' is-on':''}`}>{active?'允许访问':'已停用'}</span></header>
  <div className="admin-access-meta"><span className={assignment.explicit?'admin-tag is-explicit':'admin-tag'}>{assignment.explicit?'显式授权':'默认角色'}</span><span>授权版本 {text(assignment.assignment_version)}</span></div>
  <div className="admin-access-divider"/>
  <div className="admin-access-row"><div className="admin-access-copy"><strong>访问状态</strong><span>停用后用户无法进入此产品，已分配的角色会保留。</span></div><label className="admin-switch"><input type="checkbox" checked={active} onChange={e=>setActive(e.target.checked)} aria-label={`允许访问 ${text(manifest.name)}`}/><span className="admin-switch-track"/><span className="admin-switch-label">{active?'允许访问':'禁止访问'}</span></label></div>
  <fieldset className="admin-role-fieldset"><legend>产品角色 <span>已选 {selected.length} / {roles.length}</span></legend><div className="admin-role-grid">{roles.map(r=>{const id=String(r.id);const checked=selected.includes(id);const privileged=!!r.privileged;const locked=privileged&&!canEditPrivileged;return <label key={id} className={`admin-role-option${checked?' is-selected':''}${locked?' is-locked':''}`}><input type="checkbox" checked={checked} disabled={locked} onChange={e=>setSelected(v=>e.target.checked?[...v,id]:v.filter(roleId=>roleId!==id))}/><span className="admin-role-check" aria-hidden="true"/><span className="admin-role-name">{text(r.name)}{privileged?<em>特权</em>:null}{locked?<small>需要特权管理权限</small>:null}</span></label>;})}</div></fieldset>
  <footer className="admin-access-actions"><span>保存后按当前授权版本校验</span><Mutation title="保存产品权限" path={`/admin/v1/users/${encoded(user)}/product-access/${encoded(manifest.product_id)}?instance=${encoded(manifest.instance_id)}`} method="PUT" body={{roles:selected,active,version:assignment.assignment_version}} csrf={csrf} onSaved={onSaved}/></footer>
 </article>;
}
export function IdentityDirectory({principal,csrf}:{principal:AdminPrincipal;csrf?:string}) {const [revision,setRevision]=useState(0);return <><h1>注册身份目录</h1><p>身份提供方注册记录与平台用户分别列示，查询不会自动合并或建立账户。</p><DataList endpoint="identities" refresh={revision} columns={[["username","用户名"],["display_name","显示名称"],["email","邮箱"],["subject","身份标识"],["user_id","平台用户"],["disabled","停用"]]} actions={can(principal,'identity:manage')?r=>!r.user_id?<Mutation title="建立平台记录" path="/admin/v1/identities/materialize" body={{issuer:r.issuer,subject:r.subject}} csrf={csrf} onSaved={()=>setRevision(v=>v+1)}/>:<button className="text-button" onClick={()=>go(`/admin/users/${encoded(r.user_id)}`)}>查看用户</button>:undefined}/></>;}
