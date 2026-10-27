# UniHub first-phase server bundle

This deployment is a persistent **sandbox**. It uses the approved development-mode guards (`APP_ENV=development`, `SANDBOX_CATALOG=true`, `PAYMENT_MODE=sandbox`) and listens only on loopback. The public console and identity issuer are both `https://unihub.quukk.com`. Do not describe deployment success as real payment/model-provider acceptance.

## Audited backend and gated headless upgrade

The server can register the locally built official-source Casdoor audit backend from `deploy/identity/build.py`. It stores an **actual Docker immutable local image ID**, never an invented repository digest. PostgreSQL and Formance pins remain unchanged. Registering an image checks the source-manifest lock, backend artifact SHA-256, Linux amd64/user1000/entrypoint contract, the official runtime's layer prefix plus exactly two added layers, and `/server` inside a network-isolated read-only container. The existing UI/Swagger/config mounts remain unchanged.

Build the custom image on an authorized build machine and preserve its public `image-context` (server, source manifest and licenses). Transfer the image with `docker save`/`docker load` or an approved registry; preserve the original official runtime reference for base-layer verification. Place the public artifacts under the immutable release, for example `identity-backend/`. No private `.local` tree, users or credentials belongs in the release.

Use the following sequence on the deployment host, substituting an actual release and approved registry file:

```sh
python3 /opt/unihub/releases/<revision>/deploy/server/bootstrap.py prepare --release /opt/unihub/releases/<revision>
python3 /opt/unihub/releases/<revision>/deploy/server/bootstrap.py register-identity-image --release /opt/unihub/releases/<revision> --identity-artifacts /opt/unihub/releases/<revision>/identity-backend --image unihub/casdoor:v4.23.0-audit-v1
python3 /opt/unihub/releases/<revision>/deploy/server/bootstrap.py stage-headless --release /opt/unihub/releases/<revision> --registry /opt/unihub/releases/<revision>/products.approved.json
python3 /opt/unihub/releases/<revision>/deploy/server/bootstrap.py up --release /opt/unihub/releases/<revision>
```

`register-identity-image` validates the current installation's labels, Compose project/service, ports, network and mounts before recording `shared/identity-image.json`. During this controlled transition it accepts the currently owned official image or the previously registered custom ID. After `up` verifies the running new image and backend hash, it marks the transition complete and no longer accepts the old image. A different image/binary resets the privacy gate. Custom images use `pull_policy=never`; unregistered initial installations retain the pinned official image with `pull_policy=missing`.

`stage-headless` validates strict public policy, generates independent management/key/per-product secrets **only when adding the capability**, and preserves every existing installation secret and user ID. Subsequent missing headless secrets fail rather than rotate. It adds a non-shared `app-unihub-provisioner` in the `unihub` organization, with no interactive login, public registration, redirects or grant types. Its Basic management credential acts only as an admin of `unihub` under fixed Casdoor's native organization checks, and stays in private root0600 runtime configuration. It is never registered as a public product. Product apps keep authorization_code/PKCE and remain non-shared. Approved registration enables the named product's native registration policy, with username/password required and optional email with no verification; the public nginx `/api/signup` route remains blocked and UniHub's runtime identity gate stays off until acceptance. The managed description and full policy match `scripts/product_client.py`, so its independent readback remains usable. Existing application names/client IDs/secrets/organization/policies are verified; conflicts fail without resetting them.

Public approved policies must use distinct new client/application IDs, separate from `unihub-console`, legacy `superwagie-unihub` and `unihub-provisioner`. See `docs/headless-product-api.md` for fields and scope meanings. The draft may have `password_login`/`registration` true. The bootstrap publishes it as `/opt/unihub/headless-config/products.draft.json` with mode0644 inside a root0755 directory. The nonroot `unihub` service can read the separate active `/opt/unihub/headless-config/products.json`; while the privacy gate is false, the active file forcibly disables both identity flags. Public files contain no secrets and are outside the private mode0700 `shared` tree. Financial scopes still require current user financial RBAC.

Use `products.draft.json` only for a controlled private acceptance server/probe. Verify actual canonical adapter registration, login, nonce/state/PKCE and token exchange against the patched IdP. Inspect the resulting database audit rows, actual enabled external sinks, and proxy/application/container logs for synthetic secret markers. The public UniHub service remains disabled until evidence is accepted. If no webhook/audit exporter is configured, the report must explicitly record that mode and source-function evidence; it must not describe a nonexistent external export as tested.

`enable-headless` consumes a fresh real-service report, verifies the currently owned/running image and `/server` again, validates report/proof-file SHA-256 values and binds the report to the exact staged draft hash. It does not infer acceptance from image labels or source unit tests:

```sh
python3 /opt/unihub/releases/<revision>/deploy/server/bootstrap.py enable-headless --release /opt/unihub/releases/<revision> --privacy-report /opt/unihub/acceptance/<run>/privacy-report.json
systemctl restart unihub
```

The report schema is `schema_version=1`, `installation_id`, `image_id`, `backend_sha256`, `policy_sha256`, UTC `verified_at` no older than24h, boolean `checks` for registration/password_login/pkce_nonce_state/audit_records_redacted/webhook_payloads_redacted/proxy_logs_redacted, and nonempty `evidence_files=[{path,sha256}]` relative to the report directory. Every proof must exist and match. `sink_modes` is preserved in the acceptance state. The gate covers canonical requests created by UniHub's adapter, not all equivalent arbitrary encodings sent directly to Casdoor.

Repeated prepare and unchanged policy staging preserve an accepted gate for the same image/backend/policy. Changed policy or image closes it. `disable-headless` is a separate command with the same root/release flags; restart `unihub` afterward. Neither preparation nor enable/disable commands restart the application automatically. The loopback-only Go listener enables `HEADLESS_TRUST_LOOPBACK_PROXY=true`; nginx replaces `X-Real-IP` with `$remote_addr`. By default this is the connecting peer; an explicitly trusted ingress can restore the original client address as described below. A shared outer proxy otherwise makes all of its clients use the same source-IP rate-limit bucket.

Run `python3 deploy/server/test-headless-deployment.py` plus the existing deployment/identity scripts. For the isolated PostgreSQL policy regression, set `UNIHUB_TEST_DATABASE_URL` in the process environment. It uses a random temporary schema, validates real insert/idempotency/conflict behavior, then removes only that schema. No production server is accessed by these tests.

## Optional trusted ingress and registration source IP

`nginx.conf` includes `/etc/nginx/snippets/unihub-trusted-ingress-*.conf` **only inside the UniHub HTTPS server**. With no matching file, this bundle trusts no ingress and overwrites caller-supplied `X-Real-IP` with the connecting peer address. The distributed `.conf.example` does not match the include pattern and is never enabled by copying the release. Keep the backend listener on loopback and its existing loopback-proxy trust setting; do not expose it to bypass nginx.

When an outer HTTP proxy forwards to origin nginx, the observed TCP peer is the proxy. Without an opt-in snippet, clients behind that peer share a registration source-IP bucket. Before enabling trust, verify the exact peer address seen at the origin and prove that the outer proxy **overwrites** `X-Real-IP` with the actual client address, including when callers supply forged `X-Real-IP` and `X-Forwarded-For`. A TCP passthrough or a proxy that preserves caller-supplied values does not satisfy this contract. Verify each hop using a disposable read-only diagnostic; do not consume real registration attempts or log cookies, credentials or callback queries. Do not enable trust until these checks pass.

Use [unihub-trusted-ingress.conf.example](unihub-trusted-ingress.conf.example) to prepare one reviewed local snippet. Replace `<TRUSTED_INGRESS_IP>` with the **single exact IP address** of the connecting ingress. Do not use `0.0.0.0/0`, `::/0`, any subnet, hostname, wildcard or a client-provided value. Keep `real_ip_header X-Real-IP` and `real_ip_recursive off`; this contract uses one authoritative header from the directly connected trusted peer, not an arbitrary forwarding chain. If several fixed peers are required, review each exact address and place their `set_real_ip_from` lines in the same snippet, with the header/recursive directives once. Do not create overlapping snippets with duplicate directives.

On the deployment host, first review the active vhost and all existing snippets matching that pattern. Preserve any existing managed configuration before editing it; do not overwrite an unknown file. Install the updated supplied vhost through the existing reviewed deployment procedure. For a first-time opt-in, save the edited example as `/tmp/unihub-trusted-ingress-reviewed.conf`, then run:

```sh
# Inspect nginx -V privately: the realip module must be available.
nginx -V
install -d -m 0755 /etc/nginx/snippets
# Abort if this name already exists; review/backup it separately instead.
test ! -e /etc/nginx/snippets/unihub-trusted-ingress-edge.conf || exit 1
install -m 0644 /tmp/unihub-trusted-ingress-reviewed.conf /etc/nginx/snippets/unihub-trusted-ingress-edge.conf
if nginx -t; then
  systemctl reload nginx
else
  mv /etc/nginx/snippets/unihub-trusted-ingress-edge.conf /etc/nginx/snippets/unihub-trusted-ingress-edge.conf.disabled
  exit 1
fi
```

A syntax check does not prove the IP provenance. After reload, repeat the read-only edge/direct-origin probes: an actual client through the verified ingress must retain its address, forged caller headers must have no effect through that ingress, and any untrusted direct-origin connection must retain its own socket address. Confirm the expected source reaches the loopback backend without changing public routes or dependencies. These deployment checks establish the source-IP input; registration policy and its counters require separate application tests. If any verification fails, disable the new snippet and reload only after a successful config check:

```sh
mv /etc/nginx/snippets/unihub-trusted-ingress-edge.conf /etc/nginx/snippets/unihub-trusted-ingress-edge.conf.disabled
nginx -t && systemctl reload nginx
```

For an update of an existing snippet, rollback restores its saved reviewed contents instead. Removing the opt-in restores peer-address bucketing; it does not undo registrations or reset application counters. Keep a restricted operational copy of enabled snippets with deployment recovery material: the bundle does not install, discover or back up host `/etc/nginx` snippets automatically. Reverify trust whenever the ingress path or address changes. A local regression is available with `python3 scripts/test-server-deployment.py`; it runs the actual template and sample in disposable nginx processes, testing absent snippets, exact-peer preservation, an untrusted IPv6 peer, alternate forwarding headers, and a header-overwriting edge. It does not modify a deployed server.

## Explicit product billing staging

Preparing or upgrading a release does not select a new billing mode. With no staged billing file, `PRODUCT_BILLING_FILE` remains unset and the existing v0.1.0 products retain their version-0 hybrid behavior. An operator must explicitly review and stage a public billing JSON array to opt in. Billing policy is independent of the headless client registry: staging creates no client credentials, rotates no existing secrets, and preserves the identity image, client policy and accepted headless audit gate.

Copy a reviewed file containing at most 100 profiles into the new immutable release, for example `/opt/unihub/releases/<revision>/billing.approved.json`. A preset entry is `{"product_id":"my-product","version":1,"preset":"prepaid"}`; supported presets are `hybrid`, `prepaid` and `subscription`. Custom entries must provide all four complete components (`subscription`, `included_credits`, `addons`, `usage`); see [composable billing](../../docs/composable-billing.md) and [examples](../../examples/billing). Every version must be a positive int64. Unknown or private fields, duplicate fields/products, mixed preset/components, invalid funding combinations, symlinks, hardlinks and nonregular files are rejected. Files contain public commercial metadata only; never include tokens, passwords, client secrets, payment keys or model credentials.

Run as root on the deployment host only after reviewing the exact profiles:

```sh
python3 /opt/unihub/releases/<revision>/deploy/server/bootstrap.py stage-billing \
  --release /opt/unihub/releases/<revision> \
  --billing-profiles /opt/unihub/releases/<revision>/billing.approved.json
```

This command atomically publishes `/opt/unihub/billing-config/products.json` as root:root0644 inside a root:root0755 directory, outside private `shared`. It regenerates private `runtime.env` with that public path in `PRODUCT_BILLING_FILE`; subsequent `prepare` and other bootstrap modes preserve it. The nonroot service can read the profiles. Invalid existing staged configuration fails preparation instead of silently reverting to legacy behavior. No application or dependency restart occurs during staging. After the normal release switch and review, load the staged versions by restarting only UniHub:

```sh
systemctl restart unihub
```

The trusted backend startup creates the configured product catalog rows and atomically appends billing profile versions. It does not grant identity-client access or publish prices, Offers, cash rates, real payment merchants or model-provider credentials. Versions must increase; the same version is idempotent only when its normalized profile matches, while a lower version or changed content at the same version fails startup. Changing an existing product needs an explicit higher version. Existing paid orders, rights and in-flight usage retain their original terms. Removing a staged file does not undo versions already saved in the database. Prepaid usage consumes the payer's llmrouter product wallet CNY/6 balance only when a separately published compatible rate and trusted usage flow exist; this staging step is not payment or billing-flow acceptance.

Run the offline staging/prepare checks from the repository:

```sh
python3 deploy/server/test-billing-deployment.py
python3 scripts/test-server-deployment.py
python3 deploy/server/test-headless-deployment.py
```

These tests use disposable local files, validate preserved credentials and an accepted identity gate, exercise public access permissions, and inspect a configuration archive. They do not change the deployed server or prove PostgreSQL/Formance/payment acceptance. `backup.py` includes the staged `billing-config` directory with private runtime configuration; backups remain restricted because the other archived files contain secrets.

## Artifact and ownership contract

Run on Ubuntu 24.04 x86_64 as root with Python 3, Docker Engine/Compose and nginx already installed. No package installer, firewall change, unrelated restart or existing service removal is performed by this bundle. Existing ports 55438, 53068, 58000 and Go 8088 must be available; public HTTPS443 forwards to this host's 8484. This hostname is HTTPS-only: its dedicated port80 vhost closes HTTP connections with nginx444, preventing fallback to another hosted site. Existing wildcard certificate/key are read at `/etc/nginx/ssl/_.quukk.com_chain.pem` and `/etc/nginx/ssl/_.quukk.com_key.key`.

Place a release directly under `/opt/unihub/releases/<revision>` containing:

- `bin/unihub`: Linux amd64 executable.
- `web/dist/`: built console, with Vite `/console-assets/` and flat `/assets/unihub-{logo,icon}-v1.png` product assets.
- `casdoor-web/`: complete verified Casdoor frontend overlay, including `swagger/` and original `assets/unihub/unihub-{logo,icon}-v1.png` branding.
- `deploy/server/`: this bundle.
- A `manifest.json`, `release-manifest.json` or `REVISION` identifying the release, ideally including build checksums.

Release/static directories must be root-owned and traversable/readable by the `unihub` service user and Casdoor uid1000 (directories0755, public files0644, binary0755). Never copy `.local`, local databases, users, tokens or dev environment files into a release.

The generated private tree is `/opt/unihub/shared` (root:root0700). `credentials.json`, `runtime.env`, `compose.env` are root0600. Casdoor's exact directory bind mount uses `shared/casdoor/conf` root:1000 mode0750 and files0640, readable only by root and the container group. Mounting that directory through Docker does not require uid1000 to traverse the host's private parent. systemd reads `EnvironmentFile` as root before running Go as the dedicated non-root `unihub` user.

The dedicated Compose project is `unihub-server`, container names `unihub-server-{postgres,ledger,casdoor}`, network `unihub-server`, and volumes `unihub-server-postgres` / `unihub-server-casdoor-logs`. Every managed resource has installation-specific labels. Bootstrap refuses foreign resource ownership, unexpected mounts/images/ports/networks, missing original secrets, occupied dependency ports and symlinked managed paths. It never recovers credentials from unrelated resources or resets passwords.

## Initial preparation and startup

Use a concrete release path in the following commands. Bootstrap preserves all previously generated secrets and user IDs. Running prepare repeatedly is safe. Failure messages withhold subprocess output because Docker/SQL errors can repeat credentials.

```sh
python3 /opt/unihub/releases/<revision>/deploy/server/bootstrap.py prepare --release /opt/unihub/releases/<revision>
```

Before starting or upgrading Casdoor, prepare its **release copy** after the verified branding build:

```sh
python3 /opt/unihub/releases/<revision>/deploy/server/prepare-identity.py --web /opt/unihub/releases/<revision>/casdoor-web
```

Casdoor 4.23.0 classifies every same-origin redirect as its own login when its base path is empty, converting the console's authorization-code request to an ordinary login and sending the browser to `/apps`. This reviewed frontend overlay gives only `https://unihub.quukk.com/auth/callback`, on that same browser origin and with no query, fragment or user information, an exception in `isSelfRedirectUri`. Other redirects retain the upstream behavior. It changes no server validation, state, PKCE, token or audience checks. The input is locked to the reviewed branded main bundle SHA-256; drift or an unexpected replacement count fails before publication. A changed branding build requires a separate review of this pin.

The script publishes a newly versioned complete JS/CSS module graph, retains old assets for open tabs, updates the entry point last and preserves the existing branding audit. The public `casdoor-web/unihub-identity-manifest.json` records the specification, source/output SHA-256 values and asset mapping. Repeated preparation verifies those hashes without changing bytes. Run `python3 scripts/test-server-identity.py` from the repository for temporary-directory regression checks; it exercises the real public bundle when available. Apply this script to the release copy only; keep the local branding source overlay unchanged.

Operator credentials are only in `/opt/unihub/shared/credentials.json`; open that file privately without copying its values into shell arguments, tickets or terminal transcripts. It contains ordinary `owner` and disposable `acceptance` identities in the `unihub` organization, plus a separate built-in administration password. All independent passwords, model-service token, payment signing key and OIDC client secrets are generated server-side as 32-byte hexadecimal randomness.

The console OIDC client is `unihub-console` with only `https://unihub.quukk.com/auth/callback`. SuperWagie's public PKCE client is `superwagie-unihub`, with only `http://127.0.0.1:57991/api/platform-account/callback` and `http://localhost:57991/api/platform-account/callback`. Its undistributed random client secret stays private; the pinned Casdoor version permits public exchange with a valid S256 verifier. Both clients use `authorization_code`, `JWT-Standard`, generated `cert-built-in` RS256 signing material and one-hour tokens. Business-app signup is disabled in the fresh seed; a narrow startup SQL update also disables signup on the upstream built-in administration application and verifies that change. It preserves all other built-in authentication defaults and key material.

**Install and validate the supplied nginx vhost before starting Casdoor**, so initial closed enrollment is enforced at `/api/signup` from the first request. The nginx file belongs inside the existing `http` context, normally `/etc/nginx/conf.d/unihub.conf`. Compare any pre-existing file of that name rather than replacing an unknown file. Only this hostname's vhost is added; retain existing configuration/certificate files and baseline probes. Its access log records method and normalized `$uri` without query strings, so authorization codes/state are not recorded. This vhost overrides inherited error logging with `/dev/null crit`; nginx upstream errors otherwise include the full callback URI. Use its query-free access status and the application journal for diagnostics. Identity `/api`, discovery, `/login`, assets, Swagger and secondary login routes go to Casdoor; `/auth`, `/v1`, console assets and `/` go to Go; `/internal` is blocked. Casdoor retains its authentication on its own API. No global Go CSP is applied to Casdoor. nginx >=1.19.3 is required for the explicit `casdoor_session_id` Secure/HttpOnly/SameSite=Lax cookie flags on the HTTPS vhost; verify the actual login response without logging cookie values.

The identity administrator enters at [`/login/built-in?from=%2Fusers`](https://unihub.quukk.com/login/built-in?from=%2Fusers) using the existing `built-in/admin` account and its private administration password. When a different-organization ordinary session is already present, the verified `/login/built-in?from=%2Fusers&silentSignin=1` entry keeps the built-in form visible; it is not a universal force-account-switch flag. The pinned frontend accepts the relative `from` destination and navigates to the user list after login; omitting it defaults to `/`, which serves the Go console on this deployment. nginx routes the canonical `/users`, `/organizations` and `/applications` paths and their slash subpaths to Casdoor, so user details and related administration pages also survive direct loading and refresh. These page routes retain Casdoor's API authentication and authorization. Use separate browser sessions or log out of Casdoor before comparing administrator and ordinary-user behavior: opening the administrator login URL reuses an existing identity session. Ordinary `unihub/owner` and disposable `unihub/acceptance` accounts continue to enter the Go console through `/auth/login`.

```sh
install -m 0644 /opt/unihub/releases/<revision>/deploy/server/nginx.conf /etc/nginx/conf.d/unihub.conf
nginx -t
systemctl reload nginx
python3 /opt/unihub/releases/<revision>/deploy/server/bootstrap.py up --release /opt/unihub/releases/<revision>
```

`up` initializes independent roles/database owners (`unihub_app`, `unihub_ledger`, `unihub_casdoor`) with a separate admin role. All three databases revoke PUBLIC access. Creation uses PostgreSQL16-compatible `psql \gexec` outside transactions. It starts only this Compose stack, applies narrowly scoped branding and closed-enrollment changes, then verifies HTTPS discovery/RS256 endpoints. It does not install or activate the Go service.

Create the dedicated user if absent; an existing account of that name needs explicit ownership verification. Set `/opt/unihub/current` using a new temporary symlink followed by an atomic rename. These are operator steps so an unknown pre-existing file is never automatically overwritten:

```sh
useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin unihub
ln -s /opt/unihub/releases/<revision> /opt/unihub/.current-next
mv -Tf /opt/unihub/.current-next /opt/unihub/current
install -m 0644 /opt/unihub/releases/<revision>/deploy/server/unihub.service /etc/systemd/system/unihub.service
systemd-analyze verify /etc/systemd/system/unihub.service
systemctl daemon-reload
systemctl enable --now unihub.service
```

The service runs migrations as its application role before serving. Run fresh acceptance through public HTTPS: TLS and HTTP connection rejection, same-origin discovery, private owner and acceptance browser logins, Secure HttpOnly session cookies, sandbox catalog and subscription/credit flow, separate account/team isolation, `/internal` denial, `/api/signup`403 and unchanged probes for the host's existing applications. Check restart persistence and that dependency ports remain loopback. An initial successful process/health check alone does not prove browser or billing acceptance.

## Backup and isolated recovery verification

Create backups before upgrades and after important accepted changes:

```sh
python3 /opt/unihub/current/deploy/server/backup.py
python3 /opt/unihub/current/deploy/server/verify-restore.py /opt/unihub/backups/<backup-name>
```

`backup.py` uses **online PostgreSQL16 physical full-cluster** `pg_basebackup -F tar -X fetch`, including required WAL for one consistent recovery point covering `unihub`, `ledger` and `casdoor`. Casdoor certificate/private-key tables are included in the same cluster. It also archives exact private config/seed/credential files and the deployment bundle/available release manifest. It writes an SHA256 manifest, root0700 backup directory and root0600 archives/manifest. Failed backups retain an `INCOMPLETE` marker and are not eligible for restore verification. Backups include credentials and identity keys; copy them only to a separately authorized restricted destination, and retain the corresponding immutable release artifacts. Backups do not include the wildcard TLS private key or unrelated applications/data.

`verify-restore.py` verifies checksums, creates a fresh randomly named `unihub-restore-*` volume, unpacks/chowns/chmods only that new volume, executes `pg_verifybackup`, starts a disposable PostgreSQL16 container with **no network and no published ports**, then checks the three database owners, schema readability and signing-key presence without printing key contents. The default removes only its own created container/volume. `--retain` keeps that isolated copy for additional private inspection. No live container, volume or database is renamed, removed, mounted or queried by this restore test. A verified physical restore still requires an isolated Casdoor/Go login and business-flow test before claiming full application recovery.

After the initial manual backup and isolated restore have both passed, install the optional root-only daily timer. The verified manual backup creates `/opt/unihub/backups` with mode0700, required by the service's restricted write path. The schedule is explicitly 03:30 Asia/Shanghai, with up to five minutes of jitter and one catch-up run after downtime. No automatic deletion or off-host transfer occurs; monitor available disk and review retention separately.

```sh
install -m 0644 /opt/unihub/current/deploy/server/unihub-backup.service /etc/systemd/system/unihub-backup.service
install -m 0644 /opt/unihub/current/deploy/server/unihub-backup.timer /etc/systemd/system/unihub-backup.timer
systemd-analyze verify /etc/systemd/system/unihub-backup.service /etc/systemd/system/unihub-backup.timer
systemctl daemon-reload
systemctl enable --now unihub-backup.timer
systemctl list-timers unihub-backup.timer
```

For a real recovery, first stop writes to this application's Go/dependency stack and preserve the old dedicated volume as evidence. Restore into a **new** UniHub-owned volume and isolated test tree, restore `configuration.tar` in a root-only staging directory, verify its installation identity and release manifest, and start an isolated identity/API stack with a temporary private hostname/HTTPS issuer configuration. Validate restored login/keys and account/wallet state before a separately reviewed cutover. Do not overwrite a running data directory, use another application's volume, or point the isolated test at the live issuer/data. This bundle deliberately provides no force-restore command for a live database.

## Upgrade, rollback and limitations

Keep the previous immutable release and a verified backup. Prepare the new release without changing `current`; `bootstrap.py up --release ...` changes only this project's stateless dependency definitions/overlay and preserves existing data/secrets. Review any dependency digest/config changes before doing so. Atomically switch `current`, restart `unihub`, then repeat public acceptance. Roll back binaries/static assets by atomically restoring `current` and restarting this one service. Database migrations may be forward-only; do not claim a binary rollback reverses schema/data changes. Recovering older data requires a reviewed restore to a new volume.

The bootstrap's fixed domain, namespace, callbacks and ports are deliberate boundaries. It does not configure real merchants/model providers, an operator audience, public registration, TLS renewal, off-host backup retention or unattended restore. It never uses the local dev setup script as an imported module.
