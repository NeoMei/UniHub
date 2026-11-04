import { useMemo, useState } from 'react';
import { CheckCircle2, Copy, ShieldCheck } from 'lucide-react';
import { Notice, Panel } from '../ui';
import { buildOnboardingPlan, publicScopeOptions } from './onboardingPlan';
import type { OnboardingInput } from './onboardingPlan';

function download(name:string,body:string,type='application/json') {
 const url=URL.createObjectURL(new Blob([body],{type}));const anchor=document.createElement('a');
 anchor.href=url;anchor.download=name;document.body.appendChild(anchor);anchor.click();anchor.remove();
 window.setTimeout(()=>URL.revokeObjectURL(url),1000);
}
export function Onboarding({sandbox=false}:{sandbox?:boolean}) {
 const [input,setInput]=useState<OnboardingInput>({product:'',callback:'',origin:'',issuer:'',clientId:'',application:'',instance:'default',profile:'hybrid',scopes:['identity:read','accounts:read','catalog:read'],maturity:'existing',billing:'product',browserSSO:true,sandbox});
 const [generated,setGenerated]=useState<ReturnType<typeof buildOnboardingPlan>>(),[copied,setCopied]=useState(false),[error,setError]=useState('');
 const prepared=useMemo(()=>{try{return {plan:buildOnboardingPlan({...input,sandbox}),error:''};}catch(e){return {plan:undefined,error:(e as Error).message};}},[input,sandbox]);
 function set<K extends keyof OnboardingInput>(key:K,value:OnboardingInput[K]) {setInput(v=>({...v,[key]:value}));setGenerated(undefined);setError('');setCopied(false);}
 const command=prepared.plan?.commands||'填写产品标识、精确回调地址和身份提供方地址后生成准备命令。';
 async function copyCommand(){try{if(!navigator.clipboard)throw new Error('当前浏览器无法复制，请下载准备命令。');await navigator.clipboard.writeText(command);setCopied(true);}catch(e){setCopied(false);setError((e as Error).message);}}
 function generate(){if(!prepared.plan)return;try{download(`${input.product.trim()}-onboarding.json`,JSON.stringify(prepared.plan,null,2)+'\n');setGenerated(prepared.plan);}catch{setError('下载失败，请重试或复制准备命令。');}}
 return <div className="admin-onboarding">
 <div className="admin-page-heading"><div><h1>三方接入工作台</h1><p>整理公开客户端参数、计费配置和服务端执行清单，下载后交由管理员审阅。</p></div><span className="admin-draft-badge">计划模式</span></div>
 <Notice><ShieldCheck size={17}/>不会在浏览器保存客户端密钥。此页只下载公开参数与准备命令，应用和读回需要在服务端完成。</Notice>
 {sandbox?<Notice>测试环境 · 不代表正式支付已接入；脚本无需 sandbox 参数。</Notice>:null}
 <div className="admin-onboarding-grid">
 <Panel title="1. 产品与身份信息"><div className="admin-form">
 <label>产品标识<input value={input.product} maxLength={64} placeholder="例如 notes" onChange={e=>set('product',e.target.value)}/></label>
 <label>回调地址<input value={input.callback} placeholder="https://product.example.com/auth/callback" onChange={e=>set('callback',e.target.value)}/></label>
 <label>允许来源（可选）<input value={input.origin} placeholder="https://product.example.com" onChange={e=>set('origin',e.target.value)}/></label>
 <label>身份提供方地址<input value={input.issuer} placeholder="https://identity.example.com" onChange={e=>set('issuer',e.target.value)}/></label>
 <label>公开客户端 ID（可选）<input value={input.clientId} maxLength={256} placeholder="默认 产品标识-web" onChange={e=>set('clientId',e.target.value)}/></label>
 <label>独立应用名称（可选）<input value={input.application} maxLength={64} placeholder="默认 app-产品标识" onChange={e=>set('application',e.target.value)}/></label>
 <label>实例标识<input value={input.instance} maxLength={64} onChange={e=>set('instance',e.target.value)}/></label>
 <label>启用浏览器单点登录<select value={String(input.browserSSO)} onChange={e=>set('browserSSO',e.target.value==='true')}><option value="true">是 · 授权码 + PKCE</option><option value="false">否</option></select></label>
 </div></Panel>
 <Panel title="2. 公开客户端最小权限"><fieldset className="admin-scope-list"><legend>用户授权后的 API 范围</legend>{publicScopeOptions.map(([scope,copy])=><label key={scope}><input type="checkbox" aria-label={scope} checked={input.scopes.includes(scope)} onChange={e=>set('scopes',e.target.checked?[...input.scopes,scope]:input.scopes.filter(s=>s!==scope))}/><span><strong>{scope}</strong><small>{copy}</small></span></label>)}</fieldset><p className="admin-form-hint">这些是公开产品客户端的 API scopes。OIDC 登录与浏览器单点登录单独配置；公开客户端范围不授予私有服务能力。</p></Panel>
 <Panel title="3. 产品成熟度与计费边界"><div className="admin-form">
 <label>产品用户体系<select value={input.maturity} onChange={e=>set('maturity',e.target.value)}><option value="existing">成熟产品 · 保留原用户、登录与界面</option><option value="new">新产品 · 可使用 UserKit 构建界面</option></select></label>
 <label>模型计价归属<select value={input.billing} onChange={e=>set('billing',e.target.value)}><option value="product">原生产品自行计价 · 私有 runtime 结算</option><option value="managed">UniHub 托管模型 · 平台模型费率</option></select></label>
 <label>计费组合<select value={input.profile} onChange={e=>set('profile',e.target.value)}><option value="hybrid">会员 + 模型额度</option><option value="prepaid">预付钱包按量</option><option value="subscription">纯功能订阅</option></select></label>
 </div><p className="admin-form-hint">原产品 Key、价格与旧额度仍由原产品管理。此处选择只记录接入方向，不迁移用户或资金，也不改变已上线产品的计价模式。</p></Panel>
 <Panel title="4. 私有服务与审阅清单"><p className="admin-form-hint">私有 runtime 服务权限由部署配置按产品、实例和操作单独授予。服务 token、identity:import 与 cash:payment 不属于公开客户端 scopes，不能在此页申请或下载。</p>
 <ol className="admin-onboarding-checklist"><li><CheckCircle2 size={16}/>保留原产品认证、用户映射、Key、价格与历史资产</li><li><CheckCircle2 size={16}/>在服务端确认 runtime 能力与供应商操作验收状态</li><li><CheckCircle2 size={16}/>离线 prepare 后审阅客户端计划与完整计费配置</li><li><CheckCircle2 size={16}/>单独执行 apply、readback，再部署或重载公开注册表与计费文件</li></ol>
 <Notice kind={prepared.plan?'info':'warning'}>{prepared.plan?'可下载准备包；尚未创建应用、发布配置或启用真实收款。':prepared.error}</Notice></Panel>
 </div>
 <Panel title="接入准备命令"><div className="admin-command"><code>{command}</code><button className="secondary" disabled={!prepared.plan} onClick={copyCommand}><Copy size={15}/>{copied?'已复制':'复制命令'}</button></div>
 <p className="admin-form-hint">下载包是准备参数清单；客户端脚本在目标服务器读取现有注册表并生成包含校验摘要的正式计划。命令中的 apply/readback 默认注释，审阅后再单独执行；计费输出须与已有产品配置合并，不能直接覆盖多产品文件。</p>
 <div className="admin-onboarding-actions"><button disabled={!prepared.plan} onClick={generate}>生成接入计划</button><span>浏览器仅下载文件；服务端配置、部署和真实收款均需独立验收。</span></div>
 {generated?<div className="admin-actions"><button onClick={()=>download(`${input.product.trim()}-prepare.sh`,generated.commands,'text/x-shellscript')}>下载准备命令</button><button onClick={()=>download(`${input.product.trim()}-billing.json`,JSON.stringify(generated.billingConfiguration,null,2)+'\n')}>下载计费配置</button><button onClick={()=>download(`${input.product.trim()}-client-input.json`,JSON.stringify(generated.publicClient,null,2)+'\n')}>下载公开参数</button></div>:null}
 {error?<Notice kind="error">{error}</Notice>:null}</Panel>
 </div>;
}
