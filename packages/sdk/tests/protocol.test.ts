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
