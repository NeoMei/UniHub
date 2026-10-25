#!/usr/bin/env python3
"""Real disposable billing staging/prepare checks; no production or Docker writes."""
import copy
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import bootstrap as bundle
import backup as backup_bundle


HERE = Path(__file__).resolve().parent


class BillingDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve() / 'unihub'
        self.release = self.root / 'releases' / 'billing-test'
        for relative in ['bin/unihub', 'web/dist/index.html', 'casdoor-web/index.html',
                         'casdoor-web/swagger/index.html', 'casdoor-web/assets/unihub/unihub-logo-v1.png',
                         'casdoor-web/assets/unihub/unihub-icon-v1.png']:
            path = self.release / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('fixture')
        (self.release / 'deploy/server').mkdir(parents=True)
        bundle.prepare(self.root, self.release, {})
        self.source = Path(self.temp.name).resolve() / 'approved.json'
        self.public = self.root / 'billing-config/products.json'
        self.profiles = [{'product_id': 'cash-product', 'version': 1, 'preset': 'prepaid'}]

    def write(self, items):
        self.source.write_text(json.dumps(items))
        return self.source

    def stage(self, items=None):
        bundle.stage_billing(self.root, self.write(self.profiles if items is None else items))
        bundle.prepare(self.root, self.release, {})

    def test_absent_billing_file_keeps_legacy_runtime(self):
        bundle.prepare(self.root, self.release, {})
        runtime = bundle.read_env(self.root / 'shared/runtime.env')
        self.assertNotIn('PRODUCT_BILLING_FILE', runtime)
        self.assertFalse(self.public.exists())
        self.assertEqual(runtime['SANDBOX_CATALOG'], 'true')
        self.assertEqual(runtime['PAYMENT_MODE'], 'sandbox')

    def test_explicit_staging_survives_prepare_without_credentials_or_identity_changes(self):
        credentials = (self.root / 'shared/credentials.json').read_bytes()
        seed = (self.root / 'shared/casdoor/conf/init_data.json').read_bytes()
        self.stage()
        published = self.public.read_bytes()
        for _ in range(2):
            bundle.prepare(self.root, self.release, {})
            runtime = bundle.read_env(self.root / 'shared/runtime.env')
            self.assertEqual(runtime['PRODUCT_BILLING_FILE'], str(self.public))
            self.assertNotIn('PRODUCT_CLIENTS_FILE', runtime)
            self.assertEqual(runtime['HEADLESS_AUDIT_REDACTION_VERIFIED'], 'false')
            self.assertEqual(credentials, (self.root / 'shared/credentials.json').read_bytes())
            self.assertEqual(seed, (self.root / 'shared/casdoor/conf/init_data.json').read_bytes())
            self.assertEqual(published, self.public.read_bytes())
            self.assertFalse((self.root / 'shared/headless-audit.json').exists())
        self.assertEqual(json.loads(published), self.profiles)

    def test_public_config_is_outside_secrets_and_readable_by_nonroot_service(self):
        self.stage()
        self.assertEqual(stat.S_IMODE(self.public.stat().st_mode), 0o644)
        self.assertEqual(stat.S_IMODE(self.public.parent.stat().st_mode), 0o755)
        self.assertNotIn('shared', self.public.parts)
        for path in [self.public.parent, self.root]:
            self.assertTrue(path.stat().st_mode & stat.S_IXOTH)
        if os.geteuid() == 0:
            self.assertEqual(self.public.stat().st_uid, 0)
            self.assertEqual(self.public.parent.stat().st_uid, 0)
            # Linux root execution can exercise the actual nobody uid; on a
            # developer machine these same access bits are the service contract.
            Path(self.temp.name).chmod(0o755)
            result = subprocess.run([sys.executable, '-c', 'import json,sys;json.load(open(sys.argv[1]))', str(self.public)],
                                    user=65534, group=65534, extra_groups=[], capture_output=True)
            self.assertEqual(result.returncode, 0, 'nonroot process could not read public billing configuration')
        self.assertEqual(stat.S_IMODE((self.root / 'shared').stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE((self.root / 'shared/credentials.json').stat().st_mode), 0o600)

    def test_existing_accepted_headless_policy_gate_and_secrets_are_preserved(self):
        clients = [{'product_id': 'source-product', 'client_id': 'source-headless', 'application': 'app-source-headless',
                    'organization': 'unihub', 'redirect_uris': ['https://example.test/callback'],
                    'origins': ['https://example.test'], 'scopes': ['catalog:read'],
                    'billing_products': [], 'password_login': True, 'registration': True}]
        bundle.stage_headless(self.root, clients)
        credentials = json.loads((self.root / 'shared/credentials.json').read_text())
        pin = {'image_id': 'sha256:' + 'a' * 64, 'backend_sha256': 'b' * 64,
               'source_commit': bundle.AUDITED_COMMIT, 'transition_complete': True,
               'installation_id': credentials['installation_id']}
        bundle.atomic_write(self.root / 'shared/identity-image.json', json.dumps(pin))
        bundle.prepare(self.root, self.release, {})
        state = {'enabled': True, 'installation_id': credentials['installation_id'],
                 'policy_sha256': bundle.file_sha256(self.root / 'headless-config/products.draft.json'),
                 'image_id': pin['image_id'], 'backend_sha256': pin['backend_sha256']}
        bundle.atomic_write(self.root / 'shared/headless-audit.json', json.dumps(state))
        bundle.prepare(self.root, self.release, {})
        tracked = ['shared/credentials.json', 'shared/headless-audit.json', 'shared/identity-image.json',
                   'headless-config/products.json', 'headless-config/products.draft.json',
                   'shared/casdoor/conf/init_data.json', 'shared/casdoor/conf/app.conf']
        before = {name: (self.root / name).read_bytes() for name in tracked}
        self.stage()
        bundle.prepare(self.root, self.release, {})
        self.assertEqual(before, {name: (self.root / name).read_bytes() for name in tracked})
        self.assertEqual(bundle.read_env(self.root / 'shared/runtime.env')['HEADLESS_AUDIT_REDACTION_VERIFIED'], 'true')

    def test_presets_and_complete_compositions_are_accepted(self):
        for preset in ['hybrid', 'prepaid', 'subscription']:
            self.stage([{'product_id': 'my-product', 'version': 2, 'preset': preset}])
        for example in sorted((HERE.parents[1] / 'examples/billing').glob('*.json')):
            with self.subTest(example=example.name):
                bundle.stage_billing(self.root, example)
                bundle.prepare(self.root, self.release, {})

    def test_strict_public_schema_rejects_unknown_private_and_incomplete_fields(self):
        custom = {'product_id': 'my-product', 'version': 1, 'subscription': {'enabled': True},
                  'included_credits': {'enabled': False}, 'addons': {'enabled': False},
                  'usage': {'enabled': True, 'require_subscription': True,
                            'funding_modes': ['prepaid_cash'], 'default_funding_mode': 'prepaid_cash'}}
        rejected = [None, {}, [None], [self.profiles[0]] * 2, self.profiles * 101]
        for version in [0, -1, True, 1.0, '1', 9223372036854775808]:
            rejected.append([{**self.profiles[0], 'version': version}])
        for product in ['', 'A-product', 'product/secret', 'a' * 65, None, 1]:
            rejected.append([{**self.profiles[0], 'product_id': product}])
        for secret in ['client_secret', 'token', 'password', 'headless_clients', 'payment_signing_key']:
            rejected.append([{**self.profiles[0], secret: 'PRIVATE_MARKER'}])
        rejected.extend([[{**self.profiles[0], 'subscription': {'enabled': True}}],
                         [{**self.profiles[0], 'preset': 'unknown'}],
                         [{**self.profiles[0], 'preset': None}],
                         [{**custom, 'subscription': {'enabled': True, 'secret': 'PRIVATE_MARKER'}}]])
        for name in ['subscription', 'included_credits', 'addons', 'usage']:
            incomplete = copy.deepcopy(custom)
            del incomplete[name]
            rejected.append([incomplete])
        for name in ['enabled', 'require_subscription', 'funding_modes', 'default_funding_mode']:
            incomplete = copy.deepcopy(custom)
            del incomplete['usage'][name]
            rejected.append([incomplete])
        for changes in [{'funding_modes': []}, {'funding_modes': ['credits', 'credits']},
                        {'funding_modes': ['other']}, {'default_funding_mode': 'credits'},
                        {'enabled': False}, {'enabled': 1}, {'require_subscription': 1},
                        {'funding_modes': None}, {'default_funding_mode': None}]:
            bad = copy.deepcopy(custom)
            bad['usage'].update(changes)
            rejected.append([bad])
        for component in ['included_credits', 'addons']:
            bad = copy.deepcopy(custom)
            bad[component]['enabled'] = True
            rejected.append([bad])
        bad = copy.deepcopy(custom)
        bad['subscription']['enabled'] = False
        rejected.append([bad])
        self.stage()
        before = self.public.read_bytes()
        secrets_before = (self.root / 'shared/credentials.json').read_bytes()
        for items in rejected:
            with self.subTest(items=repr(items)[:100]):
                with self.assertRaises(bundle.DeploymentError) as error:
                    bundle.stage_billing(self.root, self.write(items))
                self.assertNotIn('PRIVATE_MARKER', str(error.exception))
                self.assertEqual(before, self.public.read_bytes())
                self.assertEqual(secrets_before, (self.root / 'shared/credentials.json').read_bytes())

    def test_duplicate_fields_extra_json_and_oversized_files_are_rejected(self):
        for raw in ['[{"product_id":"p","version":1,"version":2,"preset":"prepaid"}]',
                    '[{"product_id":"p","version":1,"preset":"prepaid"}] []', 'x', '[' + ' ' * (1 << 20) + ']']:
            self.source.write_text(raw)
            with self.assertRaises(bundle.DeploymentError):
                bundle.stage_billing(self.root, self.source)
        for raw in [json.dumps(self.profiles).encode('utf-16'), b'\xef\xbb\xbf' + json.dumps(self.profiles).encode()]:
            self.source.write_bytes(raw)
            with self.assertRaises(bundle.DeploymentError):
                bundle.stage_billing(self.root, self.source)

    def test_symlinks_hardlinks_and_nonregular_files_are_refused(self):
        real = self.write(self.profiles)
        link = real.with_name('symlink.json')
        link.symlink_to(real)
        with self.assertRaises(bundle.DeploymentError):
            bundle.stage_billing(self.root, link)
        hard = real.with_name('hardlink.json')
        os.link(real, hard)
        with self.assertRaises(bundle.DeploymentError):
            bundle.stage_billing(self.root, hard)
        hard.unlink()
        fifo = real.with_name('fifo.json')
        os.mkfifo(fifo)
        for path in [fifo, real.parent]:
            with self.assertRaises(bundle.DeploymentError):
                bundle.stage_billing(self.root, path)
        self.stage()
        self.public.unlink()
        self.public.symlink_to(real)
        with self.assertRaises(bundle.DeploymentError):
            bundle.prepare(self.root, self.release, {})
        with self.assertRaises(bundle.DeploymentError):
            bundle.stage_billing(self.root, real)

    def test_corrupt_staged_profiles_fail_prepare_instead_of_silent_fallback(self):
        self.stage()
        before = (self.root / 'shared/runtime.env').read_bytes()
        self.public.write_text('[{"product_id":"p","version":0,"preset":"prepaid"}]')
        with self.assertRaises(bundle.DeploymentError):
            bundle.prepare(self.root, self.release, {})
        self.assertEqual(before, (self.root / 'shared/runtime.env').read_bytes())

    def test_stage_billing_cli_validates_before_inventory_and_never_prints_contents(self):
        self.write([{**self.profiles[0], 'client_secret': 'PRIVATE_MARKER'}])
        output = io.StringIO()
        args = ['bootstrap.py', 'stage-billing', '--root', str(self.root), '--release', str(self.release),
                '--billing-profiles', str(self.source)]
        with patch.object(sys, 'argv', args), patch.object(bundle.os, 'geteuid', return_value=0), \
                patch.object(bundle, 'inventory_resources') as inventory, patch('sys.stdout', output):
            with self.assertRaises(bundle.DeploymentError):
                bundle.main()
            inventory.assert_not_called()
        self.assertNotIn('PRIVATE_MARKER', output.getvalue())
        self.write(self.profiles)
        with patch.object(sys, 'argv', args), patch.object(bundle.os, 'geteuid', return_value=0), \
                patch.object(bundle.os, 'chown'), patch.object(bundle.os, 'fchown'), \
                patch.object(bundle, 'inventory_resources', return_value={}), patch.object(bundle, 'validate_ports'), \
                patch('sys.stdout', output):
            bundle.main()
        self.assertEqual(bundle.read_env(self.root / 'shared/runtime.env')['PRODUCT_BILLING_FILE'], str(self.public))
        self.assertNotIn('PRIVATE_MARKER', output.getvalue())

    def test_backup_archive_includes_staged_billing_configuration(self):
        self.stage()
        inventory = {'container:unihub-server-postgres': {'State': {'Running': True}}}
        def basebackup(command, stdout, stderr):
            stdout.write(b'isolated backup fixture')
            return SimpleNamespace(returncode=0)
        destination = self.root / 'backups/test-billing'
        with patch.object(backup_bundle, 'inventory_resources', return_value=inventory), \
                patch.object(backup_bundle, 'validate_resources'), patch.object(backup_bundle.subprocess, 'run', side_effect=basebackup):
            backup_bundle.backup(self.root, destination)
        with tarfile.open(destination / 'configuration.tar') as archive:
            self.assertEqual(archive.extractfile('billing-config/products.json').read(), self.public.read_bytes())


if __name__ == '__main__':
    unittest.main()
