import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {UniHubClient, UniHubError} from '../src/index.ts';

async function fixture(t, handle) {
 const calls=[]; const server=createServer(async(req,res)=>{let body='';for await(const c of req)body+=c;const record={path:req.url,headers:req.headers,body:body?JSON.parse(body):undefined};calls.push(record);handle(record,res);});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));t.after(()=>server.close());
 return {client:new UniHubClient({baseUrl:`http://127.0.0.1:${server.address().port}`,clientId:'own-client',redirectUri:'https://product.test/callback'}),calls};
}
function send(res,status,data){res.writeHead(status,{'content-type':'application/json'});res.end(JSON.stringify(data));}

test('password JSON login binds state, S256 verifier, client and exact redirect',async t=>{
 let authorization;
 const {client,calls}=await fixture(t,(r,res)=>{
  if(r.path==='/v1/identity/authorize/password'){authorization=r.body;assert.equal(authorization.client_id,'own-client');assert.equal(authorization.scope,'openid profile email');assert.equal(authorization.code_challenge_method,'S256');assert.equal(authorization.redirect_uri,'https://product.test/callback');send(res,200,{authorization_id:'a'.repeat(43),code:'a'.repeat(64),state:r.body.state,expires_in:300});}
  else if(r.path==='/v1/identity/token'){assert.equal(r.body.client_id,'own-client');assert.equal(r.body.redirect_uri,authorization.redirect_uri);assert.equal(r.body.grant_type,'authorization_code');send(res,200,{access_token:'access',id_token:'id',token_type:'Bearer',scope:'openid profile email',expires_in:300});}
  else {assert.equal(r.headers.authorization,'Bearer access');send(res,200,{id:'user'});}
 });
 const tokens=await client.passwordLogin({username:'alice',password:'private'});assert.equal(tokens.access_token,'access');assert.equal(calls.length,2);
 const verifier=calls[1].body.code_verifier;assert.match(verifier,/^[A-Za-z0-9_-]{43}$/);
 const digest=new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(verifier)));assert.equal(Buffer.from(digest).toString('base64url'),authorization.code_challenge);assert.ok(authorization.state.length>=32);assert.notEqual(authorization.state,authorization.nonce);
 assert.equal((await client.me()).id,'user');client.clearAccessToken();assert.equal(client.hasAccessToken(),false);
});

test('state mismatch never exchanges code or stores token',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>send(res,200,{authorization_id:'a'.repeat(43),code:'a'.repeat(64),state:'wrong',expires_in:300}));
 await assert.rejects(client.passwordLogin({username:'alice',password:'private'}),e=>e instanceof UniHubError&&e.code==='invalid_authorization_response');assert.equal(calls.length,1);assert.equal(client.hasAccessToken(),false);
});

test('integer quantities and idempotency keys survive without conversion or automatic retry',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>send(res,409,{error:'operation_pending'}));client.setAccessToken('access');
 await assert.rejects(client.createRecharge('account','superwagie','9007199254740993','caller-key'),e=>e instanceof UniHubError&&e.status===409&&e.code==='operation_pending'&&e.body.error==='operation_pending');
 assert.equal(calls.length,1);assert.equal(calls[0].body.amount,'9007199254740993');assert.equal(calls[0].headers['idempotency-key'],'caller-key');
 await assert.rejects(client.createRecharge('account','superwagie',12,'other'),TypeError);assert.equal(calls.length,1);
 await assert.rejects(client.transfer('account','superwagie','10',''),TypeError);assert.equal(calls.length,1);
});

test('registration fixes client and preserves UNKNOWN as an explicit result without retries',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>send(res,202,{registration_id:'r',status:'UNKNOWN',retry_after:2}));
 const result=await client.register({username:'alice',password:'private',client_id:'injected'},'register-key');assert.equal(result.status,'UNKNOWN');assert.equal(calls.length,1);assert.equal(calls[0].body.client_id,'own-client');assert.equal(calls[0].headers['idempotency-key'],'register-key');assert.equal(calls[0].headers.authorization,undefined);
});

test('generic requests cannot send a token to another origin and errors preserve status',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>send(res,503,{error:'payment_not_configured'}));client.setAccessToken('access');
 await assert.rejects(client.request('GET','https://evil.test/v1/me'),TypeError);assert.equal(calls.length,0);
 await assert.rejects(client.request('GET','/v1/me'),e=>e instanceof UniHubError&&e.status===503&&e.code==='payment_not_configured');assert.equal(calls.length,1);
});

test('malformed token response is rejected without storing credentials',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>{
  if(r.path==='/v1/identity/authorize/password')send(res,200,{authorization_id:'a'.repeat(43),code:'a'.repeat(64),state:r.body.state,expires_in:300});
  else send(res,200,{access_token:'access',id_token:12,token_type:'Bearer',scope:'openid profile email',expires_in:300});
 });
 await assert.rejects(client.passwordLogin({username:'alice',password:'private'}),e=>e instanceof UniHubError&&e.code==='invalid_token_response');assert.equal(calls.length,2);assert.equal(client.hasAccessToken(),false);
});

test('normalized generic path cannot escape v1 boundary',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>send(res,200,{}));client.setAccessToken('access');
 await assert.rejects(client.request('GET','/v1/../auth/session'),TypeError);assert.equal(calls.length,0);
});

test('credential transport requires HTTPS except explicit loopback',()=>{
 const options={clientId:'own-client',redirectUri:'https://app.test/callback'};
 assert.throws(()=>new UniHubClient({...options,baseUrl:'http://public.example'}),TypeError);
 for(const baseUrl of ['https://hub.example','http://localhost:8189','http://127.0.0.1:8189','http://[::1]:8189'])assert.doesNotThrow(()=>new UniHubClient({...options,baseUrl}));
});

test('billing discovery carries profiles and keeps reads on product routes',async t=>{
 const profile={product_id:'my-product',version:2,subscription:{enabled:true},included_credits:{enabled:false},addons:{enabled:false},usage:{enabled:true,require_subscription:true,funding_modes:['prepaid_cash'],default_funding_mode:'prepaid_cash'}};
 const {client,calls}=await fixture(t,(r,res)=>{
  assert.equal(r.headers.authorization,'Bearer product-access');
  if(r.path==='/v1/billing-presets')send(res,200,{hybrid:{...profile,product_id:'template',version:0},prepaid:{...profile,product_id:'template',version:0},subscription:{...profile,product_id:'template',version:0}});
  else if(r.path==='/v1/products/my-product/billing-profile')send(res,200,profile);
  else if(r.path==='/v1/products')send(res,200,[{id:'my-product',name:'Product',billing_profile:profile}]);
  else send(res,403,{error:'forbidden'});
 });
 client.setAccessToken('product-access');
 assert.equal((await client.billingPresets()).prepaid.product_id,'template');
 assert.equal((await client.billingProfile('my-product')).usage.default_funding_mode,'prepaid_cash');
 assert.equal((await client.products())[0].billing_profile?.version,2);
 await assert.rejects(client.billingProfile('other-product'),e=>e instanceof UniHubError&&e.status===403&&e.code==='forbidden');
 await assert.rejects(client.request('PUT','/ops/v1/products/my-product/billing-profile',{body:profile}),TypeError);
 assert.equal(calls.length,4);
});

test('model credential preserves three-argument call and sends explicit funding mode',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>{
  assert.equal(r.path,'/v1/accounts/account/model-credentials');
  send(res,201,{id:'credential',audience:'llmrouter',actor_id:'actor',account_id:'account',origin_product_id:'my-product',models:['model'],task_id:r.body.task_id,expires_at:'2026-10-25T01:00:00Z',token:'opaque',funding_mode:r.body.funding_mode??'credits',asset:r.body.funding_mode==='prepaid_cash'?'CNY/6':'CREDIT/6',billing_profile_version:3});
 });
 assert.equal((await client.issueModelCredential('account',['model'],'legacy')).funding_mode,'credits');
 assert.equal(Object.hasOwn(calls[0].body,'funding_mode'),false);
 const cash=await client.issueModelCredential('account',['model'],'cash','prepaid_cash');
 assert.equal(cash.asset,'CNY/6');assert.equal(cash.billing_profile_version,3);
 assert.deepEqual(calls[1].body,{models:['model'],task_id:'cash',funding_mode:'prepaid_cash'});
});

test('usage and call queries preserve asset selection and separate large per-asset charges',async t=>{
 const common={scope:{account_id:'account',origin_product_id:'my-product',visibility:'account'},filter:{},financial_details:true,as_of:'2026-10-25T00:00:00Z',accuracy:'exact_for_query_snapshot',consistency:'live'};
 const usage={input:'9007199254740993',output:'0',cache_read:'0',cache_write:'0',reasoning:'0'};
 const {client,calls}=await fixture(t,(r,res)=>{
  const url=new URL(r.path,'http://fixture.test');
  if(url.pathname.endsWith('/calls')){assert.equal(url.searchParams.get('asset'),'CNY/6');send(res,200,{...common,filter:{asset:'CNY/6',model:'model'},items:[{scope:common.scope,financial_details:true,asset:'CNY/6',call_id:'call',account_id:'account',actor_id:'actor',origin_product_id:'my-product',model:'model',status:'SETTLED',created_at:'2026-10-25T00:00:00Z',time_source:'request',charged:'9007199254740993',usage}],has_more:false,total:'1',unknown_time:'0'});}
  else {const asset=url.searchParams.get('asset');if(asset!==null)assert.equal(asset,'CNY/6');send(res,200,{...common,filter:asset?{asset}:{},asset:asset??'mixed',...(asset?{charged:'17'}:{}),calls_total:'2',settled_calls:'2',pending_calls:'0',missing_usage_calls:'0',unknown_calls:'0',rejected_calls:'0',released_calls:'0',by_status:{SETTLED:'2'},charges_by_asset:asset?{'CNY/6':'17'}:{'CREDIT/6':'9007199254740993','CNY/6':'17'},usage,usage_complete:true,unknown_time:'0'});}
 });
 const page=await client.calls('account',{asset:'CNY/6',model:'model'});assert.equal(page.items[0].asset,'CNY/6');assert.equal(page.items[0].charged,'9007199254740993');
 const single=await client.usage('account',{asset:'CNY/6'});assert.equal(single.asset,'CNY/6');assert.equal(single.charged,'17');assert.deepEqual(single.charges_by_asset,{'CNY/6':'17'});
 const summary=await client.usage('account');assert.equal(summary.asset,'mixed');assert.equal(summary.charged,undefined);assert.deepEqual(summary.charges_by_asset,{'CREDIT/6':'9007199254740993','CNY/6':'17'});assert.equal(summary.usage.input,'9007199254740993');
 assert.equal(calls.length,3);
});

test('unknown product route remains a structured error and is not retried',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>send(res,404,{error:'not_found'}));
 await assert.rejects(client.request('GET','/v1/unknown-billing-route'),e=>e instanceof UniHubError&&e.status===404&&e.code==='not_found');assert.equal(calls.length,1);
});

test('explicit model pool methods preserve quantities, filters, encoded IDs and structured errors',async t=>{
 const {client,calls}=await fixture(t,(r,res)=>{
  if(r.path.includes('denied'))return send(res,403,{error:'forbidden'});
  send(res,200,{total:'9007199254740993',charged:'9007199254740993',asset:'CNY/6'});
 });client.setAccessToken('pool-token');
 assert.equal((await client.modelPoolCalls('account',{origin_product_id:'agentwiki',asset:'CNY/6',limit:1,cursor:'signed+cursor'})).total,'9007199254740993');
 assert.equal((await client.modelPoolCall('account','call/id','agentwiki')).charged,'9007199254740993');
 assert.equal((await client.modelPoolUsage('account',{asset:'CNY/6'})).charged,'9007199254740993');
 assert.equal(calls[0].path,'/v1/accounts/account/model-pool/calls?origin_product_id=agentwiki&asset=CNY%2F6&limit=1&cursor=signed%2Bcursor');
 assert.equal(calls[1].path,'/v1/accounts/account/model-pool/calls/call%2Fid?origin_product_id=agentwiki');
 assert.equal(calls[2].path,'/v1/accounts/account/model-pool/usage/summary?asset=CNY%2F6');
 assert.ok(calls.every(r=>r.headers.authorization==='Bearer pool-token'));
 await assert.rejects(client.modelPoolCalls('denied'),e=>e instanceof UniHubError&&e.status===403&&e.code==='forbidden');
});

test('OpenAPI publishes all named model pool methods with exact restricted scope',async()=>{
 const {readFile}=await import('node:fs/promises');
 const spec=await readFile(new URL('../../../api/openapi.yaml',import.meta.url),'utf8');
 for(const [path,method] of [['calls','modelPoolCalls'],['calls/{call}','modelPoolCall'],['usage/summary','modelPoolUsage']]){
  const block=spec.split(`  "/v1/accounts/{account}/model-pool/${path}":`)[1]?.split('\n  "/')[0];
  assert.ok(block);assert.ok(block.includes(`"operationId": "${method}"`));assert.ok(block.includes('"usage:all-products"'));assert.ok(block.includes('billing_admin'));
 }
});

test('product access and user administration preserve instance and assignment versions',async t=>{
 const {client,calls}=await fixture(t,(_r,res)=>send(res,200,{}));client.setAccessToken('console-token');
 await client.productAccess('notes','staging');await client.applications();await client.userManagement();await client.managedUsers('Alice',25);await client.userProducts();await client.userAccess('immutable-user');await client.setUserAccess('immutable-user','notes','staging',{roles:[],active:true,version:0});await client.setUserStatus('immutable-user',{active:false,version:7});
 assert.equal(calls[0].path,'/v1/me/product-access?product_id=notes&instance=staging');assert.equal(calls[3].path,'/v1/user-management/users?q=Alice&limit=25');assert.equal(calls[6].path,'/v1/user-management/users/immutable-user/access/notes?instance=staging');assert.deepEqual(calls[6].body,{roles:[],active:true,version:0});assert.deepEqual(calls[7].body,{active:false,version:7});
});
