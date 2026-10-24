#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import json
import tempfile
import unittest
from datetime import datetime,timezone

spec=importlib.util.spec_from_file_location('audit_verify',Path(__file__).with_name('verify-headless-audit.py'));m=importlib.util.module_from_spec(spec)

class AuditVerificationTests(unittest.TestCase):
 @classmethod
 def setUpClass(cls):spec.loader.exec_module(m)
 def result(self):return {'controlled_loopback_only':True,'audit_privacy_verified':False,'started_at':'2026-10-25T00:00:00Z','client_id':'product-client','registration_status':201,'foreign_origin_status':403,'invalid_password_status':401,'authorization_status':200,'token_status':200,'token_type':'Bearer','scope':'openid profile email','has_access_token':True,'has_id_token':True,'has_refresh_token':False,'replay_status':400}
 def test_identity_requires_actual_results_not_pass_label(self):
  since=m.stamp('2026-10-25T00:00:00Z');now=m.stamp('2026-10-25T00:05:00Z')
  m.identity_proof(self.result(),since,now)
  for change in [{'token_status':500},{'has_refresh_token':True},{'foreign_origin_status':200},{'replay_status':200}]:
   with self.assertRaises(m.AuditError):m.identity_proof(dict(self.result(),**change),since,now)
  with self.assertRaises(m.AuditError):m.identity_proof({'result':'PASS'},since,now)
 def test_record_proof_requires_all_flows_and_redacts_every_field(self):
  rows=[{'action':'add-user','object':'{"password":"***"}'},{'action':'login','object':'{"password":"***"}','request_uri':'/api/login?scope=openid'},{'action':'login/oauth/access_token','object':'{"code":"***","code_verifier":"***"}'}]
  m.audit_proof({'rows':rows,'count':3,'enabled_webhooks':0,'audit_providers':0},['synthetic-private'])
  for bad in [{'rows':rows[:1],'count':1,'enabled_webhooks':0,'audit_providers':0},{'rows':rows,'count':3,'enabled_webhooks':1,'audit_providers':0},{'rows':rows+[{'response':'synthetic-private'}],'count':4,'enabled_webhooks':0,'audit_providers':0}]:
   with self.assertRaises(m.AuditError):m.audit_proof(bad,['synthetic-private'])
 def test_markers_encoded_or_in_stderr_cannot_pass(self):
  for text in ['synthetic-private','synthetic%2Dprivate','{"password":"synthetic-private"}']:
   with self.assertRaises(m.AuditError):m.no_markers(text,['synthetic-private'])
 def test_log_window_is_exact_and_does_not_ignore_malformed_rows(self):
  since=m.stamp('2026-10-25T00:00:00Z')
  text='127.0.0.1 [25/Oct/2026:00:01:00 +0000] "POST /api/login HTTP/1.1" 200 20\n'
  self.assertEqual(m.nginx_lines(text,since,['no-secret'])['lines'],1)
  with self.assertRaises(m.AuditError):m.nginx_lines('unparseable new audit traffic\n',since,['no-secret'])
 def test_bounded_command_never_accepts_truncated_output(self):
  import sys
  with self.assertRaises(m.AuditError):m.command([sys.executable,'-c','print("x"*1024)'],max_bytes=100)

 def test_casdoor_file_archive_scans_every_regular_file_and_enforces_total_bound(self):
  import io,tarfile
  def archive(items):
   out=io.BytesIO()
   with tarfile.open(fileobj=out,mode='w') as tar:
    for name,value in items:
     info=tarfile.TarInfo(name);info.size=len(value);tar.addfile(info,io.BytesIO(value))
   return out.getvalue()
  raw=archive([('casdoor.log',b'ordinary log'),('nested/error.log',b'second log')])
  proof=m.casdoor_file_archive(raw,['synthetic-private'],max_bytes=100)
  self.assertEqual(proof['files_scanned'],2);self.assertEqual(proof['bytes_read'],22);self.assertTrue(proof['markers_absent'])
  with self.assertRaises(m.AuditError):m.casdoor_file_archive(raw,['synthetic-private'],max_bytes=10)
  with self.assertRaises(m.AuditError):m.casdoor_file_archive(archive([('casdoor.log',b'synthetic-private')]),['synthetic-private'])

 def test_casdoor_file_archive_refuses_symlinks_hardlinks_and_outside_paths(self):
  import io,tarfile
  for name,kind in [('../outside',tarfile.REGTYPE),('/absolute',tarfile.REGTYPE),('nested/link',tarfile.SYMTYPE),('hardlink',tarfile.LNKTYPE)]:
   out=io.BytesIO()
   with tarfile.open(fileobj=out,mode='w') as tar:
    info=tarfile.TarInfo(name);info.type=kind;info.linkname='/private/secret' if kind!=tarfile.REGTYPE else '';tar.addfile(info)
   with self.assertRaises(m.AuditError):m.casdoor_file_archive(out.getvalue(),['synthetic-private'])

if __name__=='__main__':unittest.main()
