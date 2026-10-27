# Product user foundation integration

The source contract is `0.1.0`, Go 1.23. The independent module is
`github.com/NeoMei/UniHub/packages/userkit-go`. It has not been published and this
repository supplies no source license. A future nested-module release would use
`packages/userkit-go/v0.x.y`; do not assume that tag exists.

## Generate a product

From a reviewed UniHub checkout:

```sh
python3 scripts/product_user_init.py \
  --output /tmp/my-notes \
  --product-id notes \
  --module example.test/my-notes
cd /tmp/my-notes
go test ./...
cp config.example.json config.json
chmod 600 config.json
# Edit config.json with your exact backend identity configuration.
PRODUCT_CONFIG=./config.json go run .
```

The output must not already exist. The generated checkout has its own `go.mod`,
`go.sum`, Browser/Client/Service module source snapshot, sorted SHA256 provenance,
public product manifest, config example and tests. It has no sibling-repository
imports, embedded secret or automated client enrollment. You can move it to
another machine with Go 1.23 and build without a UniHub checkout.

`examples/product-users` contains the same runnable browser server source. From
that directory `go test -race ./...` checks stable issuer+subject identities,
concurrent initialization, durable notebook defaults, failed transaction rollback
and the intersection of central permissions with product resource ownership.

## Explicit operator enrollment

The product service never receives IdP management credentials. The operator uses
an independent private management client for this protected workflow:

```sh
python3 scripts/product_client.py prepare \
  --registry config/product-clients.json --output /tmp/notes-plan.json \
  --product-id notes --instance-id default \
  --client-id notes-local --application app-notes-local \
  --redirect-uri http://127.0.0.1:58120/auth/callback \
  --origin http://127.0.0.1:58120 \
  --scope access:read --browser-sso
# Review the public plan. Supply management ID/secret privately in the environment.
python3 scripts/product_client.py apply --plan /tmp/notes-plan.json \
  --issuer http://localhost:58000
python3 scripts/product_client.py readback --plan /tmp/notes-plan.json \
  --issuer http://localhost:58000
```

`access:read` alone permits the kit's `/v1/capabilities` and
`/v1/me/product-access` checks. Add `identity:read` only if your application needs
`/v1/me`, and add account/billing scopes only for those features. When the selected
scopes contain only access, identity, profile, account or member capabilities,
`prepare` defaults `billing_products` to `[]`. This sample therefore creates no
commercial product row and starts even if a same-slug commercial product is
disabled. A commercial scope defaults the mapping to `[product_id]`; omitting
`--scope` preserves the legacy `accounts:read,catalog:read` commercial default.
Explicit `--billing-product` values are always preserved and opt into commercial
registration even with access-only scopes. Review that mapping before apply.

`browser_sso` defaults false for legacy clients. Explicit true enables the
managed Casdoor application's interactive Password method, sign-in session and
automatic sign-in, independently of headless `password_login` and registration.
It does not grant administrative authority, new self-registration or central
roles. Apply/readback checks the exact managed policy and callback/audience;
unknown or independently configured application ownership is refused.

Append the generated manifest object to the trusted array loaded through
`PRODUCT_USER_MANIFESTS_FILE`; load the approved registry using
`PRODUCT_CLIENTS_FILE`, then restart/reload your deployment as appropriate. Never
replace unrelated registry entries. The central directory is independent of
billing registration. Default `member` grants `product:use` and `article:create`;
privileged product roles require explicit central assignment.

Backend configuration is explicit: `mode`, `listen`, `public_url`, `oidc_issuer`,
`client_id`, optional private `client_secret`, `product_id`, `instance_id`, private
`database`, and for UniHub mode `api_url` plus `central_issuer`. The latter is the
canonical business issuer; it may differ from the OIDC issuer. Register exactly
`<public_url>/auth/callback` for this starter, `/api/unihub/callback` for LLMRouter,
and `/auth/callback` for the central console. HTTP is loopback development only.
Casdoor uses `access_token_type: "access-token"`; an opaque-token provider needs a
separately verified provider adapter.

## Runtime and extension points

Browser performs standard authorization-code/S256 OIDC with signed ID/access
verification, state/nonce and browser binding. Tokens remain on the server;
HttpOnly random cookies, current profile/permissions and a CSRF token reach the
browser. Client proves the exact central product/client/instance on every request.
Service composes Provider, product-owned Provisioner and ResourcePolicy. Product
policies can restrict central access; they cannot override central deny.

The sample provisioner atomically commits stable identity, product profile and
notebook defaults in one fsynced/renamed private JSON snapshot. One process owns a
database, enforced by an exclusive lock file. Repeated and concurrent access
initializes once. SIGINT/SIGTERM stop HTTP gracefully and release the lock; failed
OIDC/provider/listener startup also releases it so a corrected configuration can
retry. After an abrupt crash or SIGKILL verify the owner is dead before removing its exact
`.lock` file. Production adapters should use a SQL transaction and unique
`(issuer,subject)` identity constraint. They should use durable authenticated
encrypted proof/session storage; this sample holds sessions/proofs in memory and
requires a new product login after restart. Its sessions expire at the earliest
verified OIDC/central proof expiry. Shared Browser's in-memory login transaction
store requires sticky routing; logout clears this product session, leaving the
issuer's shared sign-in session intact.

To use a product-owned standard IdP, set `mode: "standalone"` and configure its
issuer/client/exact callback. Shared Browser verifies signed proofs; the standalone
Provider accepts only the resulting server-held proof and grants local member
defaults through the same Service/Provisioner/ResourcePolicy. There is no password
crypto, central-role administration or online IdP revocation check in that mode;
verified expiry and local logout bound proof use. This is an explicit integration
seam, not a complete standalone identity provider.

`GET /api/me` exposes current Access plus the local profile;
`POST /api/articles` requires `article:create` and the current local owner;
`POST /logout` clears the session. Writes require exact Origin and
`X-Product-CSRF`. Never persist elevated central permissions as permanent local
roles, quotas or credits.

## Upgrade and acceptance

Regenerate into a new output directory from a reviewed committed shared module,
compare `third_party/unihub-userkit/PROVENANCE.json` source revision and sorted
hashes, review API changes, migrate your product extensions and run generated
consumer tests. The initializer refuses a dirty shared module so provenance is
reproducible. Do not replace the relative snapshot with an absolute sibling path.

The actual local Casdoor/central/LRouter/generated-product acceptance record is
[2026-10-27 acceptance](product-user-foundation-acceptance-2026-10-27.md).
Fixtures and mocked browser checks are separate from that real IdP evidence.
Production deployment, multi-replica sessions, credentials, HTTPS/cookie policy,
provider choice, signing and real supplier/payment behavior need their own checks.

### Reproduce isolated local acceptance

The harness has no production mode. Ports 58118–58121 must be free. The private
runtime supplies the existing **local** management client and local database/
ledger addresses. Recheck its provenance before use; no production values belong
in this run. Existing Casdoor organization policy must pass protected preflight,
and the local Casdoor webhook table must be empty. Setup creates only uniquely
named marked applications/users and a new central schema; it aborts on collision.
It never updates an existing IdP application or user.

```sh
python3 scripts/acceptance/product-users-local.py setup \
  --runtime .local/llmrouter-integration/runtime.json \
  --llmrouter ../llmrouter --run-dir /tmp/my-product-user-acceptance
python3 scripts/acceptance/product-users-local.py serve \
  --llmrouter ../llmrouter --run-dir /tmp/my-product-user-acceptance
# In another terminal, using an installed Playwright package and fresh contexts:
PLAYWRIGHT_MODULE="$PWD/../llmrouter/web/node_modules/@playwright/test" \
ACCEPTANCE_SCREENSHOT_DIR=/tmp/my-product-user-screenshots \
node scripts/acceptance/product-users-browser.cjs /tmp/my-product-user-acceptance
# Stop the supervisor with Ctrl+C, wait for exact handles to stop, then:
python3 scripts/acceptance/product-users-local.py cleanup \
  --runtime .local/llmrouter-integration/runtime.json \
  --run-dir /tmp/my-product-user-acceptance
```

Build both frontends first (`npm run build --prefix web` in UniHub and
`bun run build` in LLMRouter's `web` directory). The supervisor builds current Go
binaries, generates the second product, initializes only the new router database
through its private setup API and seeds one disabled loopback-only channel.
Controlled management setup creates two ordinary local test users; this does not
claim browser self-signup acceptance. Real browser login supplies central UUID;
only the dedicated central process restarts with that UUID as `USER_ADMIN_IDS`.
Central role/status changes use the UI and read back the real API. Browser requests
to nonlocal hosts abort; no fake provider response, cookie/token injection, HAR,
trace, password screenshot or storage-state export is used. Direct authorization
checks use the native session token obtained from its actual refresh response,
kept only in memory, including across central role revocation.

Cleanup verifies every recorded exact object and marker before deleting, reads
back absence, and drops only the recorded new schema. An uncertain mutation stops
for readback; never repeat setup against existing names. The unique Formance
ledger is retained because no supported exact-ledger deletion was established.
Delete the private run directory after recording sanitized results: it contains
backend credentials, test passwords, logs and the disposable product databases.
