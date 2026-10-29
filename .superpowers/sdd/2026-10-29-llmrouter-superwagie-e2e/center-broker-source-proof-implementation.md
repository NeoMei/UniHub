# Center broker source-proof implementation

Date: 2026-10-30 (Asia/Shanghai)

## Base and scope

- Base: `2be2832e69105d961d11dee695f5a0612f6eda9e` on `codex/product-path-e2e`.
- Implemented the UniHub half of Task 1c only. The coordinator-owned plan edit was preserved and excluded from this change.
- No CodeGraph index exists in this checkout.
- Migration numbering was checked against the live migration directory: two `021_*.sql` migrations already exist, so the next unused migration is `022_product_browser_source_proof.sql`.

## Contract implemented

- Broker browser codes now retain only the SHA-256 central browser-session hash. The new `ubsp_` token has 256 random bits, is returned only by the private runtime exchange, and is stored only as SHA-256. Its proof row binds central user, source-session hash, exact client/product/instance, native source binding/version when present, and expiry capped at the central session expiry.
- Exchange requires a still-live source session, same linked central user, active account, current `session_not_before`, and no pending identity revocation when the code has a source hash. A legacy code without `source_session_hash` retains the original one-use `ubp_` bind behavior but receives no finance source proof.
- Source proof expiry is independent of the 120-second one-use `ubp_` grant and capped at the current central browser-session expiry; this allows reuse while the same source session remains live without weakening online revocation checks.
- `currentBrowser` now applies the cutoff and pending-revocation checks before broker code issuance. The same checks run at exchange and at every private source verification.
- The private `POST /internal/v1/product-runtime/identity/browser/source/verify` endpoint uses existing `identity:browser` authorization and the configured fixed `finance_client_id`; it rejects Origin, duplicate credentials/JSON keys, unknown or missing fields, oversized bodies, and mismatched client/product/instance/user. It returns 204 on success and never issues an actor or bearer.
- The backend userkit grant has optional source-proof fields for old response compatibility. `RuntimeClient.VerifyProductBrowserSource` uses the fixed endpoint and requires an empty 204 response; its HTTP client already rejects redirects.
- Existing `ubp_` one-use binding proof behavior and native/OIDC authorization paths remain unchanged.

## Files

- `internal/database/migrations/022_product_browser_source_proof.sql`
- `internal/productauth/browser_broker.go`
- `internal/productauth/browser_http.go`
- `internal/productauth/browser_source_test.go`
- `internal/httpapi/product_auth.go`
- `internal/httpapi/product_browser_test.go`
- `packages/userkit-go/product_browser.go`
- `packages/userkit-go/product_browser_test.go`
- `packages/userkit-go/runtime.go`

## Verification and boundaries

- Passed focused `internal/productauth` tests for proof token format, current session revocation gates, fail-closed DB rows, and existing browser-client scope validation.
- Passed `go test ./internal/httpapi -run '^$'` to compile the private route and the expanded PostgreSQL browser integration test.
- Passed all `packages/userkit-go` tests (`go test ./...` inside that module).
- The actual PostgreSQL browser integration test was not run: `UNIHUB_TEST_DATABASE_URL` is unset. It now covers positive source verification with a proof longer-lived than the initial bind grant, wrong subject/client, Origin, duplicate runtime credential, malformed/duplicate/oversized body, legacy pending-code bind without finance proof, and denial after source lifecycle revocation. The coordinator owns running real isolated PostgreSQL migration/server integration.
- No LLMRouter sealed source-proof persistence/callback/runtime-finance changes are included; that remains the peer-owned half. No SSH, network calls, paid calls, push, release, or deployment occurred.

## Review notes for the coordinator

- Please independently review before peer integration consumes this API. Particular checks: PostgreSQL acceptance of the `UPDATE ... FROM ... RETURNING` source-session query, migration/FK cascade under logout, and the current-native-origin lookup under auth-version/user/product disable.
- Per the source contract, this only prevents later authority resolution after revocation; it does not cancel a write already accepted before the concurrent central revocation.
