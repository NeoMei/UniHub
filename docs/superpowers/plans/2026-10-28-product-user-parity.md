# Product User Parity Implementation Plan

> **For agentic workers:** Execute the independent repository areas in parallel with exclusive file ownership, integrate contracts centrally, and review every resulting diff. Do not publish or mutate production.

**Goal:** Make UniHub-backed and original LLMRouter users use the same complete product while UniHub manages identity mappings, authorization and final settlement.

**Architecture:** Preserve mature product authentication and business services behind scoped UniHub adapters. Central runtime owns mapped subjects, effective product grants and ledger operations; LLMRouter keeps original local user IDs, keys, routing, metering and calculation. Source components support new products without replacing mature products' subsystems.

**Tech Stack:** Go/Gin/GORM (SQLite/MySQL/PostgreSQL), Go net/http/pgx/PostgreSQL, existing central ledger, React/TypeScript/TanStack/Bun.

**Spec:** `docs/superpowers/specs/2026-10-28-product-user-parity-design.md`

## Global Constraints

- Same roles/resources/settings/funds imply identical product pages and behavior, independent of identity source.
- Preserve original login/registration/authentication capabilities and mature product user/financial-account mapping.
- UniHub is final authorization and financial authority for managed accounts; never fail open to old privileges or a local wallet.
- Product-scoped `LR_QUOTA/0` with `lr-quota-v1` preserves original quota values without implicit currency conversion.
- Central privileged writes require scoped service authority and the current permitted human actor, except separately authorized imports.
- No production changes, publication, credential rotation, automatic email merges, deletion of old users/keys/history or speculative conversion.
- Keep LLMRouter's original four untracked items untouched. Do not push to its upstream origin.

## Task 1: Central runtime, ledger and SDK (UniHub worker)

**Files:** new `internal/billing/runtime*.go`, `internal/httpapi/product_runtime.go`, `packages/userkit-go/runtime.go`, next migration after 017, affected isolated service/HTTP tests. Existing route/options wiring is owned by this worker. Coordinate schema IDs with Task 2.

**Consumes:** existing accounts/productusers, central final ledger commands and durable recovery.

**Produces:** `NewRuntimeClient(ServiceConfig)`, `Bind(ctx, actorToken, BindRequest)`, `Access(ctx, localUserID)`, `Reserve(ctx, ReserveRequest)`, `TopUp(ctx, callID, AmountRequest)`, `Settle(ctx, callID, SettlementRequest)`, `Release(ctx, callID, ReleaseRequest)`, `Call(ctx, callID)`, `Balance(ctx, localUserID, payerAccountID)`, `AdjustCredit(ctx, actorToken, AdjustmentRequest)`, `ImportBalance(ctx, ImportRequest)` and delegated users/access operations. Routes are under `/internal/v1/product-runtime`; service configuration fixes product/instance/operations, actor header is `X-UniHub-Actor-Authorization: Bearer <token>`.

- [x] Write real isolated-DB tests for scoped binding, denied actors, no automatic identity merge, product/instance isolation, spent balance versus limits, duplicate/conflicting commands, lost replies, restart recovery and import replay. Observe expected failures before implementation.
- [x] Implement frozen runtime wire DTOs and scoped authentication; ensure no credential/token appears in logs or persistence.
- [x] Implement central ledger addresses/commands for scoped credit, reserve/top-up/settle/release and audited gift/import. Reuse final ledger/recovery, do not add a second financial authority or provider dispatch.
- [x] Add SDK methods and HTTP contract tests; verify errors distinguish denied, conflict, unavailable and pending.
- [x] Run `go test ./internal/productauth ./internal/httpapi ./internal/productusers ./internal/billing`, SDK `go test ./...`, and real database integration tests. Report skipped environment gates explicitly.

## Task 2: Mature-product identity adapter and native user lifecycle (identity worker)

**Files:** LLMRouter `model/unihub_identity.go`, `middleware/auth.go`, `service/authz/`, `internal/platformaccount/`, authentication/user controllers and `router/unihub*`; new UniHub `internal/productauth/`, `internal/httpapi/product_auth.go`, SDK `product_auth.go`, coordinated migration. Do not edit billing/relay or frontend.

**Consumes:** Task 1 scoped bindings/current access and shared service policy; original successful authentication/step-up callbacks.

**Produces:** stable ordinary product user mapping, exact effective central roles, delegated original user-management writes, scoped trusted-authentication assertions, lifecycle revocation and one-shot OIDC silent login.

- [x] Reproduce rejected original APIs, projection corruption on nonzero counters/admin role, stale role union and old-user duplication in behavior tests; run failures.
- [x] Replace projection-only restrictions with immutable identity-link validation and normal user business state. Keep stable IDs and never link by email alone.
- [x] Complete trusted authenticator contract before issuing product sessions. Preserve password, OAuth, MFA, Passkey and recovery behavior; current central deny/unavailability prevents session/privilege fallback.
- [x] Route original administrator writes through current central grants; include optimistic version checks, instance boundaries and independent financial permissions. New registration never imports admin privileges.
- [x] Implement explicit audited import/link operations, lifecycle version invalidation and server-validated `prompt=none` return without redirect loops.
- [x] Run targeted model/middleware/controller/router tests, then `go test ./...` and `go vet ./...` after integration. SDK and central identity bridge tests must pass against the actual frozen contracts.

## Task 3: Original UI parity (frontend worker)

**Files:** LLMRouter `web/src/routes/__root.tsx`, identity-conditioned feature/navigation helpers, keys/dashboard/profile/usage-logs/settings/auth flows, module `__tests__/` and real application browser tests. Exclusive ownership of `web/`.

**Consumes:** original API shapes with effective roles/permissions/quota/used quota/request count, original token endpoints and one-shot OIDC silent-login route from Task 2.

**Produces:** the same original product UI for both identity sources; optional central portal access does not replace product pages.

- [x] Write failing same-role identity behavior tests for original Key fields/options, dashboard values, navigation/profile/wallet, log amounts/actions, settings and second-factor verification.
- [x] Remove identity-source-driven alternative product pages, route white lists and hidden original features. Keep permission-based guards and source information for authentication/audit only.
- [x] Preserve original registration/login form behavior; coordinate one automatic `prompt=none` attempt and safe fallback without a loop.
- [x] Run affected Vitest tests, real application Playwright parity tests, `bun run typecheck`, affected-file lint and `bun run build`.

## Task 4: Same relay with central funding, budget and account cutover (root)

**Files:** LLMRouter new `internal/billing/runtime*.go`, durable model migration/funding records, `service/billing*.go`, `service/native_billing.go`, task settlement hooks, existing original quota display/adjustment controllers, SDK vendor sync and startup configuration. Coordinate shared controller/model/schema edits with Task 2.

**Consumes:** Task 1 runtime SDK/central ledger and Task 2 ordinary mapped local users/authorization. Keep real key/user/group routing.

**Produces:** central funding through the original billing/relay interfaces, fixed payer/asset/price snapshots, persistent pending commands/reconciliation and explicit one-time account funding cutover; standalone local funding remains complete.

- [x] Add failing integration tests using the real original billing interface and isolated LR database with central HTTP boundary; original keys retain model/IP/group/expiry/limit semantics and local money is not double-debited.
- [x] Add central funding selection by explicit account cutover state, never identity/key prefix. Pin payer and backend; reserve/settle/refund are persistent/idempotent and reuse logical calls across retries.
- [x] Integrate legacy quota billing and configured native metering/tasks into the same financial adapter. Preserve technical counters/logs and original price calculations. Restart recovery must not resend providers or refund uncertain dispatched calls.
- [x] Adapt original balance and credit-adjustment APIs to spendable central credit; distinguish set remaining credit from add delta and guard optimistic concurrency. Keep budgets/key caps as limits.
- [x] Add explicit frozen-snapshot import/cutover command and duplicate/conflict tests. Preserve legacy artifacts and block uncertain imports; retain pre-cutover call ownership.
- [x] Run affected tests, full backend test/vet/build and cross-project isolated acceptance: create/link users, delegate admin, create/edit original keys, relay via native pipeline, adjust budget/funds, read original logs and confirm one central debit.

## Task 5: Independent review and completion evidence

- [x] Review approved spec coverage against all changed code; close material gaps rather than treating green smoke tests as product acceptance.
- [x] Independent agents review immutable diffs for functional parity, privilege escalation, financial ownership, migration and recovery.
- [x] Fix substantive review findings and rerun only affected checks, then perform one final coherent integration pass.
- [x] Record exact commands/outcomes and remaining environment limitations. Commit local reviewable changes on `codex/unified-product-parity`; do not claim publication or production acceptance.

## Execution ownership

UniHub worktree: `/Users/neomei/.codex/worktrees/unified-product-parity/unihub`. LLMRouter checkout: `/Users/neomei/项目/codexprojects/llmrouter` on `codex/unified-product-parity`. The source SDK is synchronized into LLMRouter's existing `third_party/unihub-userkit` only after worker-owned SDK changes are stable. The root integrates shared configuration/schema changes and maintains the evidence record.


## Acceptance limits

Targeted central tests use isolated PostgreSQL schemas and the local Formance ledger. Product model/relay tests use real SQLite transactions and a controlled central HTTP boundary. Original product UI tests use the real React application and controlled API responses. These are distinct receipts.

A separate opt-in cross-repository test joins the real central HTTP/database/ledger and original product registration/login/router/billing interfaces. Its result must be recorded in the acceptance report. It does not establish live external payments, provider availability, Chromium across deployed origins, production migration or deployment.

The legacy financial importer is offline-only. `offline_evidence` is external reviewed evidence, not automatic live-node detection. No production identity mapping or financial import was performed during implementation.

Local receipts: [acceptance report](../../userkit/2026-10-28-product-parity-acceptance.md).
