import { useState } from 'react';
import { useResource } from '../useResource';
import { Notice, Panel } from '../ui';
import { ActionForm, DataList, DetailResource, Info, Mutation } from './common';
import type { Column } from './common';
import { can, encoded, go, text } from './types';
import type { AdminPrincipal, Row } from './types';
const columns:Record<string,Column[]>={
 accounts:[['name','账户'],['kind','类型'],['id','账户 ID'],['management_recovery_required','待恢复管理'],['created_at','建立时间']],
 'api-keys':[['name','名称'],['user_id','用户'],['account_id','付费账户'],['product_id','产品'],['instance_id','实例'],['status','状态'],['expires_at','到期时间']],
 calls:[['call_id','调用 ID'],['user_id','用户'],['account_id','账户'],['model','模型'],['status','状态'],['asset','资产'],['charged','消费（百万分之一单位）']],
 recharges:[['id','充值单'],['account_id','账户'],['product_id','产品'],['amount','金额（百万分之一元）'],['status','状态'],['created_at','创建时间']],
 transactions:[['id','操作'],['account_id','账户'],['kind','类型'],['status','状态'],['ledger_transaction_id','账本交易'],['created_at','创建时间']],
 orders:[['id','订单'],['account_id','账户'],['product_id','产品'],['kind','类型'],['amount','金额（百万分之一元）'],['payment_status','付款'],['fulfillment_status','履约']],
 refunds:[['id','订单'],['account_id','账户'],['refund_status','退款状态'],['refund_reason','退款原因'],['amount','金额（百万分之一元）']],
 subscriptions:[['id','订阅'],['account_id','账户'],['product_id','产品'],['offer.name','套餐'],['paid_through','有效期至'],['status','状态']],
 'credit-batches':[['id','额度批次'],['account_id','账户'],['product_id','产品'],['available','可用额度（百万分之一）'],['reserved','预占额度（百万分之一）'],['consumed','已用额度（百万分之一）'],['expires_at','到期']],
 products:[['name','产品'],['id','标识'],['enabled','启用'],['configuration_source','配置来源']],
 offers:[['name','商品'],['id','标识'],['product_id','产品'],['kind','类型'],['period','周期'],['price','价格（百万分之一元）'],['monthly_credit','额度（百万分之一）'],['status','状态']],
 rates:[['id','费率版本'],['model','模型'],['asset','资产'],['input.numerator','输入分子'],['input.denominator','输入分母'],['status','状态']],
 'billing-profiles':[['product_id','产品'],['version','版本'],['subscription.enabled','订阅'],['included_credits.enabled','赠送额度'],['addons.enabled','加量包'],['usage.enabled','按量'],['usage.default_funding_mode','默认资金模式']],
 'usage-budgets':[['account_id','账户'],['product_id','产品'],['instance_id','实例'],['asset','资产'],['max_total','总上限（百万分之一）'],['max_per_call','单次上限（百万分之一）'],['version','版本']],
 'audit-events':[['created_at','时间'],['actor_id','操作者'],['target_id','目标'],['account_id','账户'],['action','动作'],['reason','原因'],['source','来源']],
};
const titles:Record<string,string>={accounts:'账户与团队','api-keys':'API Key',calls:'调用监控',recharges:'充值记录',transactions:'账务操作',orders:'全平台订单',refunds:'退款复核',subscriptions:'订阅管理','credit-batches':'模型额度批次',products:'产品目录',offers:'商品与套餐',rates:'模型费率','billing-profiles':'计费配置','usage-budgets':'使用预算','audit-events':'审计日志'};
export function Operations({resource,principal,csrf,detail}:{resource:string;principal:AdminPrincipal;csrf?:string;detail?:string}) {
 const [revision,setRevision]=useState(0);const refresh=()=>setRevision(v=>v+1);
 const query=Object.fromEntries(new URLSearchParams(window.location.search));
 if(resource==='accounts'&&detail)return <AccountDetail id={detail} principal={principal} csrf={csrf}/>;
 if(resource==='overview')return <Overview principal={principal}/>;
 if(resource==='system-status')return <SystemStatus/>;
 if(resource==='wallets')return <Wallets/>;
 const finance=can(principal,'finance:write');
 const fields=columns[resource]||[];
 function actions(row:Row) {
  if(resource==='accounts')return <button className="text-button" onClick={()=>go(`/admin/accounts/${encoded(row.id)}`)}>查看详情</button>;
  if(resource==='api-keys')return can(principal,'keys:revoke')&&row.status==='active'?<Mutation title="撤销 Key" path={`/admin/v1/api-keys/${encoded(row.id)}/revoke`} body={{version:row.version}} csrf={csrf} onSaved={refresh} danger/>:null;
  if(resource==='offers'&&finance)return <div className="admin-row-actions">{row.status==='draft'?<><OfferEditor row={row} csrf={csrf} onSaved={refresh}/><Mutation title="发布" path={`/admin/v1/offers/${encoded(row.id)}/publish`} body={{version:row.version}} csrf={csrf} onSaved={refresh}/></>:null}{row.status==='published'?<Mutation title="停售" path={`/admin/v1/offers/${encoded(row.id)}/retire`} body={{version:row.version}} csrf={csrf} onSaved={refresh}/>:null}</div>;
  if(resource==='rates'&&finance&&row.status==='published')return <Mutation title="退役费率" path={`/admin/v1/rates/${encoded(row.id)}/retire`} body={{version:row.version}} csrf={csrf} onSaved={refresh}/>;
  if(resource==='billing-profiles'&&finance)return <ProfileEditor row={row} csrf={csrf} onSaved={refresh}/>;
  if(resource==='usage-budgets'&&finance)return <BudgetEditor row={row} csrf={csrf} onSaved={refresh}/>;
  if(resource==='refunds'&&finance&&['REVIEW','REVIEW_REQUIRED','PENDING_REVIEW'].includes(String(row.refund_status)))return <Mutation title="拒绝退款申请" path={`/admin/v1/accounts/${encoded(row.account_id)}/orders/${encoded(row.id)}/refund-review`} body={{version:row.version,decision:'reject'}} csrf={csrf} onSaved={refresh}/>;
  return null;
 }
 return <><div className="admin-page-heading"><div><h1>{titles[resource]||'平台记录'}</h1><p>{resource==='audit-events'?'按真实操作者和业务关联追踪操作。':resource==='calls'?'平台逻辑调用与可信消费记录；现金与模型额度分别计量。':'全平台范围 · 按条件检索并查看当前记录。'}</p></div>{resource==='offers'&&finance?<OfferEditor csrf={csrf} onSaved={refresh}/>:null}{resource==='rates'&&finance?<RateEditor csrf={csrf} onSaved={refresh}/>:null}{resource==='usage-budgets'&&finance?<BudgetEditor csrf={csrf} onSaved={refresh}/>:null}</div>
 <DataList key={resource+window.location.search} endpoint={resource} columns={fields} actions={actions} refresh={revision} initialFilters={query} exportResource={['accounts','orders','calls','audit-events'].includes(resource)&&can(principal,'export:read')?resource:undefined}/></>;
}
function AccountDetail({id,principal,csrf}:{id:string;principal:AdminPrincipal;csrf?:string}) {
 const [revision,setRevision]=useState(0);const refresh=()=>setRevision(v=>v+1);
 return <><button onClick={()=>go('/admin/accounts')}>返回账户列表</button><h1>账户详情</h1><DetailResource path={`/admin/v1/teams/${encoded(id)}`} revision={revision}>{row=><><Panel title={String(row.name||id)}><Info row={row} fields={[["id","账户 ID"],["kind","类型"],["version","版本"],["management_state","管理状态"],["member_count","成员数"]]}/>{can(principal,'teams:write')&&row.kind==='team'?<div className="admin-actions"><ActionForm title="修改团队名称" method="PATCH" path={`/admin/v1/teams/${encoded(id)}`} csrf={csrf} onSaved={refresh} fields={[{key:'name',label:'团队名称'}]} initial={{name:String(row.name||'')}} build={v=>({name:v.name,expected_version:row.version})}/><ActionForm title="邀请成员" path={`/admin/v1/teams/${encoded(id)}/invitations`} csrf={csrf} onSaved={refresh} fields={[{key:'recipient_id',label:'用户 ID'},{key:'role',label:'团队角色',options:can(principal,'teams:privileged')?['member','admin','billing_admin','owner']:['member']},{key:'transfer_owner_id',label:'转出的所有者 ID（仅所有权转移填写）',required:false}]} build={v=>({recipient_id:v.recipient_id,role:v.role,transfer_owner_id:v.transfer_owner_id||'',expected_version:row.version})}/></div>:null}</Panel>
 <Panel title="账户成员"><DataList endpoint={`teams/${encoded(id)}/members`} refresh={revision} columns={[["name","名称"],["email","邮箱"],["user_id","用户 ID"],["role","角色"],["active","启用"]]} actions={m=>can(principal,'teams:write')&&(m.role==='member'||can(principal,'teams:privileged'))?<><ActionForm title="调整成员角色" method="PATCH" path={`/admin/v1/teams/${encoded(id)}/members/${encoded(m.user_id)}`} csrf={csrf} onSaved={refresh} fields={[{key:'role',label:'新角色',options:can(principal,'teams:privileged')?['member','admin','billing_admin']:['member']}]} build={v=>({role:v.role,remove:false,expected_version:row.version})}/><Mutation title="移除成员" method="PATCH" path={`/admin/v1/teams/${encoded(id)}/members/${encoded(m.user_id)}`} csrf={csrf} onSaved={refresh} body={{role:m.role,remove:true,expected_version:row.version}} danger/></>:<button className="text-button" onClick={()=>go(`/admin/users/${encoded(m.user_id)}`)}>查看用户</button>}/></Panel>
 <Panel title="平台发出的团队邀请"><DataList endpoint={`teams/${encoded(id)}/invitations`} refresh={revision} columns={[["recipient_id","接收用户"],["role","角色"],["invited_by","邀请人"],["expires_at","有效期"]]} actions={can(principal,'teams:write')?inv=><Mutation title="撤销邀请" path={`/admin/v1/teams/${encoded(id)}/invitations/${encoded(inv.id)}`} method="DELETE" body={{expected_version:row.version}} csrf={csrf} onSaved={refresh}/>:undefined}/></Panel>
 <Panel title="关联业务"><div className="admin-actions">{[['wallets','钱包','finance:read'],['orders','订单','finance:read'],['subscriptions','订阅','finance:read'],['api-keys','API Key','keys:read'],['calls','调用','calls:read'],['transactions','账务操作','finance:read']].filter(([, ,p])=>can(principal,p)).map(([route,label])=><button key={route} onClick={()=>go(`/admin/${route}?account_id=${encoded(id)}`)}>{label}</button>)}</div></Panel></>}</DetailResource></>;
}
const overviewPrimaryKeys=['central_users','enabled_users','disabled_users','orders','teams','products'] as const;
const overviewSecondaryKeys=['materialized_identity_links','refund_reviews','pending_commands','unknown_commands'] as const;
const metricStatusText:Record<string,string>={separate_provider_query_required:'需在身份提供方单独查询',query_snapshot:'查询快照',not_computed_payment_capability_required:'未计算 · 需接入支付能力',not_probed:'未探测',available:'可用',not_recorded:'未记录',recorded:'已记录'};
function metricDisplay(v:unknown):string {if(typeof v==='number')return v.toLocaleString('zh-CN');if(typeof v==='boolean')return v?'是':'否';if(typeof v==='string'&&metricStatusText[v])return metricStatusText[v];return text(v);}
function formatStamp(v:unknown):string {const d=new Date(String(v??''));if(Number.isNaN(d.getTime()))return text(v);const p=(n:number)=>String(n).padStart(2,'0');return `${d.getFullYear()}-${p(d.getMonth()+1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;}
function KpiStrip({ariaLabel,entries,secondary=false}:{ariaLabel:string;entries:{key:string;label:string;value:string}[];secondary?:boolean}) {return <section className={secondary?'admin-kpi-strip admin-kpi-secondary':'admin-kpi-strip'} aria-label={ariaLabel}>{entries.map(m=><div key={m.key} className="admin-kpi"><span className="admin-kpi-label">{m.label}</span><strong className="admin-kpi-value">{m.value}</strong></div>)}</section>;}
export function Overview({principal}:{principal:AdminPrincipal}) {const r=useResource<Row>('/admin/v1/overview');if(r.error)return <Notice kind="error">{r.error}</Notice>;const data=r.data||{};const pick=(keys:readonly string[])=>keys.filter(k=>data[k]!==undefined&&data[k]!==null).map(k=>({key:k,label:metricNames[k]||k,value:metricDisplay(data[k])}));return <><h1>管理概览</h1><p>平台范围 · 真实到账、测试交易与模型额度分别记录。</p>{r.loading?<p role="status">正在加载概览…</p>:<><KpiStrip ariaLabel="平台关键指标" entries={pick(overviewPrimaryKeys)}/><div className="admin-kpi-meta"><span>查询时间 {formatStamp(data.as_of)}</span><span>数据一致性 {metricDisplay(data.consistency)}</span><span>身份目录 {metricDisplay(data.identity_directory_status)}</span></div>{pick(overviewSecondaryKeys).length?<KpiStrip ariaLabel="运营与资金队列" secondary entries={pick(overviewSecondaryKeys)}/>:null}</>}{can(principal,'system:read')?<OverviewTrends/>:null}<Panel title="常用管理"><div className="admin-actions">{[['users','用户管理','users:read'],['accounts','账户与团队','accounts:read'],['orders','订单','finance:read'],['audit-events','审计日志','audit:read'],['system-status','系统状态','system:read']].filter(([, ,p])=>can(principal,p)).map(([p,label])=><button key={p} onClick={()=>go(`/admin/${p}`)}>{label}</button>)}</div></Panel></>;}
const trendSeries=[{key:'new_users',label:'每日新增用户'},{key:'calls',label:'每日调用'},{key:'failed_calls',label:'每日失败调用'}] as const;
type TrendDay={date:string;new_users:number;calls:number;failed_calls:number};
type FailedModelRow={model:string;calls:number;failed_calls:number;failure_rate:number};
type OperationTimeseries={as_of:string;timezone:string;finance_scope:boolean;days:TrendDay[];pending_refunds:number;top_failed_models:FailedModelRow[]};
function trendPct(v:number):string {return (v*100).toFixed(1).replace(/\.0$/,'')+'%';}
function TrendChart({label,days,pick}:{label:string;days:TrendDay[];pick:(d:TrendDay)=>number}) {
 const w=264,h=88,pad=8,n=days.length;
 if(n<2)return null;
 const values=days.map(pick);
 const max=Math.max(1,...values);
 const x=(i:number)=>pad+i*(w-2*pad)/(n-1);
 const y=(v:number)=>h-pad-v/max*(h-2*pad);
 const points=values.map((v,i)=>x(i).toFixed(1)+','+y(v).toFixed(1)).join(' ');
 return <figure className="admin-trend"><figcaption>{label}</figcaption>
 <svg viewBox={'0 0 '+w+' '+h} role="img" aria-label={label+'，最近 '+n+' 天，峰值 '+max.toLocaleString('zh-CN')}>
  {[0.25,0.5,0.75,1].map(g=><line key={g} className="admin-trend-grid" x1={pad} x2={w-pad} y1={pad+g*(h-2*pad)} y2={pad+g*(h-2*pad)}/>)}
  <polyline className="admin-trend-line" points={points}/>
  <circle className="admin-trend-dot" cx={x(n-1).toFixed(1)} cy={y(values[n-1]).toFixed(1)} r="2.5"/>
 </svg>
 <span className="admin-trend-range">{days[0].date} – {days[n-1].date} · 峰值 {max.toLocaleString('zh-CN')}</span>
 </figure>;
}
function OverviewTrends() {
 const [revision,setRevision]=useState(0);
 const r=useResource<OperationTimeseries>('/admin/v1/overview/timeseries',revision);
 return <Panel title="运营趋势" className="admin-trends">
 {r.error?<div className="admin-trend-error"><Notice kind="error">{r.error}</Notice><button onClick={()=>setRevision(v=>v+1)}>重试</button></div>
 :r.loading?<p role="status">正在加载运营趋势…</p>
 :r.data?(d=>{const charts=d.finance_scope?trendSeries:[trendSeries[0]];return <><div className="admin-trend-charts">{charts.map(s=><TrendChart key={s.key} label={s.label} days={d.days} pick={day=>Number(day[s.key]||0)}/>)}</div>
 {d.finance_scope?null:<p className="admin-trend-note">调用与退款趋势需要财务管理权限。</p>}
 <div className="admin-trend-tables">
 <table className="admin-trend-table"><caption>退款队列</caption><thead><tr><th scope="col">队列</th><th scope="col">待处理</th></tr></thead>
 <tbody><tr><td>待处理退款</td><td>{d.pending_refunds.toLocaleString('zh-CN')}</td></tr></tbody></table>
 <table className="admin-trend-table"><caption>失败率最高的模型（最近 7 天）</caption><thead><tr><th scope="col">模型</th><th scope="col">调用数</th><th scope="col">失败率</th></tr></thead>
 <tbody>{d.top_failed_models.length?d.top_failed_models.map(m=><tr key={m.model}><td>{m.model}</td><td>{m.calls.toLocaleString('zh-CN')}</td><td>{trendPct(m.failure_rate)}</td></tr>):<tr><td colSpan={3}>最近 7 天没有失败的模型调用。</td></tr>}</tbody></table>
 </div></>;})(r.data):null}
 </Panel>;
}

const metricNames:Record<string,string>={users:'平台用户',total_users:'平台用户',central_users:'平台用户',active_users:'启用用户',enabled_users:'启用用户',disabled_users:'停用用户',teams:'团队',accounts:'账户',products:'产品',orders:'订单',materialized_identity_links:'已关联身份',registered_identity_total:'注册身份总数',identity_directory_status:'身份目录',pending_refunds:'待复核退款',refund_reviews:'待复核退款',pending_commands:'未决命令',unknown_commands:'待核实命令',calls:'调用',failed_calls:'失败调用',logical_calls:'逻辑调用',settled:'已结算',released:'已释放',processing:'处理中',window_from:'统计开始',window_to:'统计截止',timezone:'时区',failure_rate:'失败率',failure_rate_status:'失败率状态',attempts_status:'尝试数据状态',as_of:'查询时间',consistency:'数据一致性',income_status:'收入统计状态',sandbox:'测试环境',real_payments_configured:'真实支付已接入',status:'状态',reason:'原因',postgresql:'PostgreSQL',ledger:'账本',oidc:'OIDC 身份',configuration_source:'配置来源',platform_authority:'权限来源',secret_configuration_editable:'密钥配置可编辑',oldest_pending_at:'最早未决时间',workers:'后台任务',worker_telemetry_status:'任务遥测'};
function SystemStatus(){const [rev,setRev]=useState(0);const r=useResource<Row>('/admin/v1/system-status',rev);return <><h1>系统状态</h1><button onClick={()=>setRev(v=>v+1)}>刷新状态</button>{r.error?<Notice kind="error">{r.error}</Notice>:r.loading?<p role="status">正在检查…</p>:<div className="admin-status-grid">{Object.entries(r.data||{}).map(([key,v])=><Panel key={key} title={metricNames[key]||key}>{v&&typeof v==='object'?<Info row={v as Row} fields={Object.keys(v).map(k=>[k,metricNames[k]||k])}/>:<p>{text(v)}</p>}</Panel>)}</div>}</>;}
function Wallets(){const [account,setAccount]=useState(new URLSearchParams(window.location.search).get('account_id')||'');const [selected,setSelected]=useState(account);return <><h1>钱包与资金</h1><p>余额由账本实时提供；人民币现金与模型额度分别显示。</p><form className="admin-filters" onSubmit={e=>{e.preventDefault();setSelected(account);}}><label>付费账户 ID<input required value={account} onChange={e=>setAccount(e.target.value)}/></label><button>查询余额</button></form>{selected?<DataList key={selected} endpoint="wallets" initialFilters={{account_id:selected}} columns={[["product_id","产品"],["asset","资产"],["amount","余额（百万分之一单位）"],["available","可用（百万分之一单位）"]]}/>:<Panel><p>选择具体账户查询权威余额。</p></Panel>}</>;}
function OfferEditor({row,csrf,onSaved}:{row?:Row;csrf?:string;onSaved:()=>void}) {return <ActionForm title={row?'编辑草稿':'新增商品草稿'} path={row?`/admin/v1/offers/${encoded(row.id)}`:'/admin/v1/offers'} method={row?'PUT':'POST'} csrf={csrf} onSaved={onSaved} initial={Object.fromEntries(Object.entries(row||{}).filter(([,v])=>typeof v==='string').map(([k,v])=>[k,String(v)]))} fields={[{key:'id',label:'商品标识'},{key:'product_id',label:'产品标识'},{key:'name',label:'商品名称'},{key:'account_kind',label:'账户类型',options:['personal','team','both']},{key:'kind',label:'类型',options:['membership','addon']},{key:'period',label:'周期',options:['month','year'],required:false},{key:'price',label:'价格（百万分之一元）'},{key:'monthly_credit',label:'每月/加量包额度（百万分之一）'},{key:'rank',label:'升级等级',type:'number',required:false},{key:'validity_months',label:'有效月数',type:'number',required:false},{key:'sandbox',label:'是否测试商品',options:['true','false']}]} build={v=>({offer:{id:v.id,product_id:v.product_id,name:v.name,account_kind:v.account_kind,kind:v.kind,period:v.period||'',price:v.price,monthly_credit:v.monthly_credit,rank:Number(v.rank||0),validity_months:Number(v.validity_months||1),entitlements:row?.entitlements||{},upgrade_rule:'remaining_term_prorata_v1',refund_rule:row?.refund_rule||'',status:'draft',sandbox:v.sandbox==='true'},version:row?.version||''})}/>;}
function RateEditor({csrf,onSaved}:{csrf?:string;onSaved:()=>void}) {return <ActionForm title="发布费率版本" path="/admin/v1/rates" csrf={csrf} onSaved={onSaved} fields={[{key:'id',label:'费率标识'},{key:'model',label:'模型'},{key:'asset',label:'资产',options:['CREDIT/6','CNY/6']},...['input','output','cache_read','cache_write','reasoning'].flatMap(k=>[{key:`${k}_n`,label:`${({input:'输入',output:'输出',cache_read:'缓存读取',cache_write:'缓存写入',reasoning:'推理'} as Record<string,string>)[k]}价格分子`},{key:`${k}_d`,label:'对应分母'}])]} build={v=>({version:0,rate:{id:v.id,model:v.model,asset:v.asset,status:'published',semantics:'exclusive_v1',...Object.fromEntries(['input','output','cache_read','cache_write','reasoning'].map(k=>[k,{numerator:v[`${k}_n`],denominator:v[`${k}_d`]}]))}})}/>;}
function ProfileEditor({row,csrf,onSaved}:{row:Row;csrf?:string;onSaved:()=>void}) {return <ActionForm title="更新计费配置" path={`/admin/v1/billing-profiles/${encoded(row.product_id)}`} method="PUT" csrf={csrf} onSaved={onSaved} fields={[{key:'preset',label:'收费模式',options:['hybrid','prepaid','subscription']}]} build={v=>{const credits=v.preset==='hybrid';const cash=v.preset==='prepaid';return {version:row.version,profile:{product_id:row.product_id,version:row.version,subscription:{enabled:!cash},included_credits:{enabled:credits},addons:{enabled:credits},usage:{enabled:credits||cash,require_subscription:credits,funding_modes:credits?['credits']:cash?['prepaid_cash']:[],default_funding_mode:credits?'credits':cash?'prepaid_cash':'',models:null}}};}}/>;}
function BudgetEditor({row,csrf,onSaved}:{row?:Row;csrf?:string;onSaved:()=>void}) {return <ActionForm title={row?'修改预算':'设置预算'} path={v=>`/admin/v1/accounts/${encoded(row?.account_id||v.account_id)}/usage-budget`} method="PUT" csrf={csrf} onSaved={onSaved} fields={[{key:'account_id',label:'账户 ID'},{key:'product_id',label:'产品标识'},{key:'instance_id',label:'实例'},{key:'funding_mode',label:'资金模式',options:['credits','prepaid_cash']},{key:'max_total',label:'总限额（百万分之一单位，空为不限）',required:false},{key:'max_per_call',label:'单次限额（百万分之一单位，空为不限）',required:false}]} initial={Object.fromEntries(Object.entries(row||{}).filter(([,v])=>typeof v==='string').map(([k,v])=>[k,String(v)]))} build={v=>({product_id:v.product_id,instance_id:v.instance_id,funding_mode:v.funding_mode,max_total:v.max_total||'',max_per_call:v.max_per_call||'',version:row?.version||0})}/>;}
