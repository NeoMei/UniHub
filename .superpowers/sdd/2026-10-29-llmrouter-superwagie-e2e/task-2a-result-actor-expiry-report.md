# Task 2a: Verified result-reader access-token expiry

## Contract delivered

- `internal/httpapi/model.go`: the internal model-credential verification endpoint uses a response DTO embedding `modelaccess.Claims`, with optional `actor_expires_at` (`int64`, Unix seconds). Only successful `purpose=result_read` sets this field, directly from the verified actor access token's `auth.Claims.ExpiresAt`.
- `authorizeResultReadActor` returns verified expiry only after its existing product-verifier, issuer/subject, session, cutoff, identity, membership, product permission, context and final expiry checks succeed. Errors return zero plus the existing error. No stored grant, credential expiry, billing, SDK or package metadata changes.
- `internal/httpapi/result_read_identity_test.go`: extends the existing HTTP/PostgreSQL integration test using actual RSA-signed access tokens and OIDC discovery/JWKS. It checks the exact signed actor expiry, independently reads stored credential expiry to verify the original `expires_at`, and requires both expiries to differ. Ordinary verification/receipt and every denied response omit the new field. Added invalid/upa/ubp/zero-expiry/negative-expiry cases preserve existing ID/refresh/expired/missing/wrong-actor/audience/duplicate/service-key and current revocation/access checks. Authorized result reads still return both correct expiries after tariff retirement.
- `api/openapi.yaml` does not document this internal endpoint, so no unrelated API schema or SDK expansion was made.

## Evidence

1. Before production changes, root copied the test-only diff into its owned isolated PostgreSQL test tree against source `b9f2975` and ran:

   `go test -race -json ./internal/httpapi -run '^TestResultReadRequiresLiveProductAccessToken$' -count=1`

   Actual PostgreSQL run, exit 1, no skipped tests. Expected regression reproduced: `actor_expires_at` was absent for the valid reader and the authorized reader after tariff retirement. The stored credential-expiry assertion did not fail. Root receipt: `.local/integration-e2e-20261029/result-actor-expiry-red.result.json`. Test source SHA-256: `49e2882e5e5cfe5099608c5349afee29da8045af6202c8ef3c07c910d3f79c85`.

2. Local compile, before and after implementation:

   `go test ./internal/httpapi -run '^$' -count=1`

   Passed; explicitly compile-only, no tests executed and no database mutations. `git diff --check` passed.

3. Actual PostgreSQL green/race validation of the committed implementation remains owned by root and pending at this implementation handoff. No local shared database, Docker restart, SSH, provider or production operations were performed by this implementation task.

## Limits and concerns

- Authorization and returned expiry are point-in-time evidence. The downstream gateway must recheck this verified expiry immediately before delivering results; this does not provide cross-service linearized revocation.
- Production/server evidence, deployment and package publication are separate root-owned gates. No such completion is claimed here.
- Root-owned documentation/checklist changes were preserved and excluded from this task's commit.
