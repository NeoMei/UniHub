export const publicScopeOptions = [
 ['identity:read','识别统一用户'],['accounts:read','读取自己的账户'],['catalog:read','读取产品目录'],
 ['subscriptions:read','读取订阅'],['credits:read','读取模型额度'],['wallets:read','读取钱包'],
 ['orders:read','读取订单'],['usage:read','读取授权范围内的消费'],['model-credentials:write','管理托管模型凭据（按需）'],
] as const;
export type OnboardingInput={product:string;callback:string;origin:string;issuer:string;clientId:string;application:string;instance:string;profile:string;scopes:string[];maturity:string;billing:string;browserSSO:boolean;sandbox:boolean};
const slug=/^[a-z][a-z0-9_-]{0,63}$/;
function validURL(raw:string,origin=false) {
 try {
  if(/[\s*\\]/.test(raw)||/%(?![0-9a-fA-F]{2})/.test(raw)||raw.includes('#'))return false;
  const url=new URL(raw);
  if(url.username||url.password||!url.hostname)return false;
  const loopback=url.hostname==='localhost'||/^127\./.test(url.hostname)||url.hostname==='[::1]';
  if(url.protocol!=='https:'&&!(url.protocol==='http:'&&loopback))return false;
  if(/[\s*\\]/.test(decodeURIComponent(url.pathname)+decodeURIComponent(url.search.slice(1).replace(/\+/g,' '))))return false;
  return !origin||/^https?:\/\/[^/?#]+$/.test(raw);
 }catch{return false;}
}
function quote(raw:string) {return "'"+raw.replace(/'/g,"'\"'\"'")+"'";}
const args=(pairs:[string,string][])=>pairs.map(([flag,value])=>`${flag} ${quote(value)}`).join(' ');
export function buildOnboardingPlan(input:OnboardingInput) {
 const product=input.product.trim(),callback=input.callback.trim(),origin=input.origin.trim(),issuer=input.issuer.trim();
 const clientId=input.clientId.trim()||`${product}-web`,application=input.application.trim()||`app-${product}`,instance=input.instance.trim()||'default';
 if(!slug.test(product)||!slug.test(instance))throw new Error('产品标识与实例须为小写字母开头的 1–64 位字母、数字、下划线或连字符。');
 if(!/^[A-Za-z][A-Za-z0-9_-]{1,63}$/.test(application)||['app-built-in','app-unihub'].includes(application))throw new Error('请使用独立的应用名称，长度为 2–64 位。');
 if(!clientId||clientId.length>256||/[\x00-\x1f\x7f]/.test(clientId)||(input.browserSSO&&clientId.includes('-org-')))throw new Error('公开客户端 ID 不符合身份路由要求。');
 if(!validURL(callback)||!validURL(issuer,true)||(origin&&!validURL(origin,true)))throw new Error('回调须为精确 HTTPS URL（本机可用 HTTP）；身份提供方与允许来源只能填写 origin，不含路径。');
 if(!input.scopes.length||new Set(input.scopes).size!==input.scopes.length||input.scopes.some(s=>!publicScopeOptions.some(([scope])=>s===scope)))throw new Error('至少选择一个受支持的公开客户端权限。');
 if(!['hybrid','prepaid','subscription'].includes(input.profile))throw new Error('请选择受支持的计费组合。');
 const commercial=input.scopes.some(scope=>scope!=='identity:read'&&scope!=='accounts:read');
 const publicClient={product_id:product,client_id:clientId,application,organization:'unihub',instance_id:instance,redirect_uris:[callback],origins:origin?[origin]:[],scopes:input.scopes,billing_products:commercial?[product]:[],password_login:false,registration:false,browser_sso:input.browserSSO,refresh_tokens:false};
 const billingConfiguration=[{product_id:product,version:1,preset:input.profile}];
 const clientPath=`./${product}-client-plan.json`,billingPath=`./${product}-billing.json`;
 const prepare=args([['--registry','./product-clients.json'],['--output',clientPath],['--product-id',product],['--client-id',clientId],['--application',application],['--instance-id',instance],['--redirect-uri',callback],...(origin?[['--origin',origin] as [string,string]]:[]),...input.scopes.map(scope=>['--scope',scope] as [string,string])]);
 const command=(action:string)=>`python3 scripts/product_client.py ${action} ${args([['--plan',clientPath],['--issuer',issuer]])}`;
 const commands=[
  '#!/bin/sh','set -eu',
  '# Run from the UniHub repository. Offline preparation only; no registry or remote changes.',
  '# The registry path must point to the actual deployed public client registry; review before preparation.',
  `python3 scripts/product_client.py prepare ${prepare}${input.browserSSO?' --browser-sso':''}`,
  `python3 scripts/product_billing.py prepare ${args([['--preset',input.profile],['--product-id',product],['--version','1'],['--output',billingPath]])}`,
  `python3 scripts/product_billing.py validate ${args([['--input',billingPath]])}`,
  '# Review the generated client plan and complete billing file. Merge existing billing profiles before deployment.',
  '# Apply/readback are separate manual steps. Configure private HEADLESS_MANAGEMENT_CLIENT_ID and HEADLESS_MANAGEMENT_CLIENT_SECRET on the server.',
  '# '+command('apply'),'# '+command('readback'),
  '# Deploy/reload the reviewed client registry and PRODUCT_BILLING_FILE separately.',
  '# Existing product users, API keys, prices and legacy assets remain product-owned. Runtime service capabilities are a separate private deployment.',
  ...(input.sandbox?['# Test-environment preparation; does not configure or enable real payments.']:[]),
 ].join('\n')+'\n';
 return {kind:'unihub-onboarding-preparation',version:1,product_maturity:input.maturity,pricing_owner:input.billing,publicClient,billingConfiguration,commands};
}
