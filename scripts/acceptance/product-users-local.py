#!/usr/bin/env python3
"""Explicit disposable local acceptance setup/serve/cleanup. Never targets production.

Requires a private management runtime, local Casdoor/Postgres/Formance, current
UniHub+LLMRouter checkouts, Go, psql and installed Playwright. Logs/config remain
in the private run directory; no provider bodies, secrets or tokens are printed.
"""
import argparse,sqlite3,urllib.request,importlib.util,json,os,re,secrets,signal,socket,subprocess,sys,time,urllib.parse
from pathlib import Path
U=Path(__file__).resolve().parents[2]
def save(path,value):
 path.write_text(json.dumps(value,indent=2)+'\n');os.chmod(path,0o600)
def provider(runtime):
 private=json.loads(runtime.read_text())
 spec=importlib.util.spec_from_file_location('product_client',U/'scripts/product_client.py');pc=importlib.util.module_from_spec(spec);spec.loader.exec_module(pc)
 return pc,pc.Provider('http://localhost:58000',private['HEADLESS_MANAGEMENT_CLIENT_ID'],private['HEADLESS_MANAGEMENT_CLIENT_SECRET']),private
def setup(run,runtime,L):
 suffix='puacc'+secrets.token_hex(4)
 run.mkdir(mode=0o700)
 (run/'stopped').write_text('no processes started')
 print('Private acceptance directory: '+str(run),flush=True)
 pc,p,private=provider(runtime)
 for port in range(58118,58122):
  s=socket.socket();s.bind(('127.0.0.1',port));s.close()
 # No notification hooks or enabled external provider metadata permitted for this run.
 dbparts=urllib.parse.urlsplit(private['DATABASE_URL']);dbquery=dict(urllib.parse.parse_qsl(dbparts.query));dbquery.pop('search_path',None);clean_db=urllib.parse.urlunsplit(dbparts._replace(query=urllib.parse.urlencode(dbquery)));casdoor_db=urllib.parse.urlunsplit(dbparts._replace(path='/casdoor',query=urllib.parse.urlencode(dbquery)))
 hookcheck=subprocess.run(['psql',casdoor_db,'-At','-c','SELECT count(*) FROM webhook'],capture_output=True,text=True);assert hookcheck.returncode==0 and hookcheck.stdout.strip()=='0','webhook preflight failed'
 save(run/'meta.json',{'suffix':suffix,'schema':'puacc_'+suffix,'ledger':'puacc_'+suffix})
 clients=[];records=[]
 for kind,product,port,callback,scopes in [('console','console',58118,'/auth/callback',[]),('router','llmrouter',58119,'/api/unihub/callback',['access:read','accounts:read','accounts:write','identity:read','catalog:read','subscriptions:read','credits:read','wallets:read','recharges:read','recharges:write','operations:read','orders:read','orders:write','usage:read','model-credentials:write']),('notes','notes',58120,'/auth/callback',['access:read'])]:
  c={'product_id':product,'instance_id':'default','client_id':suffix+'-'+kind,'application':'app-'+suffix+'-'+kind,'organization':'unihub','redirect_uris':['http://127.0.0.1:'+str(port)+callback],'origins':['http://127.0.0.1:'+str(port)],'scopes':scopes,'billing_products':[],'password_login':False,'registration':False,'browser_sso':True}
  app=pc.application(c,'cert-built-in');assert pc.data(p.request('GET',pc.app_path(app)),True) is None,'app collision'
  pc.preflight(p,private['HEADLESS_MANAGEMENT_CLIENT_ID'],app);assert not app['providers']
  app['clientSecret']=secrets.token_urlsafe(40)
  # Record requested owned names before uncertain writes so cleanup can read them back.
  records.append({'kind':'app','owner':'admin','name':app['name'],'clientId':app['clientId'],'description':app['description']})
  (run/'owned.json').write_text(json.dumps(records));os.chmod(run/'owned.json',0o600)
  pc.data(p.request('POST','/api/add-application',app));current=pc.data(p.request('GET',pc.app_path(app)));pc.compare_readback(current,app)
  clients.append(c)
  if kind=='console':console_secret=app['clientSecret']
  if kind=='router':router_secret=app['clientSecret']
  if kind=='notes':notes_secret=app['clientSecret']
 users=[]
 for kind in ['admin','member']:
  name=suffix+kind;uid=secrets.token_hex(16);password=secrets.token_urlsafe(24)+'aA1!'
  assert pc.data(p.request('GET','/api/get-user?'+urllib.parse.urlencode({'id':'unihub/'+name})),True) is None,'user collision'
  user={'owner':'unihub','name':name,'id':uid,'type':'normal-user','password':password,'passwordType':'plain','displayName':'Acceptance '+kind+' '+suffix,'email':'','phone':'','emailVerified':False,'isAdmin':False,'isForbidden':False,'isDeleted':False,'signupApplication':clients[0]['application'],'properties':{'productUserAcceptance':suffix}}
  records.append({'kind':'user','owner':'unihub','name':name,'id':uid,'marker':suffix});(run/'owned.json').write_text(json.dumps(records));os.chmod(run/'owned.json',0o600)
  pc.data(p.request('POST','/api/add-user',user));read=pc.data(p.request('GET','/api/get-user?'+urllib.parse.urlencode({'id':'unihub/'+name})));assert read['id']==uid and read['properties']['productUserAcceptance']==suffix
  users.append({'name':name,'password':password})
 (run/'clients.json').write_text(json.dumps(clients[1:]));man=json.loads((U/'config/examples/product-user-manifests.json').read_text());man[0]['entry_url']='http://127.0.0.1:58119';man[1]['entry_url']='http://127.0.0.1:58120';(run/'manifests.json').write_text(json.dumps(man))
 schema='puacc_'+suffix;db=urllib.parse.urlsplit(private['DATABASE_URL']);query=dict(urllib.parse.parse_qsl(db.query));query['search_path']=schema;dburl=urllib.parse.urlunsplit(db._replace(query=urllib.parse.urlencode(query)))
 subprocess.run(['psql',clean_db,'-v','ON_ERROR_STOP=1','-c','CREATE SCHEMA "'+schema+'"'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,check=True)
 base={'PATH':os.environ['PATH'],'HOME':os.environ['HOME'],'TMPDIR':str(run),'APP_ENV':'development','LISTEN_ADDR':'127.0.0.1:58118','DATABASE_URL':dburl,'LEDGER_URL':private['LEDGER_URL'],'LEDGER_NAME':schema,'OIDC_ISSUER':'http://localhost:58000','OIDC_AUDIENCE':clients[0]['client_id'],'OIDC_CLIENT_SECRET':console_secret,'PUBLIC_URL':'http://127.0.0.1:58118','CONSOLE_STATIC_DIR':str(U/'web/dist'),'PRODUCT_CLIENTS_FILE':str(run/'clients.json'),'PRODUCT_USER_MANIFESTS_FILE':str(run/'manifests.json'),'PAYMENT_MODE':'disabled','HEADLESS_IDENTITY_KEY':secrets.token_urlsafe(48),'HEADLESS_MANAGEMENT_CLIENT_ID':private['HEADLESS_MANAGEMENT_CLIENT_ID'],'HEADLESS_MANAGEMENT_CLIENT_SECRET':private['HEADLESS_MANAGEMENT_CLIENT_SECRET'],'HEADLESS_AUDIT_REDACTION_VERIFIED':private['HEADLESS_AUDIT_REDACTION_VERIFIED']}
 lr={'PATH':os.environ['PATH'],'HOME':os.environ['HOME'],'TMPDIR':str(run),'SERVER_BIND_ADDRESS':'127.0.0.1','SQLITE_PATH':str(run/'llmrouter.sqlite'),'SQL_DSN':'','LOG_SQL_DSN':'','REDIS_CONN_STRING':'','SESSION_SECRET':secrets.token_urlsafe(48),'CRYPTO_SECRET':secrets.token_urlsafe(48),'UNIHUB_PRODUCT_ENABLED':'true','UNIHUB_PLATFORM_ENABLED':'false','NATIVE_PUBLIC_ACCESS':'false','NATIVE_ADMIN_LISTEN_ADDRESS':'127.0.0.1:58121','UNIHUB_PRODUCT_PUBLIC_ORIGIN':'http://127.0.0.1:58118','UNIHUB_PRODUCT_API_ORIGIN':'http://127.0.0.1:58118','UNIHUB_PRODUCT_OIDC_ISSUER':'http://localhost:58000','UNIHUB_PRODUCT_CLIENT_ID':clients[1]['client_id'],'UNIHUB_PRODUCT_CLIENT_SECRET':router_secret,'UNIHUB_PRODUCT_REDIRECT_URI':'http://127.0.0.1:58119/api/unihub/callback','UNIHUB_PRODUCT_INSTANCE_ID':'default','UNIHUB_PRODUCT_MODELS':'[]','GIN_MODE':'release'}
 notes={'mode':'unihub','listen':'127.0.0.1:58120','public_url':'http://127.0.0.1:58120','api_url':'http://127.0.0.1:58118','central_issuer':'http://127.0.0.1:58118','oidc_issuer':'http://localhost:58000','client_id':clients[2]['client_id'],'client_secret':notes_secret,'product_id':'notes','instance_id':'default','database':str(run/'notes.json'),'access_token_type':'access-token'}
 for name,value in [('central-env.json',base),('lr-env.json',lr),('notes-config.json',notes),('users.json',users),('meta.json',{'suffix':suffix,'schema':schema,'ledger':schema})]:
  (run/name).write_text(json.dumps(value));os.chmod(run/name,0o600)
 print('Disposable applications/users/schema created and read back.')

def serve(run,L):
 if not run.is_dir():raise ValueError('Private run directory required')
 subprocess.run(['go','build','-o',str(run/'unihub'),'./cmd/unihub'],cwd=U,check=True,stdout=subprocess.DEVNULL,stderr=(run/'build-central.log').open('wb'))
 subprocess.run(['go','build','-o',str(run/'llmrouter'),'.'],cwd=L,check=True,stdout=subprocess.DEVNULL,stderr=(run/'build-router.log').open('wb'))
 if not (run/'generated-notes').exists():subprocess.run([sys.executable,str(U/'scripts/product_user_init.py'),'--output',str(run/'generated-notes'),'--product-id','notes','--module','example.test/notes'],check=True)
 subprocess.run(['go','build','-o',str(run/'notes-server'),'.'],cwd=run/'generated-notes',check=True)
 env=json.loads((run/'central-env.json').read_text());lr_env=json.loads((run/'lr-env.json').read_text())
 if (run/'admin-id').exists():env['USER_ADMIN_IDS']=(run/'admin-id').read_text().strip()
 files=[];processes=[]
 def start(name,args,environment,cwd):
  log=(run/(name+'.log')).open('ab');files.append(log)
  p=subprocess.Popen(args,env=environment,cwd=cwd,stdout=log,stderr=log);processes.append(p)
  save(run/'pids.json',[p.pid for p in processes]);return p
 def stop(p):
  if p.poll() is None:
   p.terminate()
   try:p.wait(timeout=15)
   except subprocess.TimeoutExpired:p.kill();p.wait(timeout=10)
 (run/'stopped').unlink(missing_ok=True)
 try:
  central=start('central',[str(run/'unihub'),'serve'],env,U)
  lr=start('lr',[str(run/'llmrouter'),'--port','58119','--log-dir',str(run/'lr-logs')],lr_env,L)
  notes=start('notes',[str(run/'notes-server')],{'PATH':os.environ['PATH'],'HOME':os.environ['HOME'],'PRODUCT_CONFIG':str(run/'notes-config.json')},run/'generated-notes')
  # Initialize only this new disposable database via the actual private setup API.
  def local_http(path,body=None):
   req=urllib.request.Request('http://127.0.0.1:58121'+path,data=json.dumps(body).encode() if body is not None else None,headers={'Content-Type':'application/json'})
   with urllib.request.urlopen(req,timeout=5) as response:return json.load(response)
  for attempt in range(60):
   try:initial=local_http('/api/setup');break
   except OSError:
    if attempt==59:raise ValueError('Owned router startup unavailable')
    time.sleep(.5)
  if not initial['data']['status']:
   root_password=secrets.token_urlsafe(32)
   if not local_http('/api/setup',{'username':'ownedroot','password':root_password,'confirmPassword':root_password,'SelfUseModeEnabled':False,'DemoSiteEnabled':False})['success']:raise ValueError('Disposable router initialization failed')
  if not (run/'channel-id').exists():
   with sqlite3.connect(run/'llmrouter.sqlite') as db:
    cursor=db.execute('insert into channels(type,key,status,name,weight,created_time,base_url,models,"group",used_quota,priority,auto_ban,channel_info,setting,settings) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)',(1,'LOCAL-ACCEPTANCE-NO-SUPPLIER',2,'Acceptance updated',1,int(time.time()),'http://127.0.0.1:1','acceptance-model','default',0,0,0,b'{}','{}','{}'));db.commit();(run/'channel-id').write_text(str(cursor.lastrowid))
  def end(_signum,_frame): (run/'stop').write_text('requested')
  signal.signal(signal.SIGTERM,end);signal.signal(signal.SIGINT,end)
  while not (run/'stop').exists():
   if (run/'admin-id').exists() and not (run/'admin-ready').exists():
    uid=(run/'admin-id').read_text().strip()
    if not re.fullmatch(r'[0-9a-f-]{36}',uid):raise ValueError('Invalid central UUID')
    stop(central);env['USER_ADMIN_IDS']=uid;central=start('central',[str(run/'unihub'),'serve'],env,U);time.sleep(2)
    if central.poll() is not None:raise ValueError('Isolated central restart failed')
    (run/'admin-ready').write_text('ready')
   if any(p.poll() is not None for p in [central,lr,notes]):raise ValueError('Owned service exited; inspect private logs')
   time.sleep(.4)
 finally:
  for p in reversed(processes):stop(p)
  for f in files:f.close()
  (run/'stopped').write_text('exact handles stopped')
  print('Exact owned server handles stopped.')

def cleanup(run,runtime):
 if not (run/'stopped').exists():raise ValueError('Stop the owned server supervisor before cleanup')
 if (run/'pids.json').exists():
  for pid in json.loads((run/'pids.json').read_text()):
   try:os.kill(pid,0)
   except ProcessLookupError:continue
   raise ValueError('Recorded process still exists; verify exact ownership before cleanup')
 pc,p,private=provider(runtime);records=json.loads((run/'owned.json').read_text());meta=json.loads((run/'meta.json').read_text())
 suffix=meta['suffix']
 if not re.fullmatch('puacc[0-9a-f]{8}',suffix) or meta['schema']!='puacc_'+suffix:raise ValueError('Unrecognized disposable run marker')
 for record in reversed(records):
  if record['kind']=='user':
   path='/api/get-user?'+urllib.parse.urlencode({'id':record['owner']+'/'+record['name']});current=pc.data(p.request('GET',path),True)
   if current is None:continue
   if current.get('owner')!='unihub' or current.get('name')!=record['name'] or current.get('id')!=record['id'] or current.get('properties',{}).get('productUserAcceptance')!=suffix:raise ValueError('User cleanup ownership mismatch')
   pc.data(p.request('POST','/api/delete-user',{'owner':'unihub','name':record['name']}))
  else:
   path='/api/get-application?'+urllib.parse.urlencode({'id':record['owner']+'/'+record['name']});current=pc.data(p.request('GET',path),True)
   if current is None:continue
   if current.get('organization')!='unihub' or current.get('owner')!='admin' or current.get('name')!=record['name'] or current.get('clientId')!=record['clientId'] or current.get('description')!=record['description']:raise ValueError('Application cleanup ownership mismatch')
   pc.data(p.request('POST','/api/delete-application',{'owner':'admin','name':record['name'],'organization':current['organization']}))
  if pc.data(p.request('GET',path),True) is not None:raise ValueError('Deletion readback failed; do not retry automatically')
 parts=urllib.parse.urlsplit(private['DATABASE_URL']);query=dict(urllib.parse.parse_qsl(parts.query));query.pop('search_path',None);db=urllib.parse.urlunsplit(parts._replace(query=urllib.parse.urlencode(query)))
 subprocess.run(['psql',db,'-v','ON_ERROR_STOP=1','-c','DROP SCHEMA IF EXISTS "'+meta['schema']+'" CASCADE'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 read=subprocess.run(['psql',db,'-At','-c',"SELECT count(*) FROM pg_namespace WHERE nspname='"+meta['schema']+"'"],capture_output=True,text=True)
 if read.returncode or read.stdout.strip()!='0':raise ValueError('Schema cleanup readback failed')
 save(run/'cleanup.json',{'apps_deleted':sum(x['kind']=='app' for x in records),'users_deleted':sum(x['kind']=='user' for x in records),'schema_deleted':meta['schema'],'ledger_retained':meta['ledger']})
 print('Exact recorded test apps/users/schema removed and read back. Unique ledger retained: '+meta['ledger'])

def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('command',choices=['setup','serve','cleanup']);parser.add_argument('--run-dir',type=Path,required=True);parser.add_argument('--runtime',type=Path);parser.add_argument('--llmrouter',type=Path)
 args=parser.parse_args();run=args.run_dir.absolute()
 try:
  if args.command=='setup':
   if not args.runtime or not args.llmrouter:raise ValueError('setup requires --runtime and --llmrouter')
   setup(run,args.runtime.absolute(),args.llmrouter.absolute())
  elif args.command=='serve':
   if not args.llmrouter:raise ValueError('serve requires --llmrouter')
   serve(run,args.llmrouter.absolute())
  else:
   if not args.runtime:raise ValueError('cleanup requires --runtime')
   cleanup(run,args.runtime.absolute())
 except Exception:
  print('Local acceptance operation failed. Inspect private run files and exact ownership before any mutation retry.',file=sys.stderr);return 1
 return 0
if __name__=='__main__':raise SystemExit(main())
