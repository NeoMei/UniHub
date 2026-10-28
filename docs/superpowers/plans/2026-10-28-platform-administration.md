# UniHub Platform Administration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Deliver the approved complete platform administration area, starting with the super administrator seeing all users.
**Architecture:** Add a database-backed platform authority and dedicated `/admin/v1` routes. Keep ordinary account and native product authorization intact; management operations retain real actors and reuse domain state machines.
**Tech Stack:** Go, pgx/PostgreSQL, Formance, existing Casdoor/OIDC, React/TypeScript/Vitest.
**Spec:** `docs/superpowers/specs/2026-10-28-platform-administration-design.md` (confirmed 2026-10-28).

## Global Constraints

- Fixed `super_admin`, `user_admin`, `finance_admin`, `auditor` roles; exact central UUID, never email or provider isAdmin inference.
- `/admin/v1` accepts central console identity only; browser mutations enforce CSRF/Origin; no shared operator token or account-owner impersonation.
- Preserve original native product accounts, keys, login and ledger; no production changes, messages or payment actions during development.
- Lists use bounded pagination, filtering, count and explicit dependency failures. Never replace inaccessible balance with zero.
- Append audit with real actor, reason and versions; no secrets in lists/logs/exports; no direct balance or UNKNOWN edits.
- Database tests must actually execute against isolated schemas; source tests, browser acceptance and production readback remain separate evidence.

## Review Focus

- Role stacking and protected administrator targets: super + lower role must work; user-admin cannot gain finance through team role changes (Tasks 1,3,5).
- More than 100 users, changing filters and stale cursor: stable traversal and explicit total, no silent truncation (Tasks 1,2,4,5).
- Provider outage or unknown response: durable intent/readback, no duplicate creation, no bogus successful reset (Tasks 3,4,5).
- Disabled user re-enabled: old sessions/keys remain invalid while in-flight settlement remains recoverable (Tasks 1,3,5).
- Admin finance operations must neither mutate legacy operator allowlists nor bypass ordinary account authorization (Tasks 2,5).

## Shared interfaces and ownership

Task 1 owns `internal/platformadmin/store.go`, `authority.go`, `users.go`, `017_platform_admin.sql`, `internal/httpapi/admin.go`, and the `Options.PlatformAdmin`/route registration in `server.go`. Export `New(pool,*productusers.Store) *Store`, `Require(ctx,actor,permission) error`, `Principal(ctx,actor) (Principal,error)`, `RequireTarget(ctx,actor,target,permission) error`, `WriteAudit(ctx,tx,actor,action,target,reason string,detail any) error`, `ListOptions`, and `Page[T]`. Role JSON exposes `roles`, `permissions`, `enabled`. List envelope is `items,total,next_cursor,as_of`. Task 1 publishes exact contracts before dependent compilation.

Task 2 owns `internal/platformadmin/operations*.go`, `internal/httpapi/admin_operations*.go` and needed finance adapters. Register `s.adminOperationRoutes(add func(string,action))`; authorization is inside every platform service method. Task 3 owns `internal/platformadmin/teams*.go`, `identity*.go`, migration `018_platform_identity.sql`, `internal/httpapi/admin_identity*.go`, provider adapter files and registers `s.adminIdentityRoutes(add func(string,action))`. Task 4/root owns frontend, `cmd/unihub`, console boundary and integration. Agents do not edit one another's files or commit unrelated work.

### Task 1: Platform authority and user administration

**Files:** new platformadmin core and migration 017, admin HTTP core, productusers compatibility, tests.
**Interfaces:** shared interfaces above; routes `/admin/v1/me`, `/users`, `/users/{id}`, `/admins`, status/roles/sessions/product access.

- [ ] Write failing tests for fixed roles, exact identity, legacy import, last-super concurrency, >100 users and no permission leakage.
- [ ] Run focused tests and record failure; implement authority, explicit bootstrap/import API for protected CLI, bounded directory, details, status/session/key revocation and transactional audit.
- [ ] Add protected central routes, current-role checks and compatibility delegation without widening old grants; verify tests against isolated DB.
- [ ] Commit owned files and record interfaces/test evidence.

### Task 2: Operational queries and finance administration

**Files:** platformadmin operations/read models and routes, billing/modelaccess/wallet scoped adapters, tests.
**Interfaces:** consume `Store.Require`/shared pagination/audit; produce operation route registration above.

- [ ] Write and run failing tests for finance/auditor/user-admin distinction, safe key metadata, cross-account read, stable pagination and CSV escaping.
- [ ] Implement overview, account/Key/call/transaction/recharge/order/refund/subscription/credit/product/catalog/rate/audit/status read models, scoped budgets, key revocation and filtered export.
- [ ] Adapt existing offer/rate/profile/refund domain operations under a verified platform authority using actual actor; never mutate a global operator allowlist or claim payment success.
- [ ] Verify ledger failures and UNKNOWN recovery remain explicit and bounded; commit owned files with test evidence.

### Task 3: Identity lifecycle and team support

**Files:** migration 018, platform identity/team services, provider adapters and HTTP handlers/tests.
**Interfaces:** consume authority/target/audit; produce identity route registration and explicit provider capabilities.

- [ ] Write and run failing tests for protected admin targets, platform invitation authority recheck, last owner, provider unknown result and single-use recovery.
- [ ] Implement account/team details, name/member actions, ordinary/high privilege invitations and accepted ownership transfer with source-bound authorization.
- [ ] Implement separate provider directory, explicit materialization, pending activation/user creation, display-name editing and user-delivered recovery with durable unknown outcomes and capability checks. No admin-held reset secret.
- [ ] Implement full UniHub-derived session revocation semantics and production adapter guard; capability failure must not claim success. Verify with local provider fixture, commit owned files.

### Task 4: Integrated Web administration and executable setup

**Files:** `web/src/admin/*`, `App.tsx`, styles/tests, console/main/config/bootstrap tools.
**Interfaces:** consume `/admin/v1/me` and list envelopes; exact mutations match Tasks 1–3 route contracts.

- [ ] Add tests for role-gated navigation, >50 pagination, error distinctions, stable deep links and mutations carrying reason/version/CSRF.
- [ ] Build independent platform layout and all approved core pages, reusable searchable/filterable tables, focused detail/mutation forms; do not render dead buttons or secret values.
- [ ] Wire protected console `/admin/v1`, platform services, explicit CLI bootstrap/import, zero-product administration, auth issue-time validation and activity tracking.
- [ ] Run frontend tests/build and Go integration tests; commit owned files.

### Task 5: Whole-system acceptance and review

**Files:** OpenAPI, admin guide/permissions/acceptance evidence, targeted repairs.
**Interfaces:** all prior tasks; additive upgrade and security-preserving rollback.

- [ ] Run full real-DB/ledger race suite and frontend tests/build, identifying skipped tests separately.
- [ ] Exercise super and ordinary-user browser sessions, directory traversal, reversible user/team/product changes, finance readback/audit and credential revocation with controlled local services.
- [ ] Review each implementation task independently plus whole branch; repair load-bearing findings and rerun affected checks.
- [ ] Update contract/admin setup/capabilities/acceptance evidence and mark only actually completed tasks. Keep deployment/production acceptance explicitly separate.

## Execution rulings

- User confirmation authorizes implementation of this unchanged design; no additional plan approval is required. Use parallel agents with disjoint ownership for Tasks 1–3 and root implementation for Task 4, then a shared review, because this saves time without shared edits. This overrides the skill's generic sequential preference under the active proactive delegation instruction.
- Integration owns signature/route decisions if a detail is unspecified; record corrections in the plan ledger and continue.
