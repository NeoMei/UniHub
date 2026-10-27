#!/usr/bin/env python3
"""Generate a fresh, independently runnable product source checkout. No remote writes."""
import argparse,hashlib,json,re,shutil,subprocess,sys,tempfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
def create(output,product,module):
 if not re.fullmatch(r'[a-z][a-z0-9_-]{0,63}',product):raise ValueError('Invalid product ID')
 if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*(?:/[A-Za-z0-9][A-Za-z0-9._-]*)+',module) or '..' in module:raise ValueError('Invalid Go module')
 target=Path(output).absolute()
 if target.exists() or target.is_symlink():raise ValueError('Output must be a fresh directory')
 if not target.parent.is_dir():raise ValueError('Output parent must exist')
 source=ROOT/'packages/userkit-go';commit=subprocess.check_output(['git','log','-1','--format=%H','--','packages/userkit-go'],cwd=ROOT,text=True).strip()
 # A snapshot must have an honest immutable provenance, never a dirty source pin.
 if subprocess.check_output(['git','status','--porcelain','--','packages/userkit-go'],cwd=ROOT,text=True).strip():raise ValueError('Shared module has uncommitted changes; commit before snapshotting')
 staging=Path(tempfile.mkdtemp(prefix='.product-init-',dir=target.parent))
 try:
  for name in ['main.go','main_test.go','go.sum']:
   shutil.copyfile(ROOT/'examples/product-users'/name,staging/name)
  mod=(ROOT/'examples/product-users/go.mod').read_text().replace('module example.test/product-users','module '+module).replace('../../packages/userkit-go','./third_party/unihub-userkit')
  (staging/'go.mod').write_text(mod)
  dest=staging/'third_party/unihub-userkit';dest.mkdir(parents=True)
  files={}
  for f in sorted(source.rglob('*')):
   if not f.is_file() or f.is_symlink() or f.name.startswith('.') or f.suffix not in ['.go','.md','.mod','.sum']:continue
   relative=f.relative_to(source);(dest/relative).parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(f,dest/relative);files[str(relative)]=hashlib.sha256(f.read_bytes()).hexdigest()
  provenance={'repository':'https://github.com/NeoMei/UniHub','module':'github.com/NeoMei/UniHub/packages/userkit-go','source_commit':commit,'contract_version':'0.1.0','published':False,'license':'No license supplied by source repository; no license granted by this generator.','files':files}
  (dest/'PROVENANCE.json').write_text(json.dumps(provenance,indent=2)+'\n')
  config={'mode':'unihub','listen':'127.0.0.1:58120','public_url':'http://127.0.0.1:58120','api_url':'http://127.0.0.1:58118','central_issuer':'http://127.0.0.1:58118','oidc_issuer':'http://localhost:58000','client_id':product+'-local','client_secret':'','product_id':product,'instance_id':'default','database':'./data/product.json','access_token_type':'access-token'}
  (staging/'config.example.json').write_text(json.dumps(config,indent=2)+'\n')
  manifest=[{'product_id':product,'instance_id':'default','name':product,'entry_url':config['public_url'],'version':1,'default_roles':['member'],'roles':[{'id':'member','name':'Member','permissions':['product:use','article:create']}]}]
  (staging/'product-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
  (staging/'.gitignore').write_text('/config.json\n/data/\n/product\n')
  (staging/'README.md').write_text('''# Product user starter

Source contract 0.1.0; unpublished dependency snapshot, no source license supplied.

1. Operator registers the exact client/callback (`/auth/callback`) using UniHub's
   `scripts/product_client.py prepare/apply/readback` with `--browser-sso`,
   `--scope identity:read --scope access:read --instance-id default`.
2. Operator installs `product-manifest.json` in UniHub's manifest array and reloads
   the central service. App registration and roles are never self-enrolled here.
3. Copy `config.example.json` to `config.json`, set the exact origins/audience and
   private backend client secret if required, then `chmod 600 config.json`.
4. `go test ./...`, then `PRODUCT_CONFIG=./config.json go run .`.
5. Open the configured public URL. The browser receives only an HttpOnly random
   session cookie, current profile/permissions and a CSRF token.

UniHub mode uses shared Browser + Client + Service. Every request rechecks central
access; product resource policy can only restrict it. The provisioner atomically
commits stable issuer+subject identity, extension and notebook defaults in one
private JSON snapshot. One process per database: a lock file refuses concurrent
process ownership. After a crash, verify the owning process is dead before
removing the exact database `.lock` file. For production replace the store seam
with your SQL transaction and unique identity constraint; use durable encrypted
session/proof storage and a shared OIDC transaction store/sticky routing.

Standalone mode: set `mode` to `standalone`, configure a product-owned standard
OIDC issuer/client/callback. Shared Browser verifies signed RS256 ID/access tokens;
only the resulting server-held proof enters the standalone Provider. It grants
local member defaults through the same Service/provisioner/resource policy. There
is no password implementation, upstream live-revocation hook or central roles in
that mode. Sessions/proofs are in memory until verified expiry or local logout;
a restart requires login again. Logout terminates this product session; the issuer
SSO session remains. HTTP is allowed only for loopback development.

Upgrade: regenerate into a NEW directory from a reviewed UniHub commit, compare
`third_party/unihub-userkit/PROVENANCE.json` sorted source hashes and review public
API changes, then migrate business changes and rerun tests. Never overwrite a
working product or replace the snapshot with a machine-specific sibling path.
''')
  # mkdir is the no-clobber claim. The target remains absent until all content is ready.
  target.mkdir(mode=0o700)
  try:
   for entry in staging.iterdir():shutil.move(str(entry),target/entry.name)
  except BaseException:
   shutil.rmtree(target);raise
 finally:shutil.rmtree(staging,ignore_errors=True)
def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--output',required=True);p.add_argument('--product-id',required=True);p.add_argument('--module',required=True);a=p.parse_args()
 try:create(a.output,a.product_id,a.module)
 except (ValueError,OSError,subprocess.SubprocessError) as e:print('Product initialization failed: '+(str(e) if isinstance(e,ValueError) else 'source or output unavailable'),file=sys.stderr);return 1
 print('Fresh product source generated; configure privately and enroll client explicitly.');return 0
if __name__=='__main__':raise SystemExit(main())
