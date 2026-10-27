#!/usr/bin/env python3
"""Exercise curated release contents without rebuilding the unrelated server/UI."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('package_release', ROOT / 'scripts/package-release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseAssetsTests(unittest.TestCase):
    def test_release_supplies_buildable_userkit_and_starter_without_private_inputs(self):
        # Omitting the Go source asset or its starter/module dependencies breaks
        # this consumer contract; untracked local inputs must never enter it.
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve() / 'source'
            subprocess.run(['git', 'clone', '--quiet', '--shared', str(ROOT), str(root)], check=True)
            private = root / 'packages/userkit-go/untracked-private.go'
            private.write_text('never distribute this private input')
            with (root / '.git/info/exclude').open('a') as exclude:
                exclude.write('\n/packages/userkit-go/untracked-private.go\n')
            (root / '.local').mkdir()
            (root / '.local/runtime.env').write_text('PRIVATE_SENTINEL=do-not-distribute')
            output = Path(temporary).resolve() / 'assets'
            real_command = release.command

            def command(args, *, env=None):
                # Only expensive compiler/UI boundaries are substituted. Git
                # selection, curation, archive writing/readback and hashes run.
                if args[:2] == ['go', 'build']:
                    Path(args[args.index('-o') + 1]).write_bytes(b'fixture executable')
                    return ''
                if args[:2] == ['npm', '--prefix']:
                    dist = root / 'web/dist'
                    dist.mkdir(parents=True)
                    (dist / 'index.html').write_text('<!doctype html><title>fixture</title>')
                    return ''
                return real_command(args, env=env)

            with patch.object(release, 'ROOT', root), patch.object(release, 'command', command):
                version = (root / 'VERSION').read_text().strip()
                release.build(argparse.Namespace(version=version, output=str(output),
                    identity_artifacts=None, casdoor_web=None))

            source_asset = output / f'unihub-userkit-{version}-source.tar.gz'
            self.assertTrue(source_asset.is_file(), 'release is missing reusable Go kit/starter source asset')
            extracted = Path(temporary) / 'extracted'
            extracted.mkdir()
            with tarfile.open(source_asset, 'r:gz') as archive:
                names = {member.name for member in archive}
                prefix = f'unihub-userkit-{version}/'
                for relative in [
                    'packages/userkit-go/go.mod', 'packages/userkit-go/go.sum',
                    'packages/userkit-go/browser.go', 'packages/userkit-go/client.go',
                    'packages/userkit-go/service.go', 'packages/userkit-go/README.md',
                    'examples/product-users/main.go', 'examples/product-users/main_test.go',
                    'examples/product-users/lifecycle_test.go', 'examples/product-users/go.mod',
                    'examples/product-users/go.sum', 'scripts/product_user_init.py',
                    'scripts/product_client.py', 'config/examples/product-user-manifests.json',
                    'config/examples/product-runtime-services.json',
                    'packages/userkit-go/product_auth.go', 'packages/userkit-go/product_browser.go',
                    'packages/userkit-go/runtime.go', 'docs/userkit/runtime-adapter.md',
                    'docs/userkit/account-integration-modes.md',
                    'docs/product-user-api.md', 'docs/integrations/product-user-foundation.md',
                    'VERSION', 'CHANGELOG.md',
                ]:
                    self.assertIn(prefix + relative, names)
                self.assertFalse(any('untracked-private' in name or '/.local/' in name for name in names))
                for member in archive:
                    self.assertTrue(member.isfile())
                    target = extracted / member.name
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(archive.extractfile(member).read())
            consumer = extracted / f'unihub-userkit-{version}'
            for module in ['packages/userkit-go', 'examples/product-users']:
                subprocess.run(['go', 'test', '-mod=readonly', './...'], cwd=consumer / module, check=True)
            with tarfile.open(output / f'unihub-{version}-linux-amd64.tar.gz') as archive:
                names = {member.name for member in archive}
                prefix = f'unihub-{version}-linux-amd64/'
                self.assertIn(prefix + 'config/examples/product-user-manifests.json', names)
                self.assertIn(prefix + 'config/examples/product-runtime-services.json', names)
                self.assertIn(prefix + 'docs/userkit/runtime-adapter.md', names)
                self.assertIn(prefix + 'scripts/product_client.py', names)
                self.assertFalse(any('/.local/' in name for name in names))
            sums = (output / 'SHA256SUMS').read_text()
            self.assertIn(hashlib.sha256(source_asset.read_bytes()).hexdigest() + '  ' + source_asset.name, sums)
            manifest = json.loads((output / 'release-manifest.json').read_text())
            self.assertEqual(manifest['version'], version)


if __name__ == '__main__':
    unittest.main()
