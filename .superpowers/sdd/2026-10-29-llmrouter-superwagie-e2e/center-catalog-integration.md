# Center catalog integration

## Frozen source and scope

- Worktree: `/Users/neomei/.codex/worktrees/unihub-mail-layout/unihub`; branch `codex/product-path-e2e`.
- Base: `624778d1ffaf7aca5c0f2a3e28a2596f2db50541`.
- Implementation head: `8ebf7c4b313fc0f817d0f71f30c67171cd255734` (the source verified below; this report is committed separately).
- Implementation diff SHA-256: `948bde6b4d728815163d8c64c6477c5af07fe0f7b7d351886261750a10cdc162`, from `git diff --binary 624778d1ffaf7aca5c0f2a3e28a2596f2db50541 8ebf7c4b313fc0f817d0f71f30c67171cd255734 | shasum -a 256`.
- Applied the complete four-path scoped billing projection from reviewed `0d3acc06e54b31b45bc3cd667ee2e9f9e677b916` + `dcac3848ae0a5a14d0084be5d29baad257c34e2e`.
- Applied only `internal/metering/metering.go` and the three new rerank tests from `2bca72f927ca5e42b6ba6e49b4fc22cc620debb6`.
- Added OpenAPI query/authorization/response documentation, retaining the legacy no-query contract. This is billing consistency only; consumers still need a trusted technical capability intersection.
- Extended the imported scoped HTTP test with route-specific denial/no-disclosure assertions for absent client `catalog:read`, inactive actor and inactive product assignment, plus empty/duplicate payer and extra query rejection. These added fixture assertions are compiled but not executed here.
- Native video acceptance fixture remains byte-for-byte unchanged from base (including `1af81bc` stronger video 403/no-hold/no-debit checks). `internal/billing/wire_operation.go` and `internal/billing/credits.go` are unchanged. No video enablement, version bump, release, deployment, external requests or supplier calls.

## Verification

All Go commands used `GOPROXY=off GOTOOLCHAIN=local`; no dependency downloads were permitted.

| Check | Result |
| --- | --- |
| `go test -count=1 -json ./internal/metering ./internal/billing` | Overall exit 1: incomplete integration verification because DB/ledger configuration is absent. Metering: 10 top-level tests passed, 0 failed/skipped (75 pass events including subtests). Billing: 15 top-level passed, 27 failed at missing `UNIHUB_TEST_DATABASE_URL`, 49 skipped for missing `UNIHUB_TEST_LEDGER_URL` (22/27/53 events including subtests). This is not a green billing-suite result or evidence of a code regression. |
| `go test -count=1 ./internal/billing -run '^TestRerankCashInputTokenCharge$'` | Passed; pure rerank exact charge/rounding test executed. |
| `go test -count=1 -run '^$' ./internal/httpapi ./internal/modelaccess` | Passed compile checks only; zero tests executed. HTTP compile check repeated after the focused fixture assertions and passed. |
| Ruby `YAML.load_file` and contract assertions | OpenAPI parses; path + two query parameter declarations and 400 response verified. |
| `git diff --check` / `git diff --cached --check` | Passed. |
| Base-to-head diff of native video fixture and billing wire/reserve guards | Empty. |

`UNIHUB_TEST_DATABASE_URL` and `UNIHUB_TEST_LEDGER_URL` are both unset. No scoped/rerank HTTP fixture was executed and no database/ledger service was accessed or provisioned. Raw local Go JSON output is `/tmp/unihub-center-catalog-integration-test.jsonl`.

## Remaining acceptance

The coordinator must run affected billing/modelaccess/httpapi tests with its known isolated real PostgreSQL/Formance fixtures on the implementation head before accepting runtime behavior. Include scoped consistency, rerank HTTP settlement, existing native metering/video denial and historical receipt replay, wire operation and flat-request chat compatibility. The imported scoped test now also exercises the review-requested inactive actor/assignment and missing client-scope checks. No production, upstream supplier, desktop UI or packaged acceptance is claimed here.
