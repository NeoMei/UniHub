#!/usr/bin/env python3
"""Build curated UniHub release assets without runtime configuration or secrets."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
BLOCKED_PARTS = {'.local', '.git', 'node_modules', '__pycache__'}
BLOCKED_NAMES = {'credentials.json', 'runtime.env', 'compose.env', 'app.conf', 'init_data.json', 'products.approved.json'}
IDENTITY_FILES = {'server', 'Dockerfile', '.dockerignore', 'LICENSE', 'NOTICE', 'source-manifest.json'}

class ReleaseError(Exception):
    pass

def command(args, *, env=None):
    result = subprocess.run(args, cwd=ROOT, env=env, capture_output=True, text=True)
    if result.returncode:
        raise ReleaseError(f'{args[0]} {args[1] if len(args)>1 else ""} failed; command output withheld')
    return result.stdout.strip()

def sha256(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for part in iter(lambda: stream.read(1024*1024), b''):
            h.update(part)
    return h.hexdigest()

def safe_name(name):
    path = PurePosixPath(name)
    if not name or path.is_absolute() or '..' in path.parts or '\\' in name or any(p in BLOCKED_PARTS for p in path.parts):
        raise ReleaseError('Archive path is unsafe or private')
    if path.name in BLOCKED_NAMES or (path.name.startswith('.env') and path.name != '.env.example') or path.suffix in {'.pem', '.key', '.pyc'}:
        raise ReleaseError('Runtime configuration or private file is forbidden in release assets')

def regular(path):
    path = Path(path).absolute()
    if any(p.is_symlink() for p in [path, *path.parents]) or not path.is_file() or path.stat().st_nlink != 1:
        raise ReleaseError('Release inputs must be regular, unlinked files without symlink ancestors')
    return path

def add(entries, name, source):
    safe_name(name)
    if name in entries:
        raise ReleaseError('Duplicate archive path')
    entries[name] = regular(source)

def add_tree(entries, source, prefix):
    source = Path(source).absolute()
    if not source.is_dir() or any(p.is_symlink() for p in [source, *source.parents]):
        raise ReleaseError('Public asset tree must be a real directory')
    for path in sorted(source.rglob('*')):
        if path.is_symlink():
            raise ReleaseError('Linked public asset is forbidden')
        if path.is_file():
            add(entries, str(PurePosixPath(prefix) / path.relative_to(source).as_posix()), path)
        elif not path.is_dir():
            raise ReleaseError('Special public asset is forbidden')

def tracked(entries, selections):
    names = command(['git', 'ls-files', '-z', '--', *selections]).split('\0')
    for name in filter(None, names):
        add(entries, name, ROOT/name)

def archive(path, entries, prefix, epoch):
    safe_name(prefix)
    with path.open('xb') as raw, gzip.GzipFile(filename='', fileobj=raw, mode='wb', mtime=epoch, compresslevel=9) as gz:
        with tarfile.open(fileobj=gz, mode='w', format=tarfile.GNU_FORMAT) as output:
            for name, source in sorted(entries.items()):
                info = tarfile.TarInfo(str(PurePosixPath(prefix)/name))
                info.size = source.stat().st_size
                info.mtime = epoch
                info.uid = info.gid = 0
                info.uname = info.gname = ''
                info.mode = 0o755 if source.stat().st_mode & 0o111 else 0o644
                with source.open('rb') as stream:
                    output.addfile(info, stream)
    # Read back the finished archive, including content hashes, rather than
    # trusting the copy list or only checking suffixes in source paths.
    with tarfile.open(path, 'r:gz') as check:
        seen = set()
        for item in check:
            safe_name(item.name)
            relative = str(PurePosixPath(item.name).relative_to(prefix))
            if not item.isfile() or relative not in entries or relative in seen:
                raise ReleaseError('Built archive shape differs from the curated file list')
            stream = check.extractfile(item)
            h = hashlib.sha256()
            for block in iter(lambda: stream.read(1024*1024), b''):
                h.update(block)
            if h.hexdigest() != sha256(entries[relative]):
                raise ReleaseError('Built archive content differs from its source')
            seen.add(relative)
        if seen != set(entries):
            raise ReleaseError('Built archive is incomplete')

def versions(version):
    if not re.fullmatch(r'(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?', version):
        raise ReleaseError('Version must be an explicit semantic version without a v prefix')
    if (ROOT/'VERSION').read_text().strip() != version:
        raise ReleaseError('VERSION differs from requested release')
    result = {name: json.loads((ROOT/name).read_text())['version'] for name in ['web/package.json', 'packages/sdk/package.json']}
    text = (ROOT/'api/openapi.yaml').read_text()
    match = re.search(r'(?m)^  "?version"?:\s*"?([^"\s]+)"?\s*$', text)
    if not match:
        raise ReleaseError('OpenAPI version is unavailable')
    result['api/openapi.yaml'] = match[1]
    if any(value != version for value in result.values()):
        raise ReleaseError('Web, SDK and API versions must match VERSION')
    return result

def identity_context(source):
    source = Path(source).absolute()
    if not source.is_dir() or {p.name for p in source.iterdir()} != IDENTITY_FILES:
        raise ReleaseError('Identity context must contain only the reviewed binary, Dockerfile, licenses and manifest')
    for path in source.iterdir():
        regular(path)
    manifest = json.loads((source/'source-manifest.json').read_text())
    lock = json.loads((ROOT/'deploy/identity/source-lock.json').read_text())
    if (manifest.get('source') != lock or manifest.get('binary_target') != 'linux/amd64'
            or manifest.get('privacy_baseline_reproduced') is not True or manifest.get('privacy_patched_source_functions_passed') is not True
            or manifest.get('binary_sha256') != sha256(source/'server') or manifest.get('patch_sha256') != sha256(ROOT/'deploy/identity/audit-redaction.patch')
            or (source/'Dockerfile').read_bytes() != (ROOT/'deploy/identity/Dockerfile').read_bytes()
            or sha256(source/'LICENSE') != lock['files']['LICENSE'] or not (source/'NOTICE').stat().st_size):
        raise ReleaseError('Identity binary context provenance or privacy test lock differs from reviewed sources')
    with (source/'server').open('rb') as stream:
        header = stream.read(20)
    if header[:6] != b'\x7fELF\x02\x01' or header[18:20] != b'\x3e\x00':
        raise ReleaseError('Identity backend must be a Linux amd64 ELF binary')
    return manifest

def build(args):
    version = args.version or (ROOT/'VERSION').read_text().strip()
    package_versions = versions(version)
    if command(['git', 'status', '--porcelain']):
        raise ReleaseError('Commit the complete release source before packaging; worktree must be clean')
    commit = command(['git', 'rev-parse', 'HEAD'])
    epoch = int(command(['git', 'show', '-s', '--format=%ct', 'HEAD']))
    output = Path(args.output or ROOT/f'.local/releases/v{version}').absolute()
    if any(p.is_symlink() for p in [output, *output.parents]) or output.exists():
        raise ReleaseError('Output must be a new directory without symlink ancestors')
    for path in (ROOT/'web').glob('.env*'):
        if path.name != '.env.example':
            raise ReleaseError('Web dotenv input is forbidden in reproducible release builds')
    context_manifest = identity_context(args.identity_artifacts) if args.identity_artifacts else None
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.unihub-release-', dir=output.parent) as temporary:
        stage = Path(temporary).resolve()
        env = {k:v for k,v in os.environ.items() if not k.startswith('VITE_')}
        env.update({'GOOS':'linux', 'GOARCH':'amd64', 'CGO_ENABLED':'0', 'SOURCE_DATE_EPOCH':str(epoch), 'NODE_ENV':'production'})
        for target, package in [('unihub', './cmd/unihub'), ('unihub-identity-acceptance', './cmd/unihub-identity-acceptance')]:
            print('Building '+target+' for linux/amd64', flush=True)
            command(['go', 'build', '-mod=readonly', '-trimpath', '-buildvcs=true', '-ldflags=-s -w', '-o', str(stage/target), package], env=env)
        print('Building optional console assets', flush=True)
        command(['npm', '--prefix', 'web', 'run', 'build'], env=env)
        runtime = {}
        tracked(runtime, ['deploy/server', 'deploy/identity', 'docs', 'branding', 'README.md', 'CONTEXT.md', 'VERSION', 'CHANGELOG.md',
                          'config/examples/product-user-manifests.json', 'scripts/headless_e2e.py', 'scripts/product_client.py', 'scripts/product_billing.py', 'scripts/prepare-branding.py', 'scripts/package-release.py', 'examples/billing', 'integrations/llmrouter'])
        add(runtime, 'bin/unihub', stage/'unihub')
        add(runtime, 'bin/unihub-identity-acceptance', stage/'unihub-identity-acceptance')
        add_tree(runtime, ROOT/'web/dist', 'web/dist')
        if args.casdoor_web:
            frontend = stage/'casdoor-web'
            public = {}
            add_tree(public, args.casdoor_web, 'casdoor-web')
            for name, source in public.items():
                target = stage/name; target.parent.mkdir(parents=True, exist_ok=True); shutil.copy2(source, target)
            command(['python3', 'deploy/server/prepare-identity.py', '--web', str(frontend)])
            for name in ['index.html', 'swagger/index.html', 'assets/unihub/unihub-logo-v1.png', 'assets/unihub/unihub-icon-v1.png', 'unihub-identity-manifest.json']:
                regular(frontend/name)
            if sha256(regular(frontend/'assets/unihub/LICENSE-Casdoor.txt')) != sha256(ROOT/'branding/casdoor/LICENSE'):
                raise ReleaseError('Prepared frontend must retain the reviewed upstream license')
            add_tree(runtime, frontend, 'casdoor-web')
            add(runtime, 'third_party/casdoor/LICENSE', ROOT/'branding/casdoor/LICENSE')
            add(runtime, 'third_party/casdoor/UPSTREAM.txt', ROOT/'branding/casdoor/UPSTREAM.txt')
            notice = stage/'casdoor-frontend-NOTICE'
            notice.write_text('Casdoor frontend v4.23.0\nCopyright The Casdoor Authors.\nLicensed under Apache-2.0; see LICENSE.\n\n'
                'UniHub modifies user-facing branding and the exact same-origin HTTPS /auth/callback routing exception. '
                'Authentication and token verification remain enforced. Original copyright comments and bundled dependency licenses are retained.\n'
                'Upstream: https://github.com/casdoor/casdoor/tree/v4.23.0\n')
            add(runtime, 'third_party/casdoor/NOTICE', notice)
        manifest = {'schema_version':1, 'product':'UniHub', 'version':version, 'commit':commit, 'source_clean':True, 'source_date_epoch':epoch,
            'target':'linux/amd64', 'go_version':command(['go','version']), 'node_version':command(['node','--version']), 'package_versions':package_versions,
            'payment_mode':'sandbox_or_unconfigured', 'casdoor_frontend_included':bool(args.casdoor_web), 'identity_backend_asset_included':bool(context_manifest),
            'sdk_distribution':'private workspace TypeScript source; not an npm publication',
            'userkit_distribution':'curated Go module and starter source; no nested-module publication; initializer requires a reviewed Git checkout',
            'files':{name:sha256(source) for name,source in sorted(runtime.items())}}
        source_manifest = stage/'release-manifest.json'; source_manifest.write_text(json.dumps(manifest, indent=2, sort_keys=True)+'\n')
        add(runtime, 'release-manifest.json', source_manifest)
        generated = stage/'output'; generated.mkdir()
        archive(generated/f'unihub-{version}-linux-amd64.tar.gz', runtime, f'unihub-{version}-linux-amd64', epoch)
        sdk = {}; tracked(sdk, ['packages/sdk/src', 'packages/sdk/package.json', 'docs/headless-product-api.md', 'docs/composable-billing.md', 'docs/composable-billing-validation-2026-10-25.md', 'docs/native-metering.md', 'examples/billing', 'VERSION', 'CHANGELOG.md'])
        archive(generated/f'unihub-sdk-{version}-source.tar.gz', sdk, f'unihub-sdk-{version}', epoch)
        userkit = {}; tracked(userkit, ['packages/userkit-go', 'examples/product-users',
            'scripts/product_user_init.py', 'scripts/product_client.py', 'scripts/test-product-user-init.py', 'scripts/test-product-client.py',
            'config/examples/product-user-manifests.json', 'docs/product-user-api.md', 'docs/headless-product-api.md',
            'docs/integrations/product-user-foundation.md', 'docs/integrations/product-user-foundation-acceptance-2026-10-27.md',
            'VERSION', 'CHANGELOG.md'])
        archive(generated/f'unihub-userkit-{version}-source.tar.gz', userkit, f'unihub-userkit-{version}', epoch)
        if context_manifest:
            context = {name:regular(Path(args.identity_artifacts)/name) for name in sorted(IDENTITY_FILES)}
            add(context, 'source-lock.json', ROOT/'deploy/identity/source-lock.json')
            add(context, 'audit-redaction.patch', ROOT/'deploy/identity/audit-redaction.patch')
            archive(generated/f'unihub-casdoor-audit-{version}-linux-amd64.tar.gz', context, f'unihub-casdoor-audit-{version}', epoch)
        shutil.copyfile(ROOT/'api/openapi.yaml', generated/f'unihub-{version}-openapi.yaml')
        shutil.copyfile(source_manifest, generated/'release-manifest.json')
        if command(['git', 'status', '--porcelain']) or command(['git','rev-parse','HEAD']) != commit:
            raise ReleaseError('Release source changed during build; no package published')
        sums = ''.join(f'{sha256(p)}  {p.name}\n' for p in sorted(generated.iterdir()))
        (generated/'SHA256SUMS').write_text(sums)
        os.replace(generated, output)
    print('Release assets verified: '+str(output))

def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', help='Version matching root VERSION, without v')
    parser.add_argument('--output', help='New output directory; default .local/releases/v<VERSION>')
    parser.add_argument('--casdoor-web', type=Path, help='Explicit trusted prepared public Casdoor frontend tree')
    parser.add_argument('--identity-artifacts', type=Path, help='Explicit reviewed custom backend image-context directory')
    args = parser.parse_args(argv)
    try:
        build(args)
    except (OSError, ValueError, KeyError, ReleaseError):
        message = str(sys.exc_info()[1]) if isinstance(sys.exc_info()[1], ReleaseError) else 'Release packaging failed; inspect local build inputs'
        print(message, file=sys.stderr)
        return 1
    return 0

if __name__ == '__main__':
    raise SystemExit(main())
