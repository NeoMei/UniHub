#!/usr/bin/env python3
"""Offline public-policy and exact-request onboarding regressions."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

MODULE=Path(__file__).with_name('product_client.py')

class ProductClientTests(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  spec=importlib.util.spec_from_file_location('product_client',MODULE);cls.m=importlib.util.module_from_spec(spec);spec.loader.exec_module(cls.m)
 def client(self,**kw):
  c={'product_id':'new-product','client_id':'new-audience','application':'app-new-product','organization':'unihub','redirect_uris':['https://app.test/callback'],'origins':['https://app.test'],'scopes':['accounts:read','catalog:read'],'billing_products':['new-product'],'password_login':False,'registration':False};c.update(kw);return c
 def test_policy_rejects_secret_wildcards_unknown_scope_and_global_org(self):
  for change in [{'client_secret':'x'},{'origins':['https://app.test/path']},{'redirect_uris':['https://app.test/%2A']},{'scopes':['admin:all']},{'organization':'built-in'},{'application':'app-built-in'}]:
   with self.assertRaises(self.m.PolicyError):self.m.validate_client(self.client(**change))
  self.m.validate_client(self.client())
 def test_prepare_is_public_minimal_and_apply_only_named_app(self):
  with tempfile.TemporaryDirectory() as d:
   registry=Path(d)/'clients.json';plan=self.m.prepare(registry,self.client(),'cert-built-in');self.assertFalse(registry.exists());self.assertNotIn('clientSecret',json.dumps(plan));self.assertFalse(plan['client']['registration'])
   class Provider:
    def __init__(self):self.calls=[];self.app=None
    def request(self,method,path,body=None,management=True):
     self.calls.append((method,path,body,management))
     if path.startswith('/api/get-app-login?'):return {'status':'ok','data':{'name':'headless-management','organization':'unihub','clientId':'management-only','isShared':False}}
     if path=='/api/get-organization?id=admin%2Funihub':return {'status':'ok','data':{'name':'unihub','passwordType':'bcrypt','passwordObfuscatorType':'','passwordOptions':[],'masterPassword':'','masterVerificationCode':'','defaultPassword':''}}
     if path.startswith('/api/get-application?'):return {'status':'ok','data':self.app}
     if path=='/api/add-application':self.app=dict(body);return {'status':'ok','data':'Affected'}
     raise AssertionError(path)
   provider=Provider();self.m.apply(plan,provider,'management-only');self.assertEqual(json.loads(registry.read_text()),[self.client()]);self.assertEqual([x[1] for x in provider.calls if x[0]=='POST'],['/api/add-application'])
   provider.calls=[];self.m.apply(self.m.prepare(registry,self.client(),'cert-built-in'),provider,'management-only');self.assertFalse(any(x[0]=='POST' for x in provider.calls))
 def test_refuses_mismatched_existing_app_before_post(self):
  wanted=self.m.application(self.client(),'cert-built-in')
  for changes in [{'organization':'built-in'},{'clientId':'other'},{'isShared':True},{'name':'different'}]:
   existing=dict(wanted,**changes)
   with self.assertRaises(self.m.PolicyError):self.m.check_existing(existing,wanted)
 def test_registry_collision_and_atomic_change_detection(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/'registry.json';p.write_text(json.dumps([self.client(product_id='other')]))
   with self.assertRaises(self.m.PolicyError):self.m.prepare(p,self.client(),'cert-built-in')
   p.write_text('[]');plan=self.m.prepare(p,self.client(),'cert-built-in');p.write_text('[ ]')
   with self.assertRaises(self.m.PolicyError):self.m.publish_registry(plan)

 def test_update_preserves_secret_and_rejects_changed_registry(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/'registry.json';wanted=self.m.application(self.client(),'cert-built-in')
   class Provider:
    def __init__(self):self.app=dict(wanted,redirectUris=['https://old.test/callback'],clientSecret='sensitive-existing',logo='keep-logo');self.posts=[]
    def request(self,method,path,body=None,management=True):
     if path.startswith('/api/get-app-login?'):return {'status':'error','data':{'name':'management','organization':'unihub','clientId':'management-only','isShared':False}}
     if path.startswith('/api/get-organization?'):return {'status':'ok','data':{'name':'unihub','passwordType':'bcrypt','passwordObfuscatorType':'','passwordOptions':[],'masterPassword':'','masterVerificationCode':'','defaultPassword':''}}
     if path.startswith('/api/get-application?'):return {'status':'ok','data':self.app}
     if path.startswith('/api/update-application?'):
      self.posts.append((path,body));self.app=dict(body);return {'status':'ok','data':'Affected'}
     raise AssertionError(path)
   provider=Provider();self.m.apply(self.m.prepare(p,self.client(),'cert-built-in'),provider,'management-only');self.assertEqual(len(provider.posts),1);self.assertEqual(provider.posts[0][0],'/api/update-application?id=admin%2Fapp-new-product');self.assertEqual(provider.posts[0][1]['clientSecret'],'***');self.assertEqual(provider.posts[0][1]['logo'],'keep-logo')

 def test_prepare_never_overwrites_registry_with_plan(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d)/'registry.json';p.write_text('[]')
   code=self.m.main(['prepare','--registry',str(p),'--output',str(p),'--product-id','new-product','--client-id','new-client','--application','app-new-product','--redirect-uri','https://app.test/callback'])
   self.assertEqual(code,1);self.assertEqual(p.read_text(),'[]')

if __name__=='__main__':unittest.main()
