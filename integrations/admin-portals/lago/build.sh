#!/usr/bin/env bash
# Source is read from a local Lago remote/checkout. Build only in a fresh private clone.
set -euo pipefail
integration=$(cd "$(dirname "$0")" && pwd)
source_repo=${1:?Usage: build.sh LOCAL_CHINESE_GIT_CHECKOUT [NEW_OUTPUT_DIRECTORY]}
output=${2:-"$integration/stage"}
if [[ -e "$output" ]]; then echo "Choose a new output directory: $output" >&2; exit 1; fi
mkdir -p "$(dirname "$output")"
output="$(cd "$(dirname "$output")" && pwd)/$(basename "$output")"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/paylab-build.XXXXXX")
echo "Build checkout: $scratch/front"
git clone --quiet --no-checkout --local "$source_repo" "$scratch/front"
cd "$scratch/front"
git checkout --quiet --detach 1929e99148849758d9f8bfd60fc2cc94c1ab95cc
git apply "$integration/v1.57-chinese-source.patch"
mkdir -p translations/ui
git show be954532ff3cbbd37ed68578ea4a8fc49587b757:translations/ui/zh-CN.json > translations/ui/zh-CN.json
git apply "$integration/v1.57-chinese-catalog.patch"
test "$(git hash-object translations/ui/zh-CN.json)" = 717cb3a87d8f9744e7b99c77e048cfc10970fb09
git apply "$integration/paylab.patch"
export HUSKY=0 CYPRESS_INSTALL_BINARY=0
# Do not upload a source map even if the invoking shell is used for other releases.
unset SENTRY_AUTH_TOKEN
if [[ -n "${LAGO_RELEASE_DEPS:-}" ]]; then
  # Optional offline installation from this version's previously installed workspace.
  cmp pnpm-lock.yaml "$LAGO_RELEASE_DEPS/pnpm-lock.yaml"
  for workspace in . packages/configs packages/design-system; do
    cp -R "$LAGO_RELEASE_DEPS/$workspace/node_modules" "$workspace/"
  done
else
  corepack pnpm install --frozen-lockfile
fi
corepack pnpm --filter lago-design-system build
export APP_ENV=production APP_VERSION=be954532f LAGO_FRONT_BASE_PATH=/paylab/
export NODE_OPTIONS=--max-old-space-size=8192
corepack pnpm exec vite build
# Source maps aren't used by the UI and need not be mounted under a public asset URI.
find dist -name '*.map' -delete
test -f dist/favicon-prod.svg
rg -q '<base href="/paylab/"' dist/index.html
rg -q 'src="/paylab/assets/index\.' dist/index.html
mv dist "$output"
echo "Stage ready: $output"
