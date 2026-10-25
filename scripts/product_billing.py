#!/usr/bin/env python3
"""Prepare/validate public PRODUCT_BILLING_FILE JSON offline; no network or secrets."""
import argparse
import json
import os
import re
import sys
import tempfile
from pathlib import Path

PRESETS = ('hybrid', 'prepaid', 'subscription')
MODES = {'credits', 'prepaid_cash'}
COMPONENTS = {'subscription', 'included_credits', 'addons', 'usage'}


class PolicyError(ValueError):
    pass


def require(condition, message='Invalid billing configuration'):
    if not condition:
        raise PolicyError(message)


def exact(value, keys):
    require(isinstance(value, dict) and set(value) == set(keys), 'Unknown or missing configuration fields')


def validate(items):
    require(isinstance(items, list) and len(items) <= 100, 'Expected a JSON array of at most 100 profiles')
    seen = set()
    for item in items:
        require(isinstance(item, dict))
        product = item.get('product_id')
        require(isinstance(product, str) and re.fullmatch(r'[a-z][a-z0-9_-]{0,63}', product) is not None, 'Invalid product ID')
        require(product not in seen, 'Duplicate product ID')
        seen.add(product)
        version = item.get('version')
        require(type(version) is int and 1 <= version <= 9223372036854775807, 'Version must be a positive int64')
        if 'preset' in item:
            exact(item, {'product_id', 'version', 'preset'})
            require(item['preset'] in PRESETS, 'Unknown billing preset')
            continue
        exact(item, {'product_id', 'version'} | COMPONENTS)
        for component in ('subscription', 'included_credits', 'addons'):
            exact(item[component], {'enabled'})
            require(type(item[component]['enabled']) is bool)
        usage = item['usage']
        exact(usage, {'enabled', 'require_subscription', 'funding_modes', 'default_funding_mode'})
        require(type(usage['enabled']) is bool and type(usage['require_subscription']) is bool)
        modes = usage['funding_modes']
        require(isinstance(modes, list) and all(isinstance(x, str) and x in MODES for x in modes))
        require(len(set(modes)) == len(modes), 'Duplicate funding mode')
        if usage['enabled']:
            require(bool(modes) and usage['default_funding_mode'] in modes, 'Enabled usage needs an allowed default funding mode')
        else:
            require(not modes and usage['default_funding_mode'] == '' and not usage['require_subscription'], 'Disabled usage must have no funding modes')
        require(not usage['require_subscription'] or item['subscription']['enabled'], 'Usage subscription requirement needs subscriptions')
        require(not item['included_credits']['enabled'] or (item['subscription']['enabled'] and 'credits' in modes), 'Included credits need subscriptions and credits usage')
        require(not item['addons']['enabled'] or 'credits' in modes, 'Addons need credits usage')
    return items


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'Duplicate JSON field')
        result[key] = value
    return result


def read_config(path):
    path = Path(path)
    require(not path.is_symlink() and path.is_file() and path.stat().st_size <= 1 << 20, 'Input must be a bounded regular file')
    try:
        return validate(json.loads(path.read_bytes(), object_pairs_hook=unique_object))
    except (json.JSONDecodeError, UnicodeError):
        raise PolicyError('Invalid JSON configuration') from None


def write_new(path, items):
    """Publish a complete file atomically and refuse to replace reviewed output."""
    path = Path(path)
    payload = json.dumps(items, ensure_ascii=False, indent=2) + '\n'
    require(len(payload.encode('utf-8')) <= 1 << 20, 'Configuration exceeds file size limit')
    descriptor, temporary = tempfile.mkstemp(prefix='.' + path.name + '-', dir=path.parent)
    try:
        with os.fdopen(descriptor, 'w') as output:
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
        os.link(temporary, path)
    finally:
        os.unlink(temporary)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    prepare = commands.add_parser('prepare', help='Create a new deployment file for review; does not deploy')
    source = prepare.add_mutually_exclusive_group(required=True)
    source.add_argument('--preset', choices=PRESETS)
    source.add_argument('--input', help='Existing complete custom or preset JSON array to validate and copy')
    prepare.add_argument('--product-id')
    prepare.add_argument('--version', type=int)
    prepare.add_argument('--output', required=True, help='New file; existing files are never overwritten')
    check = commands.add_parser('validate', help='Validate a deployment file without changing it')
    check.add_argument('--input', required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == 'validate':
            items = read_config(args.input)
            print(f'Validated {len(items)} billing profile(s). No changes applied.')
            return 0
        if args.input:
            require(args.product_id is None and args.version is None, '--input cannot be combined with product/version overrides')
            items = read_config(args.input)
        else:
            items = validate([{'product_id': args.product_id, 'version': args.version if args.version is not None else 1, 'preset': args.preset}])
        write_new(args.output, items)
        print(f'Prepared {len(items)} billing profile(s). Review and deploy with PRODUCT_BILLING_FILE.')
        return 0
    except (PolicyError, OSError, TypeError):
        # Never echo untrusted JSON, URLs, environment values or exception details.
        print('Billing configuration rejected; check schema, composition, version and file paths. Output must be a new file.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
