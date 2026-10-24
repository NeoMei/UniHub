# Casdoor audit privacy backend

The fixed official Casdoor `v4.23.0` tag resolves to commit `1b91d9d319a127e9b1af88fa7a20bb35c052b9d8`. Its audit constructor filters request query parameters and masks request bodies, but `secretRecordKeys` omits `nonce`, `state`, `code`, and `code_challenge`. `AddRecord` delivers the record to webhooks before inserting it into the record table, so a PostgreSQL trigger cannot protect that first external sink.

The native `logPostOnly` option only suppresses ordinary GET audit records. Authentication POST requests remain recorded. The logout GET exceptions are deliberately retained. Organization retention runs after record creation; omitting Audit providers or webhooks does not remove database records or sanitize them. Inspection of the pinned `object/record.go`, `routers/record.go`, `object/audit_provider.go`, `object/record_cleanup.go` and configuration found no native per-auth-endpoint audit disable/filter option that fixes this path.

Official owning sources: [record construction and sinks](https://github.com/casdoor/casdoor/blob/1b91d9d319a127e9b1af88fa7a20bb35c052b9d8/object/record.go), [router audit hook](https://github.com/casdoor/casdoor/blob/1b91d9d319a127e9b1af88fa7a20bb35c052b9d8/routers/record.go), [native configuration](https://github.com/casdoor/casdoor/blob/1b91d9d319a127e9b1af88fa7a20bb35c052b9d8/conf/app.conf), [official container](https://github.com/casdoor/casdoor/blob/1b91d9d319a127e9b1af88fa7a20bb35c052b9d8/Dockerfile).

`audit-redaction.patch` adds only a comment and these four names to `secretRecordKeys`. It changes audit redaction only. The existing `secretRecordQueries` automatically inherits the names, and body redaction runs during `NewRecord`, followed again by `AddRecord` before webhook delivery. Authentication, authorization, token verification, state checks, nonce checks and PKCE checks remain byte-for-byte upstream.

## Reproducible local build

Requires Python 3.12+, Go 1.26.5 and `patch`; Docker is needed only for the optional local image build. The tool uses the official immutable source archive plus upstream pinned `go.mod`/`go.sum`, the official Go module proxy/checksum database and the already pinned official Casdoor runtime. It reads no deployment environment, secret, account or database, and never pushes an image or connects to a deployment host.

```sh
python3 deploy/identity/build.py --work-dir .local/casdoor-audit-build --test-only
python3 deploy/identity/build.py --work-dir .local/casdoor-audit-build --image-tag unihub/casdoor:v4.23.0-audit-v1
```

The build verifies the archive SHA-256 and selected owning source hashes, reproduces the privacy failure on unmodified source, applies the exact patch without fuzz, verifies the patched file hash, and runs the source-function privacy tests again. It cross-builds a static `linux/amd64` server with `CGO_ENABLED=0`, `-mod=readonly` and an explicit source version/commit, then validates its ELF architecture.

`image-context/` contains only the binary, Dockerfile, preserved upstream Apache-2.0 `LICENSE`, modification `NOTICE`, `.dockerignore` and `source-manifest.json`. The manifest reports the exact upstream commit/archive, patch/file/binary hashes, toolchain and test results. A source-hash mismatch or failed privacy regression stops the build.

The Dockerfile inherits the exact existing `casbin/casdoor:v4.23.0@sha256:e13c9aada231f7338a03ab0ae7054d0a9480e77283338ab9fc319d88fe2b3ce0` image, replaces only `/server` and adds license/provenance files. The official runtime user, entrypoint, healthcheck and assets remain inherited. Existing verified `casdoor-web` and Swagger overlays, private `/conf` mount, volume, network, ports and database remain as deployed.

The server deployment bundle currently has an explicit official-image allowlist. Publishing this custom backend therefore also requires a reviewed immutable custom image ID/digest in `deploy/server/compose.yaml` and the matching bootstrap image allowlist. This tool intentionally does not edit either or restart a running service. Record the resolved custom image and backend binary hashes in the release manifest; use the existing exact UI overlay/config mounts when eventually applying it.

## Privacy evidence and gate

`run-privacy-tests.py` parses Go ASTs and extracts the actual pinned `NewRecord`, `AddRecord`, `maskSecrets`, declarations and `util.FilterQuery` function bodies verbatim. It uses the real pinned Beego request context. Only database/webhook/audit I/O and unrelated deterministic metadata helpers are substituted. This tests sanitizer and sink order behavior, rather than a rewritten sanitizer or a text assertion.

The regression uses synthetic password, nonce, state, authorization code, challenge and verifier values in repeated URL query values and JSON/form/multipart bodies. It observes each webhook/database/audit sink, checks those values are already absent, and verifies nonsecret application/client/redirect/method/action/response/status/IP/language fields remain. Direct `AddRecord` callers are also tested for body redaction, and existing password/token masks remain covered. The unmodified official source fails for the four missing keys; the two-line patch passes under the race detector.

This is source-function evidence. It does not prove a deployed IdP, all operational log sinks, or the complete headless login/registration flow. The manifest deliberately sets `deployed_idp_privacy_verified=false`; it must never automatically enable UniHub's `AuditRedactionVerified` gate. After installing a reviewed backend, a real IdP acceptance must verify that synthetic authentication bindings are absent from audit records and enabled webhook/audit exports before that gate can be enabled.

The minimal patch retains upstream regex behavior for bodies. Escaped JSON field names and percent-encoded form field names are not equivalent to a structured decoder in that upstream sanitizer. UniHub's adapter constructs its own JSON and query requests with fixed canonical field names. The privacy gate can cover those actual adapter-generated registration/login/token requests once their real audit/export/proxy sinks have passed marker-based acceptance. It does not certify arbitrary requests sent directly to Casdoor's public API, including equivalent encoded field names. Comprehensive Casdoor-wide body redaction would require a separately reviewed structured audit sanitizer. Keep this scope explicit in the acceptance report and do not weaken authentication validation to accommodate logging.
