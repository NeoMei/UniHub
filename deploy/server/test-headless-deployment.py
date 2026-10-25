#!/usr/bin/env python3
"""Disposable deployment-contract regressions. No production resources touched."""
import importlib.util
from datetime import datetime,timezone,timedelta
import hashlib
import json
import os
from pathlib import Path
import stat
import tempfile
import shutil
import socket
import ssl
import subprocess
import threading
import time
import secrets
import urllib.request
import urllib.parse
import unittest
from http.server import BaseHTTPRequestHandler,HTTPServer
from unittest.mock import patch

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('headless_bootstrap',HERE/'bootstrap.py')
bundle=importlib.util.module_from_spec(spec);spec.loader.exec_module(bundle)


class HeadlessDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name).resolve()/'unihub';self.release=self.root/'releases'/'new'
        for relative in ['bin/unihub','web/dist/index.html','casdoor-web/index.html','casdoor-web/swagger/index.html','casdoor-web/assets/unihub/unihub-logo-v1.png','casdoor-web/assets/unihub/unihub-icon-v1.png']:
            file=self.release/relative;file.parent.mkdir(parents=True,exist_ok=True);file.write_text('fixture')
        self.credentials=bundle.prepare(self.root,self.release,{})
        self.registry=[{'product_id':'superwagie','client_id':'superwagie-headless','application':'app-superwagie-headless','organization':'unihub','redirect_uris':['http://localhost:57991/api/platform-account/callback'],'origins':['http://localhost:57991'],'scopes':['accounts:read','profile:write','wallets:read','orders:write','usage:read'],'billing_products':['superwagie','llmrouter'],'password_login':True,'registration':True}]

    def test_staging_is_additive_private_and_runtime_identity_remains_disabled(self):
        original=dict(self.credentials)
        bundle.stage_headless(self.root,self.registry)
        self.credentials=bundle.prepare(self.root,self.release,{})
        for key,value in original.items():self.assertEqual(self.credentials[key],value)
        runtime=bundle.read_env(self.root/'shared/runtime.env')
        self.assertEqual(runtime['HEADLESS_AUDIT_REDACTION_VERIFIED'],'false')
        self.assertEqual(runtime['HEADLESS_TRUST_LOOPBACK_PROXY'],'true')
        self.assertNotEqual(runtime['HEADLESS_MANAGEMENT_CLIENT_SECRET'],runtime['OIDC_CLIENT_SECRET'])
        public=Path(runtime['PRODUCT_CLIENTS_FILE']);self.assertEqual(stat.S_IMODE(public.stat().st_mode),0o644)
        self.assertEqual(stat.S_IMODE(public.parent.stat().st_mode),0o755)
        self.assertNotIn('shared',public.parts)
        config=json.loads(public.read_text());self.assertFalse(config[0]['password_login']);self.assertFalse(config[0]['registration']);self.assertIn('profile:write',config[0]['scopes'])
        seed=json.loads((self.root/'shared/casdoor/conf/init_data.json').read_text())
        app=next(a for a in seed['applications'] if a['name']==bundle.PROVISIONER_APPLICATION)
        self.assertEqual(app['organization'],'unihub');self.assertFalse(app['isShared']);self.assertTrue(app['disableSignin']);self.assertFalse(app['enablePassword']);self.assertFalse(app['enableSignUp']);self.assertEqual(app['redirectUris'],[])
        first=(self.root/'shared/credentials.json').read_bytes();bundle.prepare(self.root,self.release,{})
        self.assertEqual(first,(self.root/'shared/credentials.json').read_bytes())

    def test_all_products_usage_scope_is_deployable_only_for_llmrouter(self):
        item={**self.registry[0], 'product_id':'llmrouter', 'scopes':['usage:read','usage:all-products']}
        bundle.stage_headless(self.root,[item])
        bundle.prepare(self.root,self.release,{})
        runtime=bundle.read_env(self.root/'shared/runtime.env')
        config=json.loads(Path(runtime['PRODUCT_CLIENTS_FILE']).read_text())
        self.assertIn('usage:all-products',config[0]['scopes'])
        for product in ['superwagie','llmrouter-other']:
            with self.subTest(product=product), self.assertRaises(bundle.DeploymentError):
                bundle.stage_headless(self.root,[{**item,'product_id':product}])

    def test_registry_cannot_reuse_console_legacy_or_provisioner_identity(self):
        for client in ['unihub-console','superwagie-unihub',bundle.PROVISIONER_CLIENT]:
            item={**self.registry[0],'client_id':client}
            with self.assertRaises(bundle.DeploymentError):bundle.stage_headless(self.root,[item])
        with self.assertRaises(bundle.DeploymentError):bundle.stage_headless(self.root,[{**self.registry[0],'organization':'built-in'}])
        self.assertNotIn('headless_management_secret',json.loads((self.root/'shared/credentials.json').read_text()))

    def test_owned_official_upgrade_is_narrow_and_immutable_custom_id_checked(self):
        pin={'image_id':'sha256:'+'a'*64,'backend_sha256':'b'*64,'source_commit':bundle.AUDITED_COMMIT,'installation_id':self.credentials['installation_id']}
        bundle.atomic_write(self.root/'shared/identity-image.json',json.dumps(pin))
        resource=self.owned_casdoor(bundle.IMAGES['casdoor'])
        bundle.validate_resources({'container:unihub-server-casdoor':resource},self.credentials,self.root)
        resource['Config']['Image']=pin['image_id'];resource['Image']=pin['image_id']
        bundle.validate_resources({'container:unihub-server-casdoor':resource},self.credentials,self.root)
        resource['Image']='sha256:'+'c'*64
        with self.assertRaises(bundle.DeploymentError):bundle.validate_resources({'container:unihub-server-casdoor':resource},self.credentials,self.root)
        resource=self.owned_casdoor(bundle.IMAGES['casdoor']);resource['Config']['Labels'][bundle.LABEL]='another-install'
        with self.assertRaises(bundle.DeploymentError):bundle.validate_resources({'container:unihub-server-casdoor':resource},self.credentials,self.root)

    def test_evidence_cannot_be_label_only_stale_or_for_another_image(self):
        pin={'image_id':'sha256:'+'a'*64,'backend_sha256':'b'*64,'source_commit':bundle.AUDITED_COMMIT}
        for value in [{},{'schema_version':1,'installation_id':self.credentials['installation_id'],'image_id':pin['image_id'],'backend_sha256':pin['backend_sha256'],'checks':{name:True for name in bundle.PRIVACY_CHECKS}}]:
            with self.assertRaises(bundle.DeploymentError):bundle.validate_privacy_evidence(value,self.credentials,pin,self.release)

    def test_real_evidence_enables_only_current_backend_and_survives_identical_prepare(self):
        bundle.stage_headless(self.root,self.registry);self.credentials=bundle.prepare(self.root,self.release,{})
        pin={'image_id':'sha256:'+'a'*64,'backend_sha256':'b'*64,'source_commit':bundle.AUDITED_COMMIT,'transition_complete':True,'installation_id':self.credentials['installation_id']}
        bundle.atomic_write(self.root/'shared/identity-image.json',json.dumps(pin))
        proof=self.release/'proof.json';proof.write_text('{"record_rows_checked":3,"secret_markers_found":0}')
        report={'schema_version':1,'installation_id':self.credentials['installation_id'],'image_id':pin['image_id'],'backend_sha256':pin['backend_sha256'],'verified_at':datetime.now(timezone.utc).isoformat(),'checks':{k:True for k in bundle.PRIVACY_CHECKS},'evidence_files':[{'path':'proof.json','sha256':hashlib.sha256(proof.read_bytes()).hexdigest()}]}
        report['policy_sha256']=bundle.file_sha256(self.root/'headless-config/products.draft.json')
        for bad in [{**report,'verified_at':(datetime.now(timezone.utc)-timedelta(days=2)).isoformat()},{**report,'image_id':'sha256:'+'c'*64},{**report,'checks':{**report['checks'],'audit_records_redacted':False}}]:
            with self.assertRaises(bundle.DeploymentError):bundle.validate_privacy_evidence(bad,self.credentials,pin,self.release)
        path=self.release/'privacy.json';path.write_text(json.dumps(report))
        resource=self.owned_casdoor(pin['image_id']);resource['Image']=pin['image_id'];resource['State']={'Running':True}
        inventory={'container:unihub-server-casdoor':resource}
        with patch.object(bundle,'run',return_value=pin['backend_sha256']+'  /server\n'):
            bundle.enable_headless(self.root,self.release,path,inventory)
        runtime=bundle.read_env(self.root/'shared/runtime.env')
        self.assertEqual(runtime['HEADLESS_AUDIT_REDACTION_VERIFIED'],'true')
        self.assertTrue(json.loads(Path(runtime['PRODUCT_CLIENTS_FILE']).read_text())[0]['registration'])
        bundle.prepare(self.root,self.release,inventory);bundle.stage_headless(self.root,self.registry);bundle.prepare(self.root,self.release,inventory)
        self.assertEqual(bundle.read_env(self.root/'shared/runtime.env')['HEADLESS_AUDIT_REDACTION_VERIFIED'],'true')
        proof.write_text('altered')
        with self.assertRaises(bundle.DeploymentError):bundle.validate_privacy_evidence(report,self.credentials,pin,self.release)

    def test_existing_application_policy_mismatch_refuses_instead_of_rotating_secret(self):
        bundle.stage_headless(self.root,self.registry)
        credentials=json.loads((self.root/'shared/credentials.json').read_text())
        sql=bundle.headless_applications_sql(credentials)
        self.assertIn('jsonb_populate_record',sql)
        self.assertIn('Headless application ownership or credentials conflict',sql)
        self.assertNotIn('UPDATE application',sql)
        self.assertNotIn('DELETE FROM',sql)

    def test_seed_product_is_compatible_with_generic_managed_policy_readback(self):
        module=HERE.parents[1]/'scripts/product_client.py'
        spec=importlib.util.spec_from_file_location('managed_product_policy',module)
        policy=importlib.util.module_from_spec(spec);spec.loader.exec_module(policy)
        actual=bundle.headless_product_application(self.registry[0],'private-fixture')
        policy.compare_readback(actual,policy.application(self.registry[0],'cert-built-in'))
        actual['providers']=[{'name':'independent-provider'}]
        with self.assertRaises(policy.PolicyError):policy.compare_readback(actual,policy.application(self.registry[0],'cert-built-in'))

    @unittest.skipUnless(shutil.which('psql') and os.environ.get('UNIHUB_TEST_DATABASE_URL'),'isolated PostgreSQL test URL unavailable')
    def test_provisioner_sql_creates_only_scoped_apps_and_rejects_changed_owner(self):
        bundle.stage_headless(self.root,self.registry)
        credentials=json.loads((self.root/'shared/credentials.json').read_text())
        uri=urllib.parse.urlsplit(os.environ['UNIHUB_TEST_DATABASE_URL'])
        env={**os.environ,'PGDATABASE':urllib.parse.unquote(uri.path.lstrip('/')),'PGHOST':uri.hostname or 'localhost','PGPORT':str(uri.port or 5432),'PGUSER':urllib.parse.unquote(uri.username or ''),'PGPASSWORD':urllib.parse.unquote(uri.password or ''),'PGSSLMODE':urllib.parse.parse_qs(uri.query).get('sslmode',['prefer'])[0]}
        schema='test_deploy_'+secrets.token_hex(12)
        def sql(value,ok=True):
            result=subprocess.run(['psql','-X','-At','-q','-v','ON_ERROR_STOP=1'],input=value,text=True,capture_output=True,env=env)
            self.assertEqual(result.returncode==0,ok,'isolated SQL policy command had an unexpected outcome')
            return result.stdout.strip()
        sql('CREATE SCHEMA '+schema+';')
        try:
            prefix='SET search_path='+schema+';\n'
            sql(prefix+'''CREATE TABLE application(owner text,name text,organization text,is_shared boolean,client_id text,client_secret text,disable_signin boolean,enable_password boolean,enable_sign_up boolean,grant_types varchar(1000),redirect_uris varchar(1000),PRIMARY KEY(owner,name));
INSERT INTO application VALUES('admin','app-unihub','unihub',false,'unihub-console','fixture',false,true,false,'["authorization_code"]','["https://unihub.quukk.com/auth/callback"]');''')
            sql(prefix+'''ALTER TABLE application ADD COLUMN display_name text, ADD COLUMN description text, ADD COLUMN cert text, ADD COLUMN category text, ADD COLUMN type text, ADD COLUMN disable_self_signup boolean, ADD COLUMN enable_signin_session boolean, ADD COLUMN enable_auto_signin boolean, ADD COLUMN default_group text, ADD COLUMN default_tag text, ADD COLUMN providers text, ADD COLUMN signin_methods text, ADD COLUMN signin_items text, ADD COLUMN signup_items text, ADD COLUMN token_format text, ADD COLUMN token_signing_method text, ADD COLUMN expire_in_hours double precision, ADD COLUMN cookie_expire_in_hours bigint, ADD COLUMN failed_signin_limit integer, ADD COLUMN failed_signin_frozen_time integer;''')
            statement=bundle.headless_applications_sql(credentials)
            sql(prefix+statement);sql(prefix+statement)
            count=sql(prefix+"SELECT count(*) FROM application WHERE organization='unihub' AND is_shared=false;")
            self.assertEqual(count,'3')
            policy=sql(prefix+"SELECT disable_signin::text||':'||enable_password::text||':'||enable_sign_up::text||':'||grant_types FROM application WHERE name='"+bundle.PROVISIONER_APPLICATION+"';")
            self.assertEqual(policy,'true:false:false:[]')
            product=json.loads(sql(prefix+"SELECT json_build_object('enabled',enable_sign_up,'description',description,'items',signup_items::json,'methods',signin_methods::json) FROM application WHERE name='app-superwagie-headless';"))
            self.assertTrue(product['enabled']);self.assertEqual(product['description'],'UniHub managed product: superwagie')
            email=next(x for x in product['items'] if x['name']=='Email')
            self.assertFalse(email['required']);self.assertEqual(email['rule'],'No verification')
            self.assertEqual(product['methods'],[{'name':'Password','displayName':'Password','rule':'All'}])
            sql(prefix+"UPDATE application SET signup_items=jsonb_set(signup_items::jsonb,'{0,prompted}','false',true)::text, signin_items=jsonb_set(signin_items::jsonb,'{0,isCustom}','false',true)::text WHERE name='app-superwagie-headless';")
            sql(prefix+statement)
            sql(prefix+"UPDATE application SET organization='built-in' WHERE name='app-superwagie-headless';")
            sql(prefix+statement,ok=False)
            self.assertEqual(sql(prefix+"SELECT organization FROM application WHERE name='app-superwagie-headless';"),'built-in')
        finally:sql('DROP SCHEMA '+schema+' CASCADE;')

    def test_current_nginx_replaces_real_ip_instead_of_forwarding_spoofed_header(self):
        text=(HERE/'nginx.conf').read_text()
        self.assertIn('proxy_set_header X-Real-IP $remote_addr;',text)
        self.assertNotIn('proxy_set_header X-Real-IP $http_x_real_ip',text)

    @unittest.skipUnless(shutil.which('nginx') and shutil.which('openssl'),'nginx/openssl unavailable')
    def test_actual_nginx_overwrites_attacker_real_ip_for_go(self):
        class Upstream(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200);self.end_headers();self.wfile.write(self.headers.get('X-Real-IP','').encode())
            def log_message(self,*args):pass
        server=HTTPServer(('127.0.0.1',0),Upstream);threading.Thread(target=server.serve_forever,daemon=True).start()
        self.addCleanup(server.server_close);self.addCleanup(server.shutdown)
        def port():
            with socket.socket() as probe:probe.bind(('127.0.0.1',0));return probe.getsockname()[1]
        http,tls=port(),port();folder=Path(self.temp.name).resolve()
        subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-keyout',str(folder/'key.pem'),'-out',str(folder/'cert.pem'),'-days','1','-nodes','-subj','/CN=unihub.quukk.com'],capture_output=True,check=True)
        source=(HERE/'nginx.conf').read_text()
        for before,after in {'listen 80;':f'listen {http};','listen 8484 ssl;':f'listen {tls} ssl;','127.0.0.1:8088':f'127.0.0.1:{server.server_port}','/etc/nginx/ssl/_.quukk.com_chain.pem':str(folder/'cert.pem'),'/etc/nginx/ssl/_.quukk.com_key.key':str(folder/'key.pem'),'/var/log/nginx/unihub-access.log':str(folder/'access.log')}.items():source=source.replace(before,after)
        config=folder/'nginx.conf';config.write_text('error_log '+str(folder/'nginx-error.log')+';\npid '+str(folder/'nginx.pid')+';\nevents {}\nhttp {\n'+source+'\n}')
        command=['nginx','-p',str(folder)+'/', '-c',str(config)]
        subprocess.run(command+['-t'],capture_output=True,check=True)
        process=subprocess.Popen(command+['-g','daemon off;'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        self.addCleanup(lambda:process.wait(timeout=3));self.addCleanup(process.terminate)
        request=urllib.request.Request(f'https://127.0.0.1:{tls}/v1/probe',headers={'X-Real-IP':'203.0.113.99'})
        for _ in range(50):
            try:
                with urllib.request.urlopen(request,context=ssl._create_unverified_context(),timeout=2) as response:self.assertEqual(response.read(),b'127.0.0.1')
                break
            except OSError:time.sleep(.025)
        else:self.fail('isolated nginx did not become ready')

    def owned_casdoor(self,image):
        labels={bundle.LABEL:self.credentials['installation_id'],bundle.MANAGED:'true','com.docker.compose.project':'unihub-server','com.docker.compose.service':'casdoor'}
        return {'Image':'sha256:'+'0'*64,'Config':{'Image':image,'Labels':labels},'HostConfig':{'PortBindings':{'8000/tcp':[{'HostIp':'127.0.0.1','HostPort':'58000'}]}},'NetworkSettings':{'Networks':{'unihub-server':{}}},'Mounts':[{'Destination':'/conf','Type':'bind','Source':str(self.root/'shared/casdoor/conf'),'RW':False},{'Destination':'/web/build','Type':'bind','Source':str(self.release/'casdoor-web'),'RW':False},{'Destination':'/swagger','Type':'bind','Source':str(self.release/'casdoor-web/swagger'),'RW':False},{'Destination':'/logs','Type':'volume','Name':'unihub-server-casdoor-logs'}]}


if __name__=='__main__':unittest.main()
