# Task 1b central actor finance implementation

Date: 2026-10-30 (Asia/Shanghai)

## Scope and base

- Repository: `/Users/neomei/.codex/worktrees/unihub-mail-layout/unihub`
- Branch: `codex/product-path-e2e`
- Base: `1ea77c4b2d4968eb53eafd891beadc90b7270610`
- Implementation commit: `5fc343a1081bb1d963e0aaf7f8bcd51904223ccd`
- No database schema, identity merge, balance move, runtime-fund mutation, native-role rewrite, deployment, SSH, push, or paid call was made.

## Changes

- Added optional `finance_client_id` to `RuntimeService`; startup validation accepts individually scoped `finance:read` and/or `finance:write` operations, requires an exact registered client for the same product and instance, and rejects a mapping with no finance-eligible scopes. The selected client's route scopes remain checked per request.
- Factored action registration in `New` so the private finance paths call the existing `/v1` actions. The wrapper requires exactly one runtime service bearer and one `upa_` actor bearer, rejects `Origin`, checks the mapped product client and route scope, re-verifies actor/current product authorization plus `product:use`, attaches product attribution, bounds body/query/response data, and preserves original route patterns and path values.
- Added the finite private API routes for existing account, catalog, billing-profile, usage, managed-key and recharge actions, plus existing subscriptions/orders/billing-presets reads. Public `/v1` routing still registers its original authenticated handlers. `GET /accounts/{account}/recharges` is absent from the center baseline and remains omitted; the private route supports existing per-order reads and create instead.
- Added `ProductFinanceClient` in `packages/userkit-go`, with a fixed origin, finite method/path/query validation, exact `upa_` actor format, fixed service authorization, bounded JSON request/response, preserved recharge idempotency, redirects rejected and no application-level retries. No service or actor credentials are returned in `FinanceResult`.
- Added SDK documentation and parser, transport and HTTP tests. A PostgreSQL-backed test covers same-email identity isolation, exact account read, scope/role denial, cross-product assertion denial, actor expiry/auth-version revocation, global disable, credential non-disclosure, and idempotent sandbox-settlement replay.

## Verification

- Passed: `go test ./cmd/unihub -run '^TestFinanceRuntimeRequiresOneExactProductClientAndScopes$' -count=1`
- Passed: `go test ./internal/httpapi -run '^TestProductFinanceRoutesRequireBothCredentialsAndStayFinite$' -count=1`
- Passed: `(cd packages/userkit-go && go test ./... -count=1)`
- Passed: `git diff --check`
- Attempted database-backed test; the test harness stopped before running it because `UNIHUB_TEST_DATABASE_URL` is unset. It needs the root's configured PostgreSQL integration run. Formance acceptance and actual sandbox ledger readback were not run here.

## Remaining review and limits

- Root should independently review the implementation before the LLMRouter peer consumes this contract. Real PostgreSQL/Formance replay, exact role parity, product disable and sandbox ledger readback remain for the root's acceptance run.
- Finance client policy scopes are intentionally not widened by service operations. A read-only service can use only read routes, and any endpoint still requires its registered OIDC product-client scope plus its existing account authorization.
- No active deployment or browser acceptance is claimed.

## Independent review follow-up

- Round 1 fix commit: `d2eeab717c6ae0515f3d143ea750579f869235c2`.
- Round 1 identified that an injected SDK transport could override DNS dialing, proxy selection, or TLS verification while receiving the fixed-origin credentials. Removed the configurable HTTP client entirely. The finance client now builds a private transport with no proxy, injected dialer, custom TLS configuration, or keep-alive reuse; default certificate verification remains enabled.
- Added tests that replace the process default transport with a malicious dialer and prove the request still reaches only the configured server, inspect that the private transport has no dial/TLS/proxy overrides, and reject a self-signed HTTPS endpoint before it receives the credentialed request.
- Strengthened the PostgreSQL HTTP test to use Formance when `UNIHUB_TEST_LEDGER_URL` is configured, process a repeated sandbox settlement, and verify the operation is `APPLIED` with exactly one 1,000,000-unit cash credit. It also issues a fresh assertion before the separate auth-version revocation check so expiry does not mask version invalidation. This extended database/Formance test remains unrun locally because the database environment is unavailable.
- Before freeze, corrected the Formance assertion to target the `llmrouter` product wallet for the product-scoped recharge, assert the unified wallet remains at zero, and process the already-applied operation again to verify the product balance does not change after the replay.

## Contract addendum

The baseline has no recharge collection-list route. Subscriptions, order-list and billing-preset reads are included because those public actions already exist for the finance panels; they retain `subscriptions:read`, `orders:read`, and `catalog:read` policy checks. No other route was added beyond existing `/v1` actions.
