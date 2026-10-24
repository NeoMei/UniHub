#!/usr/bin/env python3
"""Read-only real-service headless privacy acceptance. Never prints private data.

Scans complete bounded records/log windows and writes only aggregate evidence.
Does not enable the public gate, restart services, run signup, or alter policy.
"""
import argparse
from datetime import datetime,timedelta,timezone
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path,PurePosixPath
import re
import subprocess
import sys
import tempfile
import tarfile
import urllib.parse

MAX_BYTES=64*1024*1024
MAX_JSON=4*1024*1024
class AuditError(Exception):pass

def stamp(value):
 try:
  if not isinstance(value,str) or len(value)>64:raise ValueError()
  d=datetime.fromisoformat(value.replace('Z','+00:00'))
  if d.tzinfo is None:raise ValueError()
  return d.astimezone(timezone.utc)
 except (ValueError,TypeError):raise AuditError('An exact RFC3339 timestamp is required') from None

def bounded_json(path,limit=MAX_JSON):
 path=Path(path)
 if path.is_symlink() or not path.is_file() or path.stat().st_size>limit:raise AuditError('Evidence input must be a bounded regular file')
 try:return json.loads(path.read_text())
 except (ValueError,OSError):raise AuditError('Evidence JSON is invalid') from None

def command_bytes(args,*,input=None,max_bytes=MAX_BYTES,clean_stderr=False):
 # Anonymous private files avoid holding arbitrary provider/log output in RAM;
 # exceeding the bound fails acceptance, never silently truncates the scan.
 with tempfile.TemporaryFile() as out,tempfile.TemporaryFile() as err:
  try:result=subprocess.run(args,input=input.encode() if isinstance(input,str) else input,stdout=out,stderr=err,timeout=90,check=False)
  except (subprocess.SubprocessError,OSError):raise AuditError('Read-only inspection failed; diagnostics withheld') from None
  sizes=[stream.tell() for stream in [out,err]]
  if result.returncode or sum(sizes)>max_bytes or (clean_stderr and sizes[1]):raise AuditError('Inspection failed or complete output exceeds the scan bound; nothing accepted')
  out.seek(0);err.seek(0)
  return out.read()+err.read()

def command(args,*,input=None,max_bytes=MAX_BYTES):
 try:return command_bytes(args,input=input,max_bytes=max_bytes).decode('utf8')
 except UnicodeError:raise AuditError('Inspection output is not complete UTF-8 text') from None

def casdoor_file_archive(raw,markers,*,max_bytes=MAX_BYTES):
 """Inspect a docker-cp tar stream in memory; never extract archive paths."""
 total=0;count=0;entries=0
 try:
  with tarfile.open(fileobj=io.BytesIO(raw),mode='r:') as archive:
   for member in archive:
    entries+=1;path=PurePosixPath(member.name)
    if entries>10000 or path.is_absolute() or '..' in path.parts or '\\' in member.name or not member.name or member.issym() or member.islnk():raise AuditError('Casdoor log archive contains an unsafe or linked path')
    if member.isdir():continue
    if not member.isfile() or member.size<0:raise AuditError('Casdoor log archive contains a non-regular file')
    total+=member.size
    if total>max_bytes:raise AuditError('Complete Casdoor file logs exceed 64 MiB scan bound; nothing accepted')
    stream=archive.extractfile(member)
    if stream is None:raise AuditError('Casdoor log file cannot be read completely')
    content=stream.read(member.size+1)
    if len(content)!=member.size:raise AuditError('Casdoor log archive contains an incomplete file')
    try:text=content.decode('utf8')
    except UnicodeError:raise AuditError('Casdoor file log is not complete UTF-8 text') from None
    no_markers(text,markers);count+=1
 except (tarfile.TarError,ValueError,OSError):raise AuditError('Casdoor log archive could not be scanned completely') from None
 if count==0:raise AuditError('Configured Casdoor file log sink contains no regular files to verify')
 return {'files_scanned':count,'bytes_read':total,'markers_absent':True,'complete_files':True,'scope':'all_regular_files_under_/logs','time_filter':'entire_files_unique_probe_markers'}

def casdoor_file_proof(markers):
 # Check the fixed source directory before daemon-side copy. Docker cp does not
 # follow symlinks without -L; all archive entries are independently validated.
 command(['docker','exec','unihub-server-casdoor','sh','-c','test -d /logs && test ! -L /logs'],max_bytes=4096)
 archive=command_bytes(['docker','cp','unihub-server-casdoor:/logs/.','-'],max_bytes=MAX_BYTES+16*1024*1024,clean_stderr=True)
 return casdoor_file_archive(archive,markers)

def no_markers(text,markers):
 # Scan raw, JSON escaped and decoded query representations. Values themselves
 # never leave process memory or appear in error messages/public evidence.
 variants=[text,urllib.parse.unquote(text),urllib.parse.unquote_plus(text)]
 for value in markers:
  candidates=[value,json.dumps(value,ensure_ascii=False)[1:-1],urllib.parse.quote(value,safe=''),urllib.parse.quote_plus(value,safe='')]
  if any(candidate in item for candidate in candidates for item in variants):raise AuditError('Private acceptance marker found in a real audit/log sink; activation refused')

def identity_proof(result,since,now):
 expected={'controlled_loopback_only':True,'audit_privacy_verified':False,'registration_status':201,'foreign_origin_status':403,'invalid_password_status':401,'authorization_status':200,'token_status':200,'token_type':'Bearer','scope':'openid profile email','has_access_token':True,'has_id_token':True,'has_refresh_token':False,'replay_status':400}
 if not isinstance(result,dict) or any(type(result.get(k)) is not type(v) or result.get(k)!=v for k,v in expected.items()):raise AuditError('Controlled identity registration/login/PKCE/replay results are incomplete or failed')
 started=stamp(result.get('started_at'))
 if not since<=started<=now or now-started>timedelta(hours=24):raise AuditError('Identity probe is outside the inspected fresh time window')
 if not isinstance(result.get('client_id'),str) or not result['client_id'] or len(result['client_id'])>256:raise AuditError('Identity probe client binding is absent')
 return {**expected,'started_at':started.isoformat(),'client_id':result['client_id']}

def audit_proof(snapshot,markers):
 if not isinstance(snapshot,dict) or not isinstance(snapshot.get('rows'),list) or type(snapshot.get('count')) is not int or snapshot['count']!=len(snapshot['rows']) or snapshot['count']<1:raise AuditError('Real audit rows are missing, incomplete or truncated')
 if any(type(snapshot.get(k)) is not int or snapshot[k]!=0 for k in ['enabled_webhooks','audit_providers']):raise AuditError('Configured webhook/Audit sinks require actual sink acceptance; this verifier cannot approve them')
 no_markers(json.dumps(snapshot,ensure_ascii=False),markers)
 counts={'registration':0,'password_login':0,'token_exchange':0}
 for row in snapshot['rows']:
  if not isinstance(row,dict):raise AuditError('Audit record shape is invalid')
  action=str(row.get('action',''));uri=str(row.get('request_uri',''))
  try:body=json.loads(row.get('object') or '{}')
  except (ValueError,TypeError):body={}
  if not isinstance(body,dict):body={}
  if action=='add-user' or urllib.parse.urlsplit(uri).path=='/api/add-user':
   if body.get('password')=='***':counts['registration']+=1
  elif action=='login/oauth/access_token' or urllib.parse.urlsplit(uri).path=='/api/login/oauth/access_token':
   if body.get('code')=='***' and body.get('code_verifier')=='***':counts['token_exchange']+=1
  elif action=='login' or urllib.parse.urlsplit(uri).path=='/api/login':
   if body.get('password')=='***':counts['password_login']+=1
 if not all(counts.values()):raise AuditError('Actual masked add-user, password login and code/verifier token audit objects were not all observed')
 return {'records_scanned':snapshot['count'],'masked_flow_records':counts,'enabled_webhooks':0,'audit_providers':0,'markers_absent':True,'complete_window':True}

def nginx_lines(text,since,markers):
 selected=[]
 for line in text.splitlines():
  if not line:continue
  match=re.search(r'\[([^\]]+)\]',line)
  try:when=datetime.strptime(match.group(1),'%d/%b/%Y:%H:%M:%S %z').astimezone(timezone.utc) if match else None
  except ValueError:when=None
  if when is None:raise AuditError('Nginx log window contains an unparseable line; completeness cannot be proven')
  if when>=since:selected.append(line)
 no_markers('\n'.join(selected),markers)
 return {'lines':len(selected),'registration':sum(' /api/add-user ' in x for x in selected),'password_login':sum(' /api/login ' in x for x in selected),'token_exchange':sum(' /api/login/oauth/access_token ' in x for x in selected),'markers_absent':True,'complete_window':True}

def nginx_proof(since,markers,folder=Path('/var/log/nginx')):
 files=sorted(folder.glob('unihub-access.log*'))
 if not files:raise AuditError('Managed Nginx access log is unavailable')
 total=0;aggregate={'lines':0,'registration':0,'password_login':0,'token_exchange':0,'markers_absent':True,'complete_window':True};scanned=0
 for path in files:
  if path.is_symlink() or not path.is_file():raise AuditError('Nginx log must be a regular file')
  opener=gzip.open if path.suffix=='.gz' else open
  try:
   with opener(path,'rb') as stream:raw=stream.read(MAX_BYTES-total+1)
   total+=len(raw)
   if total>MAX_BYTES:raise AuditError('Complete Nginx log scan exceeds bound; rotate/archive unrelated logs then rerun with verifiable window')
   proof=nginx_lines(raw.decode('utf8'),since,markers)
  except (OSError,UnicodeError):raise AuditError('Nginx log could not be read completely') from None
  for k in ['lines','registration','password_login','token_exchange']:aggregate[k]+=proof[k]
  scanned+=1
 if not all(aggregate[k]>0 for k in ['registration','password_login','token_exchange']):raise AuditError('Real Nginx add-user/login/token traffic was not observed in the inspected window')
 aggregate.update({'files_scanned':scanned,'bytes_read':total});return aggregate

def source_proof(release,pin,bundle):
 path=release/'identity/image-context/source-manifest.json';bundle.no_symlink(path)
 manifest=bounded_json(path);source=manifest.get('source',{})
 if (bundle.file_sha256(path)!=pin.get('source_manifest_sha256') or manifest.get('binary_sha256')!=pin['backend_sha256'] or source.get('commit')!=bundle.AUDITED_COMMIT or source.get('archive_sha256')!=bundle.AUDITED_ARCHIVE_SHA or source.get('patched_record_sha256')!=bundle.AUDITED_RECORD_SHA or source.get('runtime_image')!=bundle.IMAGES['casdoor'] or manifest.get('privacy_baseline_reproduced') is not True or manifest.get('privacy_patched_source_functions_passed') is not True):raise AuditError('Reviewed source-function test manifest is absent or differs from running backend pin')
 return {'manifest_sha256':bundle.file_sha256(path),'source_commit':source['commit'],'baseline_reproduced':True,'patched_source_functions_passed':True,'binary_sha256':manifest['binary_sha256']}

def sql_snapshot(since,bundle):
 literal=bundle.literal(since.isoformat())
 sql="BEGIN TRANSACTION READ ONLY;\nSELECT jsonb_build_object('count',count(*),'rows',COALESCE(jsonb_agg(to_jsonb(r)),'[]'::jsonb),'enabled_webhooks',(SELECT count(*) FROM webhook WHERE is_enabled=true),'audit_providers',(SELECT count(*) FROM provider WHERE category='Audit'))::text FROM record r WHERE created_time::timestamptz >= "+literal+"::timestamptz;\nCOMMIT;\n"
 raw=command(['docker','exec','-i','unihub-server-postgres','psql','-X','-U','unihub_admin','-d','casdoor','-v','ON_ERROR_STOP=1','-Atq'],input=sql)
 try:return json.loads(raw)
 except ValueError:raise AuditError('Audit read-only query did not return one complete snapshot') from None

def verify(root,release,markers_path,result_path,since,output):
 root,release,output=map(lambda p:Path(p).absolute(),[root,release,output]);now=datetime.now(timezone.utc)
 if since>now or now-since>timedelta(hours=24):raise AuditError('Audit acceptance window must start within the past 24 hours')
 spec=importlib.util.spec_from_file_location('unihub_audit_bootstrap',release/'deploy/server/bootstrap.py');bundle=importlib.util.module_from_spec(spec);spec.loader.exec_module(bundle)
 for path in [root,release,output,Path(markers_path).absolute(),Path(result_path).absolute()]:bundle.no_symlink(path)
 credentials=bounded_json(root/'shared/credentials.json');pin=bundle.identity_pin(root)
 if not pin or not pin.get('transition_complete') or pin.get('installation_id')!=credentials.get('installation_id'):raise AuditError('Current managed patched identity image is not ready for verification')
 inventory=bundle.inventory_resources();bundle.validate_resources(inventory,credentials,root)
 container=inventory.get('container:unihub-server-casdoor',{})
 if not container.get('State',{}).get('Running') or container.get('Image')!=pin['image_id']:raise AuditError('Running managed Casdoor image does not match registered backend')
 observed=command(['docker','exec','unihub-server-casdoor','sha256sum','/server'],max_bytes=4096).split()
 if not observed or observed[0]!=pin['backend_sha256']:raise AuditError('Running Casdoor binary checksum mismatch')
 private=bounded_json(markers_path);markers=private.get('audit_markers') if isinstance(private,dict) else None
 if not isinstance(markers,list) or len(markers)<7 or len(markers)>100 or any(not isinstance(x,str) or len(x)<8 or len(x)>16384 for x in markers):raise AuditError('Private synthetic password/token/PKCE audit markers are incomplete')
 markers=list(set(markers))
 if len(markers)<7:raise AuditError('Distinct private acceptance markers are incomplete')
 identity=identity_proof(bounded_json(result_path),since,now)
 policy=root/'headless-config/products.draft.json';bundle.no_symlink(policy);clients=bounded_json(policy)
 if not isinstance(clients,list) or not any(isinstance(c,dict) and c.get('client_id')==identity['client_id'] and c.get('registration') is True and c.get('password_login') is True for c in clients):raise AuditError('Controlled probe client is not enabled in the reviewed draft policy')
 source=source_proof(release,pin,bundle);audit=audit_proof(sql_snapshot(since,bundle),markers)
 config=command(['nginx','-T']);no_markers(config,markers)
 format_match=re.search(r'log_format\s+unihub_server\s+([^;]+);',config)
 if not format_match or '$uri' not in format_match.group(1) or '$request' in format_match.group(1).replace('$request_method','') or 'error_log /dev/null crit;' not in config:raise AuditError('Active Nginx does not prove URI-only access and disabled sensitive error logging')
 proxy=nginx_proof(since,markers)
 journals=command(['journalctl','--unit','unihub.service','--since',since.isoformat(),'--no-pager','--quiet','--output','json']);no_markers(journals,markers)
 try:journal_count=sum(1 for line in journals.splitlines() if json.loads(line))
 except ValueError:raise AuditError('Journald output is incomplete or invalid') from None
 logs=command(['docker','logs','--since',since.isoformat(),'--timestamps','unihub-server-casdoor']);no_markers(logs,markers)
 file_logs=casdoor_file_proof(markers)
 checks={name:True for name in bundle.PRIVACY_CHECKS}
 aggregate={'schema_version':1,'since':since.isoformat(),'as_of':now.isoformat(),'identity_probe':identity,'audit':audit,'proxy':proxy,'journald':{'lines_scanned':journal_count,'markers_absent':True,'complete_window':True},'casdoor_docker_logs':{'lines_scanned':len(logs.splitlines()),'markers_absent':True,'complete_window':True},'casdoor_file_logs':file_logs,'source_function_test':source,'sink_modes':{'webhooks':'none_configured+source_function_test','audit_providers':'none_configured+source_function_test'},'private_markers_checked':len(markers),'raw_sink_data_saved':False}
 if output in [Path(markers_path).absolute(),Path(result_path).absolute(),root/'shared/credentials.json',policy]:raise AuditError('Evidence output cannot replace private inputs or runtime policy')
 output.parent.mkdir(parents=True,exist_ok=True);evidence=output.with_name(output.stem+'-aggregate.json');bundle.no_symlink(evidence)
 bundle.atomic_write(evidence,json.dumps(aggregate,indent=2)+'\n',0o600)
 report={'schema_version':1,'installation_id':credentials['installation_id'],'image_id':pin['image_id'],'backend_sha256':pin['backend_sha256'],'policy_sha256':bundle.file_sha256(policy),'verified_at':datetime.now(timezone.utc).isoformat(),'checks':checks,'sink_modes':aggregate['sink_modes'],'evidence_files':[{'path':evidence.name,'sha256':bundle.file_sha256(evidence)}]}
 bundle.validate_privacy_evidence(report,credentials,pin,output.parent);bundle.atomic_write(output,json.dumps(report,indent=2)+'\n',0o600);return report

def main(argv=None):
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=Path('/opt/unihub'));parser.add_argument('--release',type=Path,required=True);parser.add_argument('--private-markers',type=Path,required=True);parser.add_argument('--identity-result',type=Path,required=True);parser.add_argument('--since',required=True);parser.add_argument('--output',type=Path,required=True);args=parser.parse_args(argv)
 try:verify(args.root,args.release,args.private_markers,args.identity_result,stamp(args.since),args.output)
 except AuditError as error:
  print('Headless privacy verification refused: '+str(error)+'. Public gate remains unchanged.',file=sys.stderr);return 1
 except Exception:
  # No arbitrary exception/provider/SQL/log content can reach stdout/stderr.
  print('Headless privacy verification failed; no gate enabled. Inspect private inputs and real sinks without exposing credentials.',file=sys.stderr);return 1
 print('Real-service headless privacy evidence verified and aggregate report saved. Public gate remains unchanged.');return 0
if __name__=='__main__':raise SystemExit(main())
