#!/bin/sh
set -eu
here=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
source_repo=${1:?Pass the path to the Logto Git checkout}
output=${2:-$here}
base=d179bf9d2d51104236a342b90c2841df66e4ba5b
test "$(git -C "$source_repo" rev-parse 'v1.46.0^{commit}')" = "$base"
mkdir -p "$output"
output=$(cd "$output" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/logto-portal.XXXXXXXX")
trap 'rm -rf "$work"' EXIT
git clone -q --local --no-hardlinks --no-checkout "$source_repo" "$work/src"
cd "$work/src"
git checkout -q --detach "$base"
git apply --check "$here/logto-1.46.0.patch"
git apply "$here/logto-1.46.0.patch"
export CI=true PUPPETEER_SKIP_DOWNLOAD=true npm_config_cache="$work/npm-cache"
export NODE_OPTIONS="${NODE_OPTIONS:---max-old-space-size=6000}"
npm install --prefix "$work/tools" --no-save --no-audit --no-fund pnpm@10.28.2
export PATH="$work/tools/node_modules/.bin:$PATH"
pnpm install --frozen-lockfile --store-dir "$work/store"
pnpm --workspace-concurrency=3 --filter='@logto/core^...' --filter='@logto/cli...' \
    --filter='!@logto/console' --filter='!@logto/experience' --filter='!@logto/account' \
    --filter='!@logto/demo-app' --filter='!@logto/device-demo-app' -r build
ADMIN_CONSOLE_BASE_PATH=/auth ADMIN_ACCOUNT_BASE_PATH=/auth ADMIN_EXPERIENCE_BASE_PATH=/auth \
    pnpm --workspace-concurrency=3 --filter @logto/core --filter @logto/console \
        --filter @logto/experience --filter @logto/account -r build
python3 "$here/pack-overlay.py" "$work/src" "$output"
