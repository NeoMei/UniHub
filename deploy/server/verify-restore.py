#!/usr/bin/env python3
"""Verify a physical backup in a new isolated volume/container, without ports."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time

from bootstrap import DeploymentError, IMAGES, run


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('backup', type=Path)
    parser.add_argument('--retain', action='store_true', help='Keep this isolated container/volume for further private inspection.')
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise DeploymentError('Run restore verification as root to read the private backup.')
    os.umask(0o077)
    manifest = json.loads((args.backup / 'manifest.json').read_text())
    if (args.backup / 'INCOMPLETE').exists():
        raise DeploymentError('Backup is marked incomplete.')
    if set(manifest['files']) != {'cluster.tar', 'configuration.tar'} or manifest['images']['postgres'] != IMAGES['postgres']:
        raise DeploymentError('Backup manifest has an unexpected format or PostgreSQL image.')
    for name, expected in manifest['files'].items():
        digest = hashlib.sha256()
        with (args.backup / name).open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        if digest.hexdigest() != expected:
            raise DeploymentError('Backup checksum mismatch.')
    name = 'unihub-restore-' + secrets.token_hex(8)
    volume = name + '-postgres'
    image = IMAGES['postgres']
    run(['docker', 'volume', 'create', '--label', 'com.unihub.restore=' + name, volume])
    started = False
    verified = False
    try:
        # The backup tar is input, never a command argument. No live volume or
        # live network is referenced by this helper or the restored server.
        with (args.backup / 'cluster.tar').open('rb') as source:
            result = subprocess.run(['docker', 'run', '--rm', '--name', name + '-unpack',
                '--network', 'none', '-i', '--mount', 'type=volume,source=' + volume + ',target=/restore',
                image, 'sh', '-ceu', 'tar -xf - -C /restore; chown -R postgres:postgres /restore; chmod 700 /restore'],
                stdin=source, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if result.returncode:
            raise DeploymentError('Isolated backup extraction failed.')
        run(['docker', 'run', '--rm', '--name', name + '-verify', '--network', 'none', '--mount',
            'type=volume,source=' + volume + ',target=/restore', image, 'pg_verifybackup', '/restore'])
        run(['docker', 'run', '-d', '--name', name, '--label', 'com.unihub.restore=' + name,
            '--network', 'none', '--mount', 'type=volume,source=' + volume + ',target=/var/lib/postgresql/data', image])
        started = True
        for attempt in range(60):
            result = subprocess.run(['docker', 'exec', name, 'pg_isready', '-U', 'unihub_admin', '-h', '127.0.0.1', '-d', 'postgres'],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if result.returncode == 0:
                break
            time.sleep(1)
        else:
            raise DeploymentError('Isolated restored PostgreSQL failed to start.')
        query = "SELECT datname || ':' || pg_get_userbyid(datdba) FROM pg_database WHERE datname IN ('unihub','ledger','casdoor') ORDER BY datname;"
        output = run(['docker', 'exec', name, 'psql', '-U', 'unihub_admin', '-d', 'postgres', '-At', '-v', 'ON_ERROR_STOP=1', '-c', query])
        if output.strip().splitlines() != ['casdoor:unihub_casdoor', 'ledger:unihub_ledger', 'unihub:unihub_app']:
            raise DeploymentError('Restored database owners do not match all three dedicated databases.')
        for database in ['unihub', 'ledger', 'casdoor']:
            run(['docker', 'exec', name, 'psql', '-U', 'unihub_admin', '-d', database,
                '-At', '-v', 'ON_ERROR_STOP=1', '-c', 'SELECT count(*) FROM information_schema.tables WHERE table_schema=\'public\';'])
        # Check presence of signing material without ever selecting its content.
        key_count = run(['docker', 'exec', name, 'psql', '-U', 'unihub_admin', '-d', 'casdoor', '-At',
            '-v', 'ON_ERROR_STOP=1', '-c', "SELECT count(*) FROM cert WHERE name='cert-built-in' AND length(private_key)>0;"])
        if key_count.strip() != '1':
            raise DeploymentError('Restored Casdoor signing key was not found.')
        verified = True
        print('Isolated physical restore verified: checksum, pg_verifybackup, all three database owners and Casdoor signing-key presence.')
        print('Application login after restoration still requires an isolated identity/API end-to-end check.')
    finally:
        if args.retain:
            print('Isolated restore retained privately: container=' + name + ' volume=' + volume)
        else:
            if started:
                run(['docker', 'rm', '-f', name])
            run(['docker', 'volume', 'rm', volume])
        if not verified:
            print('Restore verification incomplete.', file=sys.stderr)


if __name__ == '__main__':
    try:
        main()
    except (DeploymentError, OSError, ValueError, KeyError):
        print('Restore verification failed; no live UniHub resource was changed.', file=sys.stderr)
        sys.exit(1)
