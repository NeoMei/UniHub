import { useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { request, idempotencyKey } from '../api';
import { useResource } from '../useResource';
import { Notice, Panel, Table, Modal } from '../ui';
import { value, text } from './types';
import type { ListPage, Row } from './types';
export type Column = [string,string];
export function DataList({endpoint,columns,actions,extra,refresh=0,header,initialFilters,exportResource}: {endpoint:string;columns:Column[];actions?:(row:Row)=>ReactNode;extra?:ReactNode;refresh?:number;header?:ReactNode;initialFilters?:Record<string,string>;exportResource?:string}) {
 const [draft,setDraft]=useState('');const [query,setQuery]=useState('');const [status,setStatus]=useState('');const [account,setAccount]=useState(initialFilters?.account_id||'');const [product,setProduct]=useState(initialFilters?.product_id||'');const [from,setFrom]=useState('');const [to,setTo]=useState('');const [cursor,setCursor]=useState('');const [history,setHistory]=useState<string[]>([]);const [version,setVersion]=useState(0);
 const params = new URLSearchParams({...initialFilters,q:query,status,account_id:account,product_id:product,from,to,limit:'50',cursor});
 const resource=useResource<ListPage>(`/admin/v1/${endpoint}?${params}`,version+refresh);
 function reset() {setCursor('');setHistory([]);}
 function search(event:FormEvent) {event.preventDefault();setQuery(draft);reset();setVersion(v=>v+1);}
 return <><form className="admin-filters" onSubmit={search}>
 <label>搜索<input aria-label="搜索记录" value={draft} onChange={e=>setDraft(e.target.value)} placeholder="名称、邮箱或 ID" maxLength={100}/></label>
 <label>状态<input value={status} onChange={e=>{setStatus(e.target.value);reset();}} placeholder="全部" maxLength={40}/></label>
 <label>账户 ID<input value={account} onChange={e=>{setAccount(e.target.value);reset();}} maxLength={128}/></label>
 <label>产品<input value={product} onChange={e=>{setProduct(e.target.value);reset();}} maxLength={64}/></label>
 <label>开始日期<input type="date" value={from} onChange={e=>{setFrom(e.target.value);reset();}}/></label><label>结束日期<input type="date" value={to} onChange={e=>{setTo(e.target.value);reset();}}/></label>
 <button type="submit">搜索</button><button type="button" onClick={()=>setVersion(v=>v+1)}>刷新</button>{exportResource?<a className="button-link" href={`/admin/v1/exports?${new URLSearchParams({...Object.fromEntries(params),resource:exportResource,cursor:''})}`}>导出筛选结果</a>:null}{extra}
 </form>{header}
 {resource.loading?<p role="status">正在加载…</p>:resource.error?<Notice kind="error">{resource.error}<button onClick={()=>setVersion(v=>v+1)}>重试</button></Notice>:<>
 <div className="admin-list-meta"><span>共 {resource.data?.total||0} 条</span><span>{resource.data?.as_of ? `查询时间 ${new Date(resource.data.as_of).toLocaleString('zh-CN')}` : ''}</span></div>
 <Table headers={[...columns.map(c=>c[1]),...(actions?['操作']:[])]}>
 {resource.data?.items?.length ? resource.data.items.map((row,index)=><tr key={text(row.id||row.user_id||row.product_id||index)}>{columns.map(([key])=><td key={key}>{text(value(row,key))}</td>)}{actions?<td className="admin-row-actions">{actions(row)}</td>:null}</tr>):<tr><td colSpan={columns.length+(actions?1:0)}><p className="admin-empty">暂无符合条件的记录</p></td></tr>}
 </Table><div className="admin-pagination"><button disabled={!history.length} onClick={()=>{setCursor(history[history.length-1]);setHistory(h=>h.slice(0,-1));}}>上一页</button><span>第 {history.length+1} 页</span><button disabled={!resource.data?.next_cursor} onClick={()=>{setHistory(h=>[...h,cursor]);setCursor(resource.data!.next_cursor!);}}>下一页</button></div></>}
 </>;
}
export function Info({row,fields}:{row:Row;fields:Column[]}) {return <dl className="admin-info">{fields.map(([key,label])=><div key={key}><dt>{label}</dt><dd>{text(value(row,key))}</dd></div>)}</dl>;}
export function Mutation({title,path,method='POST',body,csrf,onSaved,children,danger=false}:{title:string;path:string;method?:string;body:Row;csrf?:string;onSaved:()=>void;children?:ReactNode;danger?:boolean}) {
 const [open,setOpen]=useState(false);const [reason,setReason]=useState('');const [error,setError]=useState('');const [busy,setBusy]=useState(false);const [key,setKey]=useState(idempotencyKey);
 async function submit(e:FormEvent) {e.preventDefault();setBusy(true);setError('');try{await request(path,{method,body:{...body,reason},csrf,key});setOpen(false);setReason('');onSaved();}catch(e){setError((e as Error).message);}finally{setBusy(false);}}
 return <><button className={danger?'danger':''} onClick={()=>{setKey(idempotencyKey());setOpen(true);}}>{title}</button>{open?<Modal title={title} onClose={()=>{if(!busy)setOpen(false);}}><form className="admin-form" onSubmit={submit}>{children}<label>操作原因<textarea required minLength={2} maxLength={500} value={reason} onChange={e=>setReason(e.target.value)}/></label>{error?<Notice kind="error">{error}</Notice>:null}<button type="submit" disabled={busy}>{busy?'提交中…':'确认提交'}</button></form></Modal>:null}</>;
}
export function ActionForm({title,path,method='POST',csrf,onSaved,fields,build,initial={},operationId=false}:{title:string;path:string|((values:Record<string,string>)=>string);method?:string;csrf?:string;onSaved:()=>void;fields:{key:string;label:string;type?:string;options?:string[];required?:boolean}[];build?:(values:Record<string,string>)=>Row;initial?:Record<string,string>;operationId?:boolean}) {
 const [open,setOpen]=useState(false);const [values,setValues]=useState(initial);const [error,setError]=useState('');const [busy,setBusy]=useState(false);const [key,setKey]=useState(idempotencyKey);
 async function submit(e:FormEvent) {e.preventDefault();setBusy(true);setError('');try{await request(typeof path==='function'?path(values):path,{method,csrf,key,body:{...(build?build(values):values),...(operationId?{operation_id:key}:{}),reason:values.reason}});setOpen(false);onSaved();}catch(e){setError((e as Error).message);}finally{setBusy(false);}}
 return <><button onClick={()=>{setKey(idempotencyKey());setOpen(true);}}>{title}</button>{open?<Modal title={title} onClose={()=>{if(!busy)setOpen(false);}}><form className="admin-form" onSubmit={submit}>{[...fields,{key:'reason',label:'操作原因',required:true}].map(f=><label key={f.key}>{f.label}{f.options?<select value={values[f.key]||''} required={f.required!==false} onChange={e=>setValues(v=>({...v,[f.key]:e.target.value}))}><option value="">请选择</option>{f.options.map(o=><option key={o} value={o}>{o}</option>)}</select>:<input type={f.type||'text'} maxLength={500} required={f.required!==false} value={values[f.key]||''} onChange={e=>setValues(v=>({...v,[f.key]:e.target.value}))}/>}</label>)}{error?<Notice kind="error">{error}</Notice>:null}<button type="submit" disabled={busy}>{busy?'提交中…':'确认提交'}</button></form></Modal>:null}</>;
}
export function DetailResource({path,children,revision=0}:{path:string;children:(row:Row)=>ReactNode;revision?:number}) {const r=useResource<Row>(path,revision);if(r.loading)return <p role="status">正在加载详情…</p>;if(r.error)return <Notice kind="error">{r.error}</Notice>;return r.data?<>{children(r.data)}</>:<Panel><p>未找到记录。</p></Panel>;}
