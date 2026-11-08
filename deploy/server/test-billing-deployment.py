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

    def test_prepare_preserves_accepted_admin_identity_flags(self):
        runtime_path = self.root / 'shared/runtime.env'
        runtime_path.write_text(runtime_path.read_text() +
                                 'ADMIN_IDENTITY_DEPLOYMENT_VERIFIED=true\n'
                                 'ADMIN_SESSION_REVOCATION_VERIFIED=true\n'
                                 'RECOVERY_SMTP_ADDR=smtp.example.test:587\n'
                                 'RECOVERY_SMTP_FROM=ops@example.test\n')
        bundle.prepare(self.root, self.release, {})
        runtime = bundle.read_env(runtime_path)
        self.assertEqual(runtime['ADMIN_IDENTITY_DEPLOYMENT_VERIFIED'], 'true')
        self.assertEqual(runtime['ADMIN_SESSION_REVOCATION_VERIFIED'], 'true')
        self.assertEqual(runtime['RECOVERY_SMTP_ADDR'], 'smtp.example.test:587')
        self.assertEqual(runtime['RECOVERY_SMTP_FROM'], 'ops@example.test')

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

    def native_profile(self):
        return {'product_id': 'superwagie', 'version': 3,
                'subscription': {'enabled': True}, 'included_credits': {'enabled': True},
                'addons': {'enabled': True},
                'usage': {'enabled': True, 'require_subscription': True,
                          'require_subscription_for_credits': True,
                          'funding_modes': ['credits', 'prepaid_cash'],
                          'default_funding_mode': 'prepaid_cash'},
                'native_credit_conversion': {'version': 'sw-native-cny10-20261109-v1',
                                             'numerator': '146', 'denominator': '1',
                                             'rounding': 'lr-cumulative-ceil-v1'}}

    def test_native_conversion_and_credit_subscription_roundtrip(self):
        profile = self.native_profile()
        self.stage([profile])
        self.assertEqual(json.loads(self.public.read_text()), [profile])
        # A hybrid preset also supplies the subscription and credits policy.
        preset = {'product_id': 'superwagie', 'version': 4, 'preset': 'hybrid',
                  'native_credit_conversion': profile['native_credit_conversion']}
        self.stage([preset])
        self.assertEqual(json.loads(self.public.read_text()), [preset])

    def test_native_conversion_and_credit_subscription_are_strict(self):
        valid = self.native_profile()
        rejected = []
        for name in ['version', 'numerator', 'denominator', 'rounding']:
            bad = copy.deepcopy(valid)
            del bad['native_credit_conversion'][name]
            rejected.append(bad)
        for key, values in {
            'version': ['', 'lr-quota-usd-cny-v1', 'a' * 129, 'x/y', 1, None],
            'numerator': ['0', '-1', '+1', '01', '1.0', ' 1', '9223372036854775808', 1, True, None],
            'denominator': ['0', '-1', '01', '9223372036854775808', 1, None],
            'rounding': ['', 'ceil', None], 'secret': ['PRIVATE_MARKER'],
        }.items():
            for value in values:
                bad = copy.deepcopy(valid)
                bad['native_credit_conversion'][key] = value
                rejected.append(bad)
        for value in [[], 'conversion']:
            rejected.append({**valid, 'native_credit_conversion': value})
        rejected.append({**valid, 'product_id': 'other'})
        for changes in [{'require_subscription_for_credits': 1},
                        {'funding_modes': ['prepaid_cash']},
                        {'enabled': False, 'funding_modes': [], 'default_funding_mode': '',
                         'require_subscription': False}]:
            bad = copy.deepcopy(valid)
            bad.pop('native_credit_conversion')
            bad['included_credits']['enabled'] = False
            bad['addons']['enabled'] = False
            bad['usage'].update(changes)
            rejected.append(bad)
        bad = copy.deepcopy(valid)
        bad['subscription']['enabled'] = False
        bad['included_credits']['enabled'] = False
        bad['usage']['require_subscription'] = False
        rejected.append(bad)
        self.stage()
        before = self.public.read_bytes()
        for item in rejected:
            with self.subTest(item=item):
                with self.assertRaises(bundle.DeploymentError):
                    bundle.stage_billing(self.root, self.write([item]))
                self.assertEqual(before, self.public.read_bytes())

    def direct_runtime(self, key=True):
        path = self.root / 'shared/runtime.env'
        values = bundle.read_env(path)
        values.update({'APP_ENV': 'production', 'PAYMENT_MODE': 'direct',
                       'SANDBOX_CATALOG': 'false', 'EXISTING_IDENTITY_SECRET': 'fixture retained'})
        protected = Path(self.temp.name).resolve() / 'merchant.key'
        if key:
            protected.write_bytes(bytes(range(32)))
            protected.chmod(0o600)
            values['PAYMENT_CONFIG_KEY_FILE'] = str(protected)
        else:
            protected.write_text('{"fixture": true}')
            protected.chmod(0o600)
            values['PAYMENT_PROVIDERS_FILE'] = str(protected)
        bundle.atomic_write(path, bundle.env_text(values))
        return path, values, protected

    def test_prepare_preserves_direct_mode_key_static_paths_and_unrelated_secrets(self):
        for key in [True, False]:
            path, values, protected = self.direct_runtime(key)
            values.pop('PAYMENT_PROVIDERS_FILE' if key else 'PAYMENT_CONFIG_KEY_FILE', None)
            bundle.atomic_write(path, bundle.env_text(values))
            original = protected.read_bytes()
            bundle.prepare(self.root, self.release, {})
            actual = bundle.read_env(path)
            for name, value in values.items():
                self.assertEqual(actual[name], value, name)
            self.assertEqual(protected.read_bytes(), original)

    def test_managed_key_accepts_service_owner_and_rejects_unrelated_owner(self):
        _, _, protected = self.direct_runtime()
        actual = protected.stat()
        def owned(uid):
            fields = list(actual)
            fields[4] = uid
            return os.stat_result(fields)
        with patch.object(bundle.os, 'geteuid', return_value=0), \
                patch('pwd.getpwnam', return_value=SimpleNamespace(pw_uid=65431)):
            with patch.object(bundle.os, 'fstat', return_value=owned(65431)):
                self.assertEqual(bundle.protected_payment_file(str(protected), key=True), protected)
            with patch.object(bundle.os, 'fstat', return_value=owned(65432)):
                with self.assertRaises(bundle.DeploymentError):
                    bundle.protected_payment_file(str(protected), key=True)

    def test_prepare_refuses_missing_unsafe_or_conflicting_managed_key_without_rotation(self):
        path, values, protected = self.direct_runtime()
        before = path.read_bytes()
        secret = protected.read_bytes()
        for unsafe in ['missing', 'short', 'public', 'symlink', 'hardlink']:
            protected.unlink(missing_ok=True)
            link = protected.with_suffix('.target')
            link.unlink(missing_ok=True)
            if unsafe == 'symlink':
                link.write_bytes(secret)
                link.chmod(0o600)
                protected.symlink_to(link)
            elif unsafe == 'hardlink':
                link.write_bytes(secret)
                link.chmod(0o600)
                os.link(link, protected)
            elif unsafe != 'missing':
                protected.write_bytes(b'short' if unsafe == 'short' else secret)
                protected.chmod(0o644 if unsafe == 'public' else 0o600)
            with self.subTest(unsafe=unsafe), self.assertRaises(bundle.DeploymentError):
                bundle.prepare(self.root, self.release, {})
            self.assertEqual(path.read_bytes(), before)
            if unsafe == 'missing':
                self.assertFalse(protected.exists())
        protected.unlink()
        protected.write_bytes(secret)
        protected.chmod(0o600)
        for changes in [{'PAYMENT_PROVIDERS_FILE': str(protected)}, {'PAYMENT_MODE': 'sandbox'}]:
            bundle.atomic_write(path, bundle.env_text({**values, **changes}))
            with self.assertRaises(bundle.DeploymentError):
                bundle.prepare(self.root, self.release, {})
            self.assertEqual(protected.read_bytes(), secret)

    def test_presets_and_complete_compositions_are_accepted(self):
        for preset in ['hybrid', 'prepaid', 'subscription']:
            self.stage([{'product_id': 'my-product', 'version': 2, 'preset': preset}])
        for name in ['dual-assets', 'hybrid', 'prepaid', 'subscription-cash', 'subscription']:
            example = HERE.parents[1] / 'examples/billing' / (name + '.json')
            with self.subTest(example=example.name):
                bundle.stage_billing(self.root, example)
                bundle.prepare(self.root, self.release, {})
        model_scoped = [{
            'product_id': 'model-product', 'version': 1,
            'subscription': {'enabled': True},
            'included_credits': {'enabled': True},
            'addons': {'enabled': True},
            'usage': {
                'enabled': True, 'require_subscription': True,
                'funding_modes': ['credits'], 'default_funding_mode': 'credits',
                'models': ['model-a', 'model-b'],
            },
        }]
        self.stage(model_scoped)

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

    def test_static_manifest_backup_keeps_referenced_credential_files(self):
        runtime, values, manifest = self.direct_runtime(key=False)
        private = manifest.with_name('signing.pem')
        private.write_text('private signing fixture')
        private.chmod(0o600)
        public = manifest.with_name('public.pem')
        public.write_text('public verification fixture')
        public.chmod(0o644)
        api_key = manifest.with_name('api-v3.key')
        api_key.write_bytes(bytes(range(32)))
        api_key.chmod(0o600)
        manifest.write_text(json.dumps({'wechat': {'private_key_file': str(private),
                                                  'api_v3_key_file': str(api_key),
                                                  'platform_key_files': {'serial': str(public)}},
                                       'alipay': {'private_key_file': str(private),
                                                  'public_key_file': str(public)}}))
        manifest.chmod(0o644)
        bundle.prepare(self.root, self.release, {})
        self.assertEqual(bundle.read_env(runtime)['PAYMENT_PROVIDERS_FILE'], str(manifest))
        sources, effective = backup_bundle.recovery_configuration(self.root, None)
        self.assertEqual(set(sources), {manifest, private, public, api_key})
        self.assertEqual(effective['PAYMENT_MODE'], 'direct')

    def test_systemd_environment_excludes_inactive_runtime_values_and_honors_unset(self):
        self.direct_runtime()
        properties = {'Environment': 'PAYMENT_MODE=disabled IDENTITY_SECRET=fixture',
                      'UnsetEnvironment': 'IDENTITY_SECRET', 'EnvironmentFiles': ''}
        _, effective = backup_bundle.recovery_configuration(self.root, {'properties': properties})
        self.assertEqual(effective, {'PAYMENT_MODE': 'disabled'})

    def test_backup_restores_effective_unit_dropins_environment_and_merchant_key_privately(self):
        runtime_path, _, runtime_key = self.direct_runtime()
        host = Path(self.temp.name).resolve() / 'host-systemd'
        host.mkdir()
        unit = host / 'unihub.service'
        unit.write_text('[Service]\nEnvironmentFile=' + str(runtime_path) + '\n')
        dropin = host / 'commerce.conf'
        effective_key = host / 'effective.key'
        effective_key.write_bytes(bytes(reversed(range(32))))
        effective_key.chmod(0o600)
        extra_env = host / 'commerce.env'
        extra_env.write_text(bundle.env_text({'PAYMENT_CONFIG_KEY_FILE': str(effective_key),
                                             'EXTRA_IDENTITY_SECRET': 'private fixture'}))
        extra_env.chmod(0o600)
        dropin.write_text('[Service]\nEnvironmentFile=' + str(extra_env) + '\n')
        show = ('LoadState=loaded\nFragmentPath=' + str(unit) + '\nDropInPaths=' + str(dropin) +
                '\nEnvironment=UNIT_IDENTITY_SECRET=unit-fixture\nEnvironmentFiles=' +
                str(runtime_path) + ' (ignore_errors=no) ' + str(extra_env) + ' (ignore_errors=no)\n')
        effective_unit = '# ' + str(unit) + '\n' + unit.read_text() + '# ' + str(dropin) + '\n' + dropin.read_text()
        def commands(command, **kwargs):
            if command[:2] == ['systemctl', 'show']:
                return SimpleNamespace(returncode=0, stdout=show)
            if command[:2] == ['systemctl', 'cat']:
                return SimpleNamespace(returncode=0, stdout=effective_unit)
            self.assertEqual(command[:3], ['docker', 'exec', 'unihub-server-postgres'])
            kwargs['stdout'].write(b'isolated backup fixture')
            return SimpleNamespace(returncode=0)
        destination = self.root / 'backups/test-effective-config'
        inventory = {'container:unihub-server-postgres': {'State': {'Running': True}}}
        with patch.object(backup_bundle, 'inventory_resources', return_value=inventory), \
                patch.object(backup_bundle, 'validate_resources'), \
                patch('shutil.which', return_value='/fixture/systemctl'), \
                patch.object(backup_bundle.subprocess, 'run', side_effect=commands):
            backup_bundle.backup(self.root, destination)
        self.assertEqual(stat.S_IMODE(destination.stat().st_mode), 0o700)
        for path in destination.iterdir():
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        restored = Path(self.temp.name).resolve() / 'isolated-restore'
        restored.mkdir(mode=0o700)
        with tarfile.open(destination / 'configuration.tar') as archive:
            self.assertTrue(all(not Path(item.name).is_absolute() and '..' not in Path(item.name).parts for item in archive))
            archive.extractall(restored, filter='data')
        self.assertTrue((restored / 'host-configuration/files.json').is_file(), 'backup omitted merchant/unit recovery mapping')
        mapping = json.loads((restored / 'host-configuration/files.json').read_text())
        for source in [unit, dropin, extra_env, runtime_key, effective_key]:
            restored_path = restored / mapping[str(source)]
            self.assertEqual(restored_path.read_bytes(), source.read_bytes())
            self.assertEqual(stat.S_IMODE(restored_path.stat().st_mode), 0o600)
        self.assertEqual((restored / 'host-configuration/effective-unit.conf').read_text(), effective_unit)
        effective = bundle.read_env(restored / 'host-configuration/effective.env')
        self.assertEqual(effective['PAYMENT_CONFIG_KEY_FILE'], str(effective_key))
        self.assertEqual(effective['PAYMENT_MODE'], 'direct')
        self.assertEqual(effective['UNIT_IDENTITY_SECRET'], 'unit-fixture')
        self.assertEqual(effective['EXTRA_IDENTITY_SECRET'], 'private fixture')
        manifest = json.loads((destination / 'manifest.json').read_text())
        self.assertEqual(manifest['files']['configuration.tar'], backup_bundle.sha256(destination / 'configuration.tar'))

    def test_backup_archive_includes_staged_billing_configuration(self):
        self.stage()
        inventory = {'container:unihub-server-postgres': {'State': {'Running': True}}}
        def basebackup(command, stdout, stderr):
            stdout.write(b'isolated backup fixture')
            return SimpleNamespace(returncode=0)
        destination = self.root / 'backups/test-billing'
        with patch.object(backup_bundle, 'inventory_resources', return_value=inventory), \
                patch.object(backup_bundle, 'validate_resources'), patch.object(backup_bundle, 'systemd_snapshot', return_value=None), patch.object(backup_bundle.subprocess, 'run', side_effect=basebackup):
            backup_bundle.backup(self.root, destination)
        with tarfile.open(destination / 'configuration.tar') as archive:
            self.assertEqual(archive.extractfile('billing-config/products.json').read(), self.public.read_bytes())


if __name__ == '__main__':
    unittest.main()
