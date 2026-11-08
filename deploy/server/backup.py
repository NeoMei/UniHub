#!/usr/bin/env python3
"""Create a restricted, online full-cluster physical backup with included WAL."""
import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import shlex
import shutil
import re
import sys
import tarfile

from bootstrap import DeploymentError, IMAGES, atomic_write, identity_pin, inventory_resources, no_symlink, private_directory, protected_payment_file, read_env, env_text, validate_payment_runtime, validate_resources


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def systemd_snapshot():
    """Read effective service definitions. No daemon reload or service action."""
    if shutil.which('systemctl') is None:
        return None
    def command(args):
        result = subprocess.run(['systemctl', *args, '--no-pager'], text=True, capture_output=True)
        if result.returncode:
            raise DeploymentError('Cannot read effective UniHub service configuration; backup remains incomplete.')
        return result.stdout
    show = command(['show', 'unihub.service', '--property=LoadState,FragmentPath,DropInPaths,Environment,EnvironmentFiles,UnsetEnvironment,PassEnvironment'])
    properties = {}
    for line in show.splitlines():
        if '=' not in line:
            continue
        name, value = line.split('=', 1)
        # systemctl emits one EnvironmentFiles record per file on some versions.
        # Keep every record in order so later files retain override precedence.
        if name in properties and name in {'EnvironmentFiles', 'Environment', 'DropInPaths', 'UnsetEnvironment', 'PassEnvironment'}:
            properties[name] += ' ' + value
        elif name in properties:
            raise DeploymentError('Repeated singleton systemd property; backup remains incomplete.')
        else:
            properties[name] = value
    if properties.get('LoadState') == 'not-found':
        return None
    if properties.get('LoadState') != 'loaded':
        raise DeploymentError('Effective UniHub service configuration is unavailable.')
    return {'properties': properties, 'show': show, 'unit': command(['cat', 'unihub.service'])}


def recovery_configuration(root, snapshot):
    runtime = read_env(root / 'shared/runtime.env')
    effective = dict(runtime) if snapshot is None else {}
    sources = []
    def source(raw):
        path = Path(raw)
        if not path.is_absolute():
            raise DeploymentError('Recovery configuration requires absolute host paths.')
        no_symlink(path)
        info = path.stat()
        if not path.is_file() or info.st_nlink != 1 or info.st_size > 1 << 20:
            raise DeploymentError('Recovery configuration requires bounded, unlinked regular files.')
        if path not in sources:
            sources.append(path)
    if snapshot is not None:
        properties = snapshot['properties']
        if properties.get('PassEnvironment'):
            raise DeploymentError('PassEnvironment requires an explicit private recovery snapshot before backup.')
        for raw in shlex.split(properties.get('FragmentPath', '')) + shlex.split(properties.get('DropInPaths', '')):
            source(raw)
        # systemd applies Environment first, then EnvironmentFile entries in
        # their listed order. Preserve the resulting values privately.
        effective.update(dict(item.split('=', 1) for item in shlex.split(properties.get('Environment', ''))))
        raw_files = properties.get('EnvironmentFiles', '')
        files = re.findall(r'(\S+) \(ignore_errors=(yes|no)\)', raw_files)
        if re.sub(r'\S+ \(ignore_errors=(?:yes|no)\)', '', raw_files).strip():
            raise DeploymentError('Unsupported systemd EnvironmentFile format; collect recovery configuration explicitly.')
        for raw, optional in files:
            path = Path(raw)
            if not path.exists() and optional == 'yes':
                continue
            source(raw)
            effective.update(read_env(path))
        for assignment in shlex.split(properties.get('UnsetEnvironment', '')):
            name, separator, value = assignment.partition('=')
            if not separator or effective.get(name) == value:
                effective.pop(name, None)
    # Both runtime.env and service overrides may contain durable credentials.
    for values in [runtime, effective]:
        validate_payment_runtime(values)
        for name in ['PAYMENT_CONFIG_KEY_FILE', 'PAYMENT_PROVIDERS_FILE']:
            if values.get(name):
                source(values[name])
        manifest = values.get('PAYMENT_PROVIDERS_FILE')
        if manifest:
            try:
                providers = json.loads(Path(manifest).read_text())
                if not isinstance(providers, dict):
                    raise ValueError()
                for name in ['wechat', 'alipay']:
                    provider = providers.get(name)
                    if provider is None:
                        continue
                    if not isinstance(provider, dict):
                        raise ValueError()
                    files = [(provider.get('private_key_file'), True)]
                    if name == 'wechat':
                        files.append((provider.get('api_v3_key_file'), True))
                        keys = provider.get('platform_key_files', {})
                        if not isinstance(keys, dict):
                            raise ValueError()
                        files.extend((value, False) for value in keys.values())
                    else:
                        files.append((provider.get('public_key_file'), False))
                    for path, secret in files:
                        if not isinstance(path, str) or not path:
                            raise ValueError()
                        protected_payment_file(path, secret=secret)
                        source(path)
            except (ValueError, TypeError):
                raise DeploymentError('Static payment credential references are invalid; backup was not completed.') from None
    return sources, effective


def archive_private_text(archive, name, content):
    raw = content.encode('utf-8')
    info = tarfile.TarInfo(name)
    info.size, info.mode = len(raw), 0o600
    archive.addfile(info, io.BytesIO(raw))


def backup(root, destination):
    root = root.absolute()
    no_symlink(root)
    no_symlink(destination)
    if destination.parent != root / 'backups' or destination.exists():
        raise DeploymentError('Backup must be a new directory directly inside the dedicated backups tree.')
    credentials = json.loads((root / 'shared/credentials.json').read_text())
    inventory = inventory_resources()
    validate_resources(inventory, credentials, root)
    postgres = inventory.get('container:unihub-server-postgres')
    if not postgres or not postgres.get('State', {}).get('Running'):
        raise DeploymentError('The dedicated PostgreSQL container must be running for an online backup.')
    release = Path(read_env(root / 'shared/compose.env')['RELEASE_DIR'])
    if release.parent != root / 'releases' or not release.is_dir():
        raise DeploymentError('Configured release is outside the dedicated releases tree.')
    snapshot = systemd_snapshot()
    recovery_sources, effective = recovery_configuration(root, snapshot)
    private_directory(root / 'backups')
    private_directory(destination)
    atomic_write(destination / 'INCOMPLETE', 'Do not restore until manifest.json is present and checksums verify.\n')
    physical = destination / 'cluster.tar'
    # Local Unix-socket trust belongs to the private PostgreSQL container. No
    # password/DSN enters argv. -X fetch includes required WAL in this one tar.
    with physical.open('xb') as output, (destination / '.basebackup-stderr').open('xb') as errors:
        os.chmod(physical, 0o600)
        os.chmod(destination / '.basebackup-stderr', 0o600)
        result = subprocess.run(['docker', 'exec', 'unihub-server-postgres', 'pg_basebackup',
            '-U', 'unihub_admin', '-D', '-', '-F', 'tar', '-X', 'fetch', '--checkpoint=fast'],
            stdout=output, stderr=errors)
    if result.returncode:
        raise DeploymentError('Physical backup failed; partial backup retained privately, command output withheld.')
    (destination / '.basebackup-stderr').unlink()
    config = destination / 'configuration.tar'
    fd = os.open(config, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as output, tarfile.open(fileobj=output, mode='w') as archive:
        for relative in ['shared/credentials.json', 'shared/runtime.env', 'shared/compose.env', 'shared/casdoor/conf']:
            source = root / relative
            no_symlink(source)
            archive.add(source, arcname=relative, recursive=True)
        for relative in ['shared/identity-image.json','shared/headless-audit.json','headless-config','billing-config']:
            source=root/relative
            if source.exists():
                no_symlink(source)
                archive.add(source,arcname=relative,recursive=True)
        mapping = {}
        for index, source in enumerate(recovery_sources):
            name = 'host-configuration/files/' + str(index)
            mapping[str(source)] = name
            def private_member(info):
                info.mode, info.uid, info.gid = 0o600, 0, 0
                info.uname = info.gname = ''
                return info
            archive.add(source, arcname=name, recursive=False, filter=private_member)
        archive_private_text(archive, 'host-configuration/files.json', json.dumps(mapping, indent=2) + '\n')
        archive_private_text(archive, 'host-configuration/effective.env', env_text(effective))
        if snapshot is not None:
            archive_private_text(archive, 'host-configuration/systemd-show.txt', snapshot['show'])
            archive_private_text(archive, 'host-configuration/effective-unit.conf', snapshot['unit'])
        archive.add(release / 'deploy/server', arcname='release/deploy/server', recursive=True)
        for name in ['manifest.json', 'release-manifest.json', 'headless-release-manifest.json', 'REVISION',
                     'products.approved.json', 'identity/image-context/source-manifest.json']:
            if (release / name).is_file():
                no_symlink(release / name)
                archive.add(release / name, arcname='release/' + name)
    config.chmod(0o600)
    pin=identity_pin(root)
    images={**IMAGES,'casdoor':pin['image_id'] if pin else IMAGES['casdoor']}
    manifest = {'schema_version': 1, 'created_at': datetime.now(timezone.utc).isoformat(),
        'installation_id': credentials['installation_id'], 'release': release.name,
        'format': 'PostgreSQL 16 physical tar with WAL (pg_basebackup -X fetch)',
        'databases': ['unihub', 'ledger', 'casdoor'], 'identity_keys': 'Casdoor certificate/key tables included in full cluster',
        'images': images, 'files': {name: sha256(destination / name) for name in ['cluster.tar', 'configuration.tar']}}
    atomic_write(destination / 'manifest.json', json.dumps(manifest, indent=2) + '\n')
    (destination / 'INCOMPLETE').unlink()
    return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path('/opt/unihub'))
    parser.add_argument('--name', default=datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise DeploymentError('Backup must run as root to read restricted server configuration.')
    if not args.name or '/' in args.name or args.name in ['.', '..']:
        raise DeploymentError('Backup name must be one directory name.')
    os.umask(0o077)
    result = backup(args.root, args.root / 'backups' / args.name)
    print('Private full-cluster backup complete: ' + str(result))
    print('Run verify-restore.py against this directory before claiming recoverability.')


if __name__ == '__main__':
    try:
        main()
    except (DeploymentError, OSError, ValueError, KeyError):
        print('Backup failed; inspect the private backup directory. No credential values were printed.', file=sys.stderr)
        sys.exit(1)
