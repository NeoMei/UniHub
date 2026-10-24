#!/usr/bin/env python3
"""Create a restricted, online full-cluster physical backup with included WAL."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile

from bootstrap import DeploymentError, IMAGES, atomic_write, identity_pin, inventory_resources, no_symlink, private_directory, read_env, validate_resources


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


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
    with tarfile.open(config, 'x') as archive:
        for relative in ['shared/credentials.json', 'shared/runtime.env', 'shared/compose.env', 'shared/casdoor/conf']:
            source = root / relative
            no_symlink(source)
            archive.add(source, arcname=relative, recursive=True)
        for relative in ['shared/identity-image.json','shared/headless-audit.json','headless-config']:
            source=root/relative
            if source.exists():
                no_symlink(source)
                archive.add(source,arcname=relative,recursive=True)
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
