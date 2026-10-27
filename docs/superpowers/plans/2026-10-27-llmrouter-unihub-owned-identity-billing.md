# LLMRouter UniHub-Owned Identity and Billing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make UniHub the only public LLMRouter user and billing authority while keeping native LLMRouter identities and billing behind an explicit internal compatibility boundary.

**Architecture:** LLMRouter's public product routes are gated by UniHub product sessions and model credentials. The trusted platform route keeps provider/channel execution and UniHub reservation/settlement in LLMRouter. Native user, token, wallet, subscription, and top-up routes are disabled for public origins and remain available only to private operator/compatibility traffic.

**Tech Stack:** Go/Gin/GORM LLMRouter, Go/pgx UniHub, Node/Vitest SuperWagie managed backend, PostgreSQL, existing UniHub model-credential and LLMRouter platformbilling contracts.

**Spec:** `docs/superpowers/specs/2026-10-27-llmrouter-unihub-owned-identity-billing-design.md`

## Global Constraints

- UniHub is the only public identity and billing authority for LLMRouter.
- Native users, passwords, API tokens, balances, subscriptions, and historical data are not automatically migrated.
- UniHub platform traffic never falls back to native LLMRouter quota or billing.
- Every public allowlisted model has exactly one unambiguous published asset rate.
- Provider secrets, model-service tokens, and user credentials never enter tracked files, reports, or browser responses.
- OCR, embedding, rerank, speech, image, and video operations remain separate until their own UniHub contracts exist.
- Preserve a database/config backup before every remote mutation and keep a rollback path.

---

### Task 1: Add explicit LLMRouter public/native boundary policy

**Files:**
- Create: `/Users/neomei/项目/codexprojects/llmrouter/internal/unihubaccess/policy.go`
- Test: `/Users/neomei/项目/codexprojects/llmrouter/internal/unihubaccess/policy_test.go`
- Modify: `/Users/neomei/项目/codexprojects/llmrouter/router/api-router.go`
- Modify: `/Users/neomei/项目/codexprojects/llmrouter/middleware/*` where public auth is registered

**Interfaces:**
- Produces `unihubaccess.Policy` with `PublicEnabled`, `NativePublicEnabled`, and `IsPrivateManagementRequest(*http.Request) bool`.
- Consumes deployment env `UNIHUB_PRODUCT_ENABLED`, `UNIHUB_PLATFORM_ENABLED`, `NATIVE_PUBLIC_ACCESS`, and private management origin/proxy policy.

- [ ] Write tests for public/native policy combinations: public UniHub enabled, native disabled; explicit internal compatibility enabled; missing/invalid policy fails closed.
- [ ] Run `go test ./internal/unihubaccess -count=1` and confirm failures before implementation.
- [ ] Implement policy parsing with strict boolean values, trusted-origin matching, and no query/header override for public requests.
- [ ] Apply the policy to native login/signup/token/top-up/subscription routes while leaving health, readiness, public UniHub config, and platform routes available.
- [ ] Add route tests proving public native routes return a stable disabled/migration response and private management requests retain access.
- [ ] Run `go test ./internal/unihubaccess ./router ./middleware -count=1`.
- [ ] Commit `feat: gate native llmrouter access behind private policy`.

### Task 2: Make UniHub platform billing the only public relay path

**Files:**
- Modify: `/Users/neomei/项目/codexprojects/llmrouter/router/api-router.go`
- Modify: `/Users/neomei/项目/codexprojects/llmrouter/internal/platformbilling/*`
- Test: `/Users/neomei/项目/codexprojects/llmrouter/router/platform_live_fixture_test.go`
- Test: `/Users/neomei/项目/codexprojects/llmrouter/internal/platformbilling/*_test.go`

**Interfaces:**
- Consumes `unihubaccess.Policy` from Task 1 and existing `platformbilling.Adapter`.
- Produces the invariant: platform request admission, reserve, dispatch, settle/release; no native quota mutation.

- [ ] Add regression tests for platform calls when UniHub is unavailable, grant is missing, rate is missing, account/model is wrong, and native fallback would otherwise be selected.
- [ ] Run the focused platform billing tests and confirm the new cases fail before implementation.
- [ ] Add a single public admission guard before provider dispatch and make native relay middleware unreachable for `/platform/v1/*` failures.
- [ ] Verify request digest/idempotency remains the source of duplicate rejection and receipt reconciliation remains durable.
- [ ] Add a test that a rejected UniHub grant produces zero native user/token quota updates and zero provider sends.
- [ ] Run `go test ./internal/platformbilling ./router -count=1` and the focused race tests.
- [ ] Commit `feat: make unihub the sole public platform billing path`.

### Task 3: Lock UniHub product model/rate consistency

**Files:**
- Modify: `/Users/neomei/项目/codexprojects/unihub/internal/modelaccess/service.go`
- Modify: `/Users/neomei/项目/codexprojects/unihub/internal/httpapi/product.go`
- Test: `/Users/neomei/项目/codexprojects/unihub/internal/modelaccess/*_test.go`
- Test: `/Users/neomei/项目/codexprojects/unihub/internal/httpapi/*_test.go`
- Modify: `/Users/neomei/项目/codexprojects/unihub/docs/integrations/superwagie-model-catalog-2026-10-27.json`

**Interfaces:**
- Consumes published `billing.Rate` records and the `llmrouter` product policy.
- Produces credential issuance only for models with an active published rate matching the selected asset/funding mode.

- [ ] Add tests for missing model rate, duplicate same-asset rate, wrong asset, disabled product, and cross-product model credential requests.
- [ ] Run `go test ./internal/modelaccess ./internal/httpapi -count=1` and confirm the new cases fail before implementation.
- [ ] Implement strict model/rate validation and return a stable authorization error before inserting `model_credentials`.
- [ ] Add a read-only consistency endpoint/test projection showing product allowlist, published rates, and missing/extra models without exposing tokens.
- [ ] Run `go test ./... -count=1`.
- [ ] Commit `feat: enforce unihub model rate consistency`.

### Task 4: Configure SuperWagie defaults and public model discovery

**Files:**
- Modify: `/Users/neomei/项目/codexprojects/SuperWagie-open/packages/web/server/lib/platform-model/runtime.js`
- Test: `/Users/neomei/项目/codexprojects/SuperWagie-open/packages/web/server/lib/platform-model/runtime.test.js`
- Modify: `/Users/neomei/项目/codexprojects/SuperWagie-open/packages/web/server/lib/platform-account/DOCUMENTATION.md`

**Interfaces:**
- Consumes public `https://llmrouter.quukk.com/platform/v1` and the published 22-model catalog.
- Produces managed runtime defaults while honoring explicit local/enterprise disablement and local overrides.

- [ ] Add a test for managed desktop with no local override: 22 UniHub models are present and the gateway is the public HTTPS platform path.
- [ ] Run `bun run --cwd packages/web test -- server/lib/platform-model/runtime.test.js` and confirm the new test fails before implementation.
- [ ] Implement defaults behind explicit `OPENCHAMBER_UNIHUB_DISABLED=1` / `OPENCHAMBER_UNIHUB_MODE=off` fail-closed switches.
- [ ] Preserve ordinary OpenCode/BYOK providers and all local secret-handling boundaries.
- [ ] Run `bun run --cwd packages/web test -- server/lib/platform-model/runtime.test.js server/lib/platform-account/runtime.test.js` and `bun run --cwd packages/web type-check`.
- [ ] Commit `feat: default managed SuperWagie to UniHub model service`.

### Task 5: Migrate deployment configuration without enabling native public access

**Files:**
- Modify: LLMRouter deployment env/config source used by the remote container.
- Modify: UniHub product/rate deployment manifest.
- Create: nonsecret deployment consistency receipt under the remote operations directory.
- Test: remote readiness, public config, rate consistency, native route disabled responses.

- [ ] Back up LLMRouter and UniHub databases, current container env metadata, and the current Nginx/native route policy.
- [ ] Apply `NATIVE_PUBLIC_ACCESS=false`, `UNIHUB_PRODUCT_ENABLED=true`, `UNIHUB_PLATFORM_ENABLED=true`, the 22-model allowlist, and the complete asset-rate map.
- [ ] Reload the public proxy only after `nginx -t` succeeds and the new container passes readiness.
- [ ] Verify `/api/unihub/config` returns the exact allowlist, internal rates contain one rate per model/asset, and `/platform/v1/chat/completions` reaches grant validation instead of a native fallback.
- [ ] Verify native public login/top-up/token routes are disabled while private management readiness and channel APIs remain available.
- [ ] Record exact backup paths and the new deployment image/config digest without secrets.

### Task 6: End-to-end acceptance and GitHub publication

**Files:**
- Modify: integration validation reports in UniHub and LLMRouter.
- Modify: SuperWagie model catalog manifest if the source catalog changes.

- [ ] Run LLMRouter `go test ./internal/platformbilling ./router -count=1` and race-focused platform tests.
- [ ] Run UniHub `go test ./... -count=1`; run the actual gateway acceptance when isolated database/ledger and `UNIHUB_LLMROUTER_PATH` are configured, otherwise record it as not executed rather than passing.
- [ ] Run SuperWagie platform/account/knowledge tests and type-check.
- [ ] Use a designated UniHub acceptance user to issue one valid model credential, make one ordinary and one streaming call, read the call receipt, repeat the same logical call, and verify one supplier dispatch plus one settlement.
- [ ] Verify invalid/expired/revoked/wrong-model credentials fail before dispatch; verify native quota and native wallet remain unchanged for platform calls.
- [ ] Run `git diff --check` in all repositories and scan all publishable files for credentials.
- [ ] Commit only scoped source/docs changes; preserve unrelated worktree modifications.
- [ ] Push UniHub, LLMRouter, and SuperWagie branches to their intended GitHub remotes; record commit IDs and any pull-request URLs.
