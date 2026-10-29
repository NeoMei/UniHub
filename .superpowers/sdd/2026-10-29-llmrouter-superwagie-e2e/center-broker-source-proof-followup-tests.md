# Center broker source-proof review follow-up

Date: 2026-10-30 (Asia/Shanghai)

## Changes

Tests only. The implementation source from `87b16c6e58fe81cc44870345c980d1966a6a8423` is unchanged.

- Added `internal/httpapi/product_browser_source_integration_test.go`, an isolated-schema PostgreSQL test using a deliberately seeded `browser_sessions` row and actual product authorize/exchange/source-verify HTTP routes. This is a controlled central session fixture, not a claim of real IdP/browser acceptance.
- Added independently mutated facts, each starting with a valid proof and confirming success first: source-session deletion (including proof-row FK cascade), session cutoff, pending identity revocation, inactive global user, inactive destination product access, wrong product, wrong instance, wrong configured client, and native-origin auth-version change while the central browser-session row remains present.
- The test also checks exchange denial when a pending code's source session is deleted, the 64 lowercase-hex token shape, SHA-256-only storage, and that issuance/verification does not create extra central users or identities.
- Corrected the existing legacy source-less pending-code test to reuse the already-created destination binding. Creating a second local binding for the same product/user violated the repository's intentional uniqueness rule; no binding policy was changed.

## Tests and execution boundary

- New DB test: `TestBrowserSourceProofCentralSessionRevocationAndScope`.
- Existing DB test: `TestNativeBrowserHandoffCreatesCentralSessionAndSecondProductBinding`.
- `go test ./internal/httpapi -run '^$' -count=1` passed, compiling the new database test and existing integration fixture.
- Attempted both DB tests locally; both stopped immediately because `UNIHUB_TEST_DATABASE_URL` is unset. No database assertions ran here. The coordinator owns running the complete HTTP integration package against isolated PostgreSQL.

## Files

- `internal/httpapi/product_browser_source_integration_test.go`
- `internal/httpapi/product_browser_test.go`
- This report.
