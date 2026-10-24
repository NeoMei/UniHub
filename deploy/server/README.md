# UniHub first-phase server bundle

This deployment is a persistent **sandbox**. It uses the approved development-mode guards (`APP_ENV=development`, `SANDBOX_CATALOG=true`, `PAYMENT_MODE=sandbox`) and listens only on loopback. The public console and identity issuer are both `https://unihub.quukk.com`. Do not describe deployment success as real payment/model-provider acceptance.

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
