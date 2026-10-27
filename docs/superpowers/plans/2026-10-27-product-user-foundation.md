# Product User Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Ship reusable UniHub user foundation and consume it in LLMRouter for automatic product users and centrally managed operator access.

**Architecture:** Central product manifests and persisted role/status assignments supply current product-scoped access. A public Go kit verifies this access and invokes product-owned idempotent provisioning. LLMRouter retains ordinary zero-quota projections and evaluates live management permissions in request context.

**Tech Stack:** Go, PostgreSQL/pgx, GORM, React/TypeScript, existing OIDC and browser sessions.

**Spec:** docs/superpowers/specs/2026-10-27-product-user-foundation-design.md

## Global Constraints

- Approved user scope includes implementation; use dedicated feature branches, preserve existing dirty files and all business/billing data.
- No production grants, supplier calls, real payments, message sending, deployment or publication.
- Product access comes from verified audience plus explicit policy, never email, client input, account team roles or stored native elevation.
- Default users remain ordinary, zero native quota; no native wallet, user token, finance or relay authority for UniHub users.
- One implementation worker at a time; reviewers and read-only discovery may run independently.
- Tests must cover observable contracts and run against real isolated databases when database behavior is involved.
- LLMRouter SQLite, MySQL 5.7+, PostgreSQL 9.6+ compatibility and common JSON wrapper requirements remain binding.

### Task 1: Central product access and reusable user module

**Files:** Create packages/userkit-go/{go.mod,client.go,service.go,*_test.go,README.md}, internal/productusers/{registry.go,store.go,*_test.go}, internal/database/migrations/015_product_user_access.sql (choose next free number if occupied), internal/httpapi/product_users.go and tests. Modify internal/httpapi/{server.go,product.go}, internal/products/registry.go, cmd/unihub/{config.go,main.go} and tests. Extend packages/sdk/src/index.ts/tests and web/src with a separate product user administration view and application launcher. Update API schema/docs when routes are public contracts.

**Interfaces:** Consumes existing verified HTTP identity/console CSRF boundary and pgx account store. Produces exact wire/API/Go module contract in the spec, including current access, product-scoped assignments, global active status and user administration UI. Go interface Provider.Resolve(context.Context,string)(Access,error); public types and initializer/provisioner signatures documented in module README before completion.

- [ ] Add failing tests: default member receives product:use; same user explicit admin receives channel permissions only in target product/instance; empty override remains empty; revoked/disabled user denied with old bearer; stale version conflict; product credentials cannot use admin API; independent non-billing product works.
- [ ] Run tests and record expected RED result. Use isolated schema through testutil.Database; e.g. `go test ./internal/productusers ./internal/httpapi ./cmd/unihub` with configured test DB.
- [ ] Implement manifests, transactional status/assignments/audit, route gates and environment configuration, reusable provider/provisioner kit and server-backed client. Frontend admin view uses trusted session, existing request CSRF and server permission checks; app launch view shows current product roles.
- [ ] Add kit tests using actual httptest server and transaction-capable test provisioner: repeated/concurrent access initializes once, failed initialization leaves no usable identity, wrong product/audience/instance rejected, central deny cannot be overridden by extension policy, HTTP errors/oversized response/redirect reject.
- [ ] Extend TS API client contracts; update types and runnable minimal example docs, including standalone provider requirement and one-way role authority.
- [ ] Run Go kit tests, real-DB affected tests, TS SDK test/typecheck and web tests/build. Commit only task-owned files, report full commands/results, API signatures, commit range and concerns.

### Task 2: LLMRouter uses shared module and live product access

**Files:** Extend UniHub packages/userkit-go with the reusable OIDC browser helper and tests (separate UniHub commit), then create third_party/unihub-userkit (exact snapshot), scripts/sync-unihub-userkit.py, internal/platformaccount/userkit.go, model/unihub_session_binding.go, middleware/unihub_management.go and meaningful tests. Modify go.mod, router/unihub.go, middleware/{native_api_boundary.go,auth.go}, native management route registration, service/auth_session.go where current proof is required, GORM migration registration, frontend unihub/auth/sidebar files and tests, integration config/docs.

**Interfaces:** Consumes Task 1 Go kit and /v1/me/product-access. Produces encrypted per-session upstream proof binding, authenticated /api/unihub/access, explicit route permission enforcement and seamless OIDC->JIT->native product session UX. Uses Provider.Resolve plus product-owned GORM provisioner around existing ResolveUniHubProjection. No persisted local elevated role.

- [ ] Add failing tests for UniHub ordinary/admin/read-only/currently-revoked user through actual channel routes; wrong audience/session binding, native/PAT credentials and finance/token/relay paths rejected; standalone native admin continues.
- [ ] Run focused tests for RED. Reuse meaningful current router fixtures and require/assert conventions.
- [ ] Add the shared framework-neutral browser OIDC code/PKCE helper with browser-bound state/nonce/verifier and safe returns; test callback mismatch, replay/expiry and token-validation errors. Vendor shared module via deterministic sync tool and checked provenance, adapt platformaccount proof to shared kit, bind upstream proof encrypted and expiry-capped to each actual dashboard session.
- [ ] Add explicit route permission matrix and live access checks, combine effective request role with specific permissions and audit identity. Cover refresh, session revocation and central unavailable without fallback.
- [ ] Update frontend SSO bootstrapping, safe return destination, role/access hydration and permitted navigation without changing private native policy or financially significant payer selection.
- [ ] Run tests, typecheck/lint/build and exact-source vendor verification. Commit task files; report commands/results, commit range and limitations.

### Task 3: Second-product starter and integration/browser acceptance

**Files:** Create examples/product-users (minimal independently runnable server using the Go module with extension/provisioning example), docs/integrations/product-user-foundation.md, isolated acceptance harness/tests and browser evidence instructions. Update root README and CONTEXT glossary to reflect actual delivered functionality and remaining deployment steps.

**Interfaces:** Consumes Task 1 public module/API and Task 2 LLMRouter adapter, validates real TCP cross-repo paths. No custom auth protocol, shared DB coupling or real external users.

- [ ] Add meaningful acceptance checks using isolated Postgres + actual HTTP services: same central user reaches two product identities, defaults initialize once; one product admin does not become another product admin; revoke/status reaches previously logged-in LLMRouter; native quota unchanged.
- [ ] Implement starter with explicit local standalone provider integration seam, reusable provisioner and extension data; include commands that work without repository-internal imports or machine-specific paths. New-product integration guide lists required callbacks/credentials and upgrade workflow.
- [ ] Run broad UniHub Go tests/race where relevant, LLMRouter focused backend regressions, both frontend required checks, SDK/kit tests, and actual TCP acceptance. Capture browser successful login/launch/role change/revocation with identity fixture boundary clearly labeled; use real local OIDC when available.
- [ ] Fix verified integration failures with regression tests; commit task-owned changes and report distinct automated, TCP, browser, OIDC and deployment evidence.
