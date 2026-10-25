#!/usr/bin/env python3
"""Behavior tests for the offline public billing configuration tool."""
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('product_billing.py')


class BillingConfigTests(unittest.TestCase):
    def run_cli(self, *args):
        return subprocess.run([sys.executable, str(SCRIPT), *args], capture_output=True, text=True)

    def test_prepare_creates_deployment_array_for_every_preset(self):
        with tempfile.TemporaryDirectory() as temp:
            for preset in ['hybrid', 'prepaid', 'subscription']:
                output = Path(temp) / (preset + '.json')
                result = self.run_cli('prepare', '--product-id', 'my-product', '--preset', preset, '--version', '3', '--output', str(output))
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(output.read_text()), [{'product_id': 'my-product', 'version': 3, 'preset': preset}])
                self.assertEqual(self.run_cli('validate', '--input', str(output)).returncode, 0)

    def test_prepare_refuses_to_overwrite_a_reviewed_file(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / 'billing.json'
            output.write_text('reviewed')
            result = self.run_cli('prepare', '--product-id', 'my-product', '--preset', 'prepaid', '--output', str(output))
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(output.read_text(), 'reviewed')

    def test_custom_subscription_and_cash_composition_validates_without_idp_or_secret(self):
        custom = [{'product_id': 'my-product', 'version': 1, 'subscription': {'enabled': True}, 'included_credits': {'enabled': False}, 'addons': {'enabled': False}, 'usage': {'enabled': True, 'require_subscription': True, 'funding_modes': ['prepaid_cash'], 'default_funding_mode': 'prepaid_cash'}}]
        with tempfile.TemporaryDirectory() as temp:
            source, output = Path(temp) / 'custom.json', Path(temp) / 'output.json'
            source.write_text(json.dumps(custom))
            result = self.run_cli('prepare', '--input', str(source), '--output', str(output))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(output.read_text()), custom)
            self.assertEqual(self.run_cli('validate', '--input', str(output)).returncode, 0)

    def test_validate_rejects_unknown_partial_duplicate_and_invalid_compositions(self):
        valid = {'product_id': 'my-product', 'version': 1, 'preset': 'prepaid'}
        cases = [
            [{**valid, 'client_secret': 'test-never-print-me'}],
            [{**valid, 'subscription': {'enabled': True}}],
            [valid, valid],
            [{**valid, 'version': True}],
            [{'product_id': 'my-product', 'version': 1, 'subscription': {'enabled': True}}],
            [{'product_id': 'my-product', 'version': 1, 'subscription': {'enabled': False}, 'included_credits': {'enabled': True}, 'addons': {'enabled': False}, 'usage': {'enabled': True, 'require_subscription': False, 'funding_modes': ['credits'], 'default_funding_mode': 'credits'}}],
            [{'product_id': 'my-product', 'version': 1, 'subscription': {'enabled': True}, 'included_credits': {'enabled': False}, 'addons': {'enabled': False}, 'usage': {'enabled': True, 'require_subscription': False, 'funding_modes': ['prepaid_cash'], 'default_funding_mode': 'credits'}}],
        ]
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp) / 'bad.json'
            for value in cases:
                source.write_text(json.dumps(value))
                result = self.run_cli('validate', '--input', str(source))
                self.assertNotEqual(result.returncode, 0, value)
                self.assertNotIn('test-never-print-me', result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
