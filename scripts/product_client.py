#!/usr/bin/env python3
"""Prepare/review/apply one private UniHub product client. Python stdlib only.

Never prints provider response bodies or credentials. prepare is offline; apply
writes only its exact named managed application, verifies it, then atomically
publishes the public registry. readback never mutates either system.
"""
import argparse
import base64
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

SCOPES=set('access:read identity:read profile:write accounts:read accounts:write members:read members:write catalog:read subscriptions:read credits:read wallets:read recharges:read recharges:write transfers:write operations:read orders:read orders:write settings:read settings:write refunds:write usage:all-products usage:read model-credentials:write'.split())
FIELDS={'product_id','client_id','application','organization','redirect_uris','origins','scopes','billing_products','password_login','registration','browser_sso','instance_id'}
SLUG=re.compile(r'^[a-z][a-z0-9_-]{0,63}$')
NAME=re.compile(r'^[A-Za-z][A-Za-z0-9_-]{1,63}$')
class PolicyError(Exception):pass

def valid_url(raw,origin=False):
 if not isinstance(raw,str) or re.search(r'[*\\\s]',raw) or re.search(r'%(?![0-9a-fA-F]{2})',raw):return False
 try:
  u=urllib.parse.urlsplit(raw);_ = u.port
  decoded=urllib.parse.unquote(u.path)+urllib.parse.unquote_plus(u.query)
  if not u.hostname or u.username is not None or u.password is not None or '#' in raw or re.search(r'[*\\\s]',decoded):return False
  if u.scheme!='https':
   try:loopback=ipaddress.ip_address(u.hostname).is_loopback
   except ValueError:loopback=u.hostname=='localhost'
   if u.scheme!='http' or not loopback:return False
  return not origin or (u.path=='' and u.query=='' and '?' not in raw)
 except (ValueError,TypeError):return False

def validate_client(c,managed=True):
 if not isinstance(c,dict) or set(c)-FIELDS:raise PolicyError('Registry contains unknown or secret fields')
 if not isinstance(c.get('product_id'),str) or not SLUG.fullmatch(c['product_id']):raise PolicyError('Invalid product ID')
 if not isinstance(c.get('client_id'),str) or not c['client_id'] or c['client_id'].strip()!=c['client_id'] or len(c['client_id'])>256:raise PolicyError('Invalid client audience')
 if managed and c.get('organization')!='unihub':raise PolicyError('Only private unihub organization is permitted')
 if managed and (not isinstance(c.get('application'),str) or not NAME.fullmatch(c['application']) or c['application'] in {'app-built-in','app-unihub'}):raise PolicyError('An independent named product application is required')
 if (c.get('password_login') or c.get('registration')) and '-org-' in c['client_id']:raise PolicyError('Identity-enabled client audience conflicts with organization token routing')
 if 'instance_id' in c and (not isinstance(c['instance_id'],str) or not SLUG.fullmatch(c['instance_id'])):raise PolicyError('Invalid instance ID')
 for field in ['password_login','registration','browser_sso']:
  if type(c.get(field,False)) is not bool:raise PolicyError('Identity capability flags must be booleans')
 for field in ['scopes','billing_products','origins','redirect_uris']:
  if not isinstance(c.get(field,[]),list) or any(not isinstance(x,str) for x in c.get(field,[])) or len(set(c.get(field,[])))!=len(c.get(field,[])):raise PolicyError('Policy fields must be unique string lists')
 if managed and not c.get('redirect_uris'):raise PolicyError('At least one exact redirect URI is required')
 if set(c.get('scopes',[]))-SCOPES:raise PolicyError('Unknown product scope')
 if 'usage:all-products' in c.get('scopes',[]) and c.get('product_id')!='llmrouter':raise PolicyError('usage:all-products requires llmrouter')
 if any(not SLUG.fullmatch(x) for x in c.get('billing_products',[])):raise PolicyError('Invalid billing product')
 if any(not valid_url(x,True) for x in c.get('origins',[])) or any(not valid_url(x) for x in c.get('redirect_uris',[])):raise PolicyError('Unsafe exact redirect URI or Origin')
 return c

def application(c,cert):
 validate_client(c)
 if not isinstance(cert,str) or not NAME.fullmatch(cert):raise PolicyError('An existing signing certificate name is required')
 password=c.get('password_login',False) or c.get('registration',False) or c.get('browser_sso',False)
 return {'owner':'admin','name':c['application'],'displayName':c['product_id'],'description':'UniHub managed product: '+c['product_id'],'organization':'unihub','cert':cert,'category':'Default','type':'All','isShared':False,'clientId':c['client_id'],'enablePassword':password,'enableSignUp':c.get('registration',False),'disableSelfSignup':not c.get('registration',False),'disableSignin':False,'enableSigninSession':c.get('browser_sso',False),'enableAutoSignin':c.get('browser_sso',False),'defaultGroup':'','defaultTag':'','providers':[],'signinMethods':[{'name':'Password','displayName':'Password','rule':'All' if password else 'Hide password'}],'signinItems':[{'name':x,'visible':True,'rule':'None'} for x in ['Username','Password','Login button']],'signupItems':[{'name':'Username','visible':True,'required':True,'rule':'None'},{'name':'Password','visible':True,'required':True,'rule':'None'},{'name':'Display name','visible':True,'required':False,'rule':'All'},{'name':'Email','visible':True,'required':False,'rule':'No verification'}],'grantTypes':['authorization_code'],'redirectUris':c['redirect_uris'],'tokenFormat':'JWT-Standard','tokenSigningMethod':'RS256','expireInHours':1,'cookieExpireInHours':1,'failedSigninLimit':5,'failedSigninFrozenTime':15}

def digest_file(p):
 if p.is_symlink():raise PolicyError('Registry must not be a symlink')
 if not p.exists():return None
 if not p.is_file() or p.stat().st_size>1<<20:raise PolicyError('Registry must be a bounded regular file')
 return hashlib.sha256(p.read_bytes()).hexdigest()

def merge_registry(p,c):
 digest_file(p)
 try:clients=json.loads(p.read_text()) if p.exists() else []
 except (ValueError,OSError):raise PolicyError('Existing registry is invalid')
 if not isinstance(clients,list):raise PolicyError('Existing registry must be an array')
 out=[];seen=set();found=False
 for old in clients:
  validate_client(old,managed=False)
  if old['client_id'] in seen:raise PolicyError('Existing registry has duplicate audiences')
  seen.add(old['client_id'])
  if old['client_id']==c['client_id']:
   if any(old.get(k)!=c.get(k) for k in ['product_id','application','organization']):raise PolicyError('Audience belongs to a different product/application')
   out.append(c);found=True
  else:
   if old.get('application')==c['application']:raise PolicyError('Application belongs to a different client')
   out.append(old)
 if not found:out.append(c)
 return out

def prepare(registry,c,cert):
 validate_client(c);registry=Path(registry).absolute();merge_registry(registry,c)
 return {'version':1,'registry_path':str(registry),'registry_before_sha256':digest_file(registry),'client':c,'certificate':cert,'application':application(c,cert)}

def validate_plan(plan):
 if not isinstance(plan,dict) or set(plan)!={'version','registry_path','registry_before_sha256','client','certificate','application'} or plan['version']!=1:raise PolicyError('Invalid public plan')
 validate_client(plan['client'])
 if plan['application']!=application(plan['client'],plan['certificate']):raise PolicyError('Application plan does not match public client policy')
 return plan

def write_atomic(path,value):
 path=Path(path);path.parent.mkdir(parents=True,exist_ok=True)
 fd,tmp=tempfile.mkstemp(prefix='.'+path.name+'-',dir=path.parent)
 try:
  with os.fdopen(fd,'w') as stream:json.dump(value,stream,ensure_ascii=False,indent=2);stream.write('\n');stream.flush();os.fsync(stream.fileno())
  os.chmod(tmp,0o600);os.replace(tmp,path)
 finally:
  if os.path.exists(tmp):os.unlink(tmp)

def publish_registry(plan):
 validate_plan(plan);path=Path(plan['registry_path'])
 if digest_file(path)!=plan['registry_before_sha256']:raise PolicyError('Registry changed after prepare; review a new plan')
 write_atomic(path,merge_registry(path,plan['client']))

def check_existing(existing,wanted):
 if not isinstance(existing,dict) or any(existing.get(k)!=wanted[k] for k in ['owner','name','organization','clientId','description']) or existing.get('isShared') is not False:raise PolicyError('Existing application identity/ownership does not match this managed product')
 if existing.get('providers') or existing.get('defaultGroup') or existing.get('defaultTag'):raise PolicyError('Existing application has independent security policy; manual review required')

def data(result,allow_none=False):
 if not isinstance(result,dict) or result.get('status')!='ok':raise PolicyError('Provider rejected request; inspect private IdP audit without exposing secrets')
 value=result.get('data')
 if value is None and not allow_none:raise PolicyError('Provider returned incomplete policy')
 return value

def preflight(provider,management_id,wanted):
 envelope=provider.request('GET','/api/get-app-login?'+urllib.parse.urlencode({'type':'code','responseType':'code','clientId':management_id}),management=False)
 app=envelope.get('data') if isinstance(envelope,dict) else None
 if not isinstance(app,dict) or app.get('organization')!='unihub' or app.get('clientId')!=management_id or app.get('isShared') is not False or management_id==wanted['clientId'] or app.get('name')==wanted['name']:raise PolicyError('Management credential must belong to a separate private unihub application')
 org=data(provider.request('GET','/api/get-organization?id=admin%2Funihub'))
 required=['name','passwordType','passwordObfuscatorType','passwordOptions','masterPassword','masterVerificationCode','defaultPassword']
 if not isinstance(org,dict) or any(k not in org for k in required) or org['name']!='unihub' or org['passwordType']!='bcrypt' or org['passwordObfuscatorType'] not in ['', 'None'] or any(org[k] for k in ['masterPassword','masterVerificationCode','defaultPassword']) or not isinstance(org['passwordOptions'],list) or any(not isinstance(x,str) for x in org['passwordOptions']) or set(org['passwordOptions'])-{'AtLeast6','AtLeast8','Aa123','SpecialChar','NoRepeat'}:raise PolicyError('Private organization policy is incompatible; no changes made')

def app_path(wanted):return '/api/get-application?'+urllib.parse.urlencode({'id':'admin/'+wanted['name']})
def compare_readback(current,wanted):
 check_existing(current,wanted)
 # Casdoor extends known nested policy records with zero/default fields.
 def subset(actual,expected):
  if isinstance(expected,dict):return isinstance(actual,dict) and all(k in actual and subset(actual[k],v) for k,v in expected.items())
  if isinstance(expected,list):return isinstance(actual,list) and len(actual)==len(expected) and all(subset(a,b) for a,b in zip(actual,expected))
  return actual==expected
 if not subset(current,wanted):raise PolicyError('Application readback differs from reviewed plan; registry was not published')

def apply(plan,provider,management_id):
 validate_plan(plan);wanted=plan['application'];path=Path(plan['registry_path'])
 if digest_file(path)!=plan['registry_before_sha256']:raise PolicyError('Registry changed after prepare; review a new plan')
 preflight(provider,management_id,wanted)
 existing=data(provider.request('GET',app_path(wanted)),True)
 if existing is not None:
  check_existing(existing,wanted)
  try:compare_readback(existing,wanted);changed=False
  except PolicyError:changed=True
  if changed:
   body=dict(existing);body.update(wanted);body['clientSecret']='***'
   data(provider.request('POST','/api/update-application?'+urllib.parse.urlencode({'id':'admin/'+wanted['name']}),body))
 else:data(provider.request('POST','/api/add-application',wanted))
 compare_readback(data(provider.request('GET',app_path(wanted))),wanted)
 publish_registry(plan)

def readback(plan,provider,management_id):
 validate_plan(plan);preflight(provider,management_id,plan['application']);compare_readback(data(provider.request('GET',app_path(plan['application']))),plan['application'])
 clients=merge_registry(Path(plan['registry_path']),plan['client'])
 if not Path(plan['registry_path']).exists() or json.loads(Path(plan['registry_path']).read_text())!=clients:raise PolicyError('Application matches but registry is not published; apply the reviewed plan')

class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self,*args,**kwargs):raise PolicyError('Provider redirects are forbidden')
class Provider:
 def __init__(self,issuer,client,secret):
  if not valid_url(issuer,True):raise PolicyError('Issuer must be an HTTPS or loopback HTTP origin')
  self.issuer=issuer;self.auth='Basic '+base64.b64encode((client+':'+secret).encode()).decode();self.opener=urllib.request.build_opener(NoRedirect())
 def request(self,method,path,body=None,management=True):
  headers={'Accept':'application/json','Content-Type':'application/json'}
  if management:headers['Authorization']=self.auth
  req=urllib.request.Request(self.issuer+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method)
  try:
   with self.opener.open(req,timeout=10) as response:
    raw=response.read((1<<20)+1)
    if len(raw)>1<<20:raise PolicyError('Provider response exceeded policy limit')
    return json.loads(raw)
  except PolicyError:raise
  except (urllib.error.URLError,ValueError,OSError):raise PolicyError('Provider result unavailable/unknown; do not retry mutation automatically. Use readback and inspect original application.')

def main(argv=None):
 parser=argparse.ArgumentParser(description=__doc__);sub=parser.add_subparsers(dest='command',required=True)
 prep=sub.add_parser('prepare',help='Offline: generate a reviewable public plan; no remote or registry changes')
 for name in ['registry','output','product-id','client-id','application']:prep.add_argument('--'+name,required=True)
 prep.add_argument('--redirect-uri',action='append',required=True);prep.add_argument('--origin',action='append',default=[]);prep.add_argument('--scope',action='append');prep.add_argument('--billing-product',action='append');prep.add_argument('--certificate',default='cert-built-in');prep.add_argument('--password-login',action='store_true');prep.add_argument('--registration',action='store_true');prep.add_argument('--browser-sso',action='store_true');prep.add_argument('--instance-id',default='default')
 for command in ['apply','readback']:
  p=sub.add_parser(command,help='Apply exact named application and publish after readback' if command=='apply' else 'Read-only remote and registry verification');p.add_argument('--plan',required=True);p.add_argument('--issuer',required=True)
 args=parser.parse_args(argv)
 try:
  if args.command=='prepare':
   if Path(args.registry).absolute()==Path(args.output).absolute():raise PolicyError('Plan output must differ from registry path')
   c={'product_id':args.product_id,'client_id':args.client_id,'application':args.application,'organization':'unihub','redirect_uris':args.redirect_uri,'origins':args.origin,'scopes':args.scope or ['accounts:read','catalog:read'],'billing_products':args.billing_product or [args.product_id],'password_login':args.password_login,'registration':args.registration,'browser_sso':args.browser_sso,'instance_id':args.instance_id}
   plan=prepare(Path(args.registry),c,args.certificate);write_atomic(Path(args.output),plan);print('Public product plan prepared; review before apply. No remote mutation occurred.')
  else:
   plan=validate_plan(json.loads(Path(args.plan).read_text()));client=os.environ.get('HEADLESS_MANAGEMENT_CLIENT_ID','');secret=os.environ.get('HEADLESS_MANAGEMENT_CLIENT_SECRET','')
   if not client or not secret:raise PolicyError('HEADLESS_MANAGEMENT_CLIENT_ID and HEADLESS_MANAGEMENT_CLIENT_SECRET must be set privately in the environment')
   provider=Provider(args.issuer,client,secret)
   (apply if args.command=='apply' else readback)(plan,provider,client);print('Named private product application and public registry verified.' if args.command=='readback' else 'Named private product application verified; public registry published. Deploy/reload UniHub separately.')
  return 0
 except (PolicyError,ValueError,OSError) as error:
  # Policy errors are intentionally fixed, nonsecret messages. Never serialize
  # provider JSON, application clientSecret, env values or request bodies.
  print('Product onboarding failed: '+(str(error) if isinstance(error,PolicyError) else 'Invalid local plan or file operation'),file=sys.stderr);return 1
if __name__=='__main__':raise SystemExit(main())
