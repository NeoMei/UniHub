# UniHub Subscriptions, Credits and Console Implementation Plan

> **For agentic workers:** Use subagent-driven-development with test-driven development and independent reviews. Continue through implementation and acceptance; this plan does not redefine the whole platform as complete.

**Goal:** Make the next phase-one slice usable: authenticated personal/team console, recoverable subscription and add-on purchases, monthly model-credit batches, bounded trusted model reservations and settlement, and first-product integration.

**Architecture:** Retain Casdoor identity, PostgreSQL metadata and durable commands, and Formance as the only asset authority. Add one commercial domain service, a separate browser session boundary, and a Chinese React console. Product integrations consume restricted credentials and service endpoints; they never decide the user's final charge.

**Tech Stack:** Existing Go/pgx/Formance; React, TypeScript and Vite for static console assets. No production merchant or model credentials are invented.

**Spec:** ../specs/2026-10-24-unihub-design.md, especially sections 4–10.

## Global Constraints

- All visible branding is UniHub. Reuse approved branding/assets PNGs.
- Personal/team ownership and existing roles remain authoritative. Finance actions require owner or billing_admin, including at submission time.
- Cash uses CNY/6; model credits use a distinct CREDIT/6 asset. Monetary and credit API fields are integer strings; no float accounting or implicit cash/credit exchange.
- Formance owns every asset quantity; PostgreSQL stores metadata, immutable commands, allocations and reconstructible projections only.
- Account advisory locks coordinate role/authorization revocation and submission. Uncertain commands recover using the original reference and payload.
- Monthly grants reset, annual purchases issue monthly, late jobs issue only the current valid month; UTC original-day anchors survive short months.
- All products spend the account's llmrouter credit pool and retain origin_product_id. No automatic cash fallback or add-on purchase on exhaustion.
- User confirmed on 2026-10-24: real offers remain configurable drafts; explicitly synthetic test offers may be published only in development.
- No real merchant, deployment, account migration, model secret, push, third-party accounts, MCP or API gateway is part of this local implementation.
- Preserve unrelated work in product repositories and their AGENTS.md instructions. Use CodeGraph first if indexed.

## Task 1: Recoverable commercial domain

**Files:** Create `internal/billing/` with focused types, catalog, calendar, purchases, credits, rates, recovery and tests; migration `005_billing.sql`. Do not edit HTTP/config/frontend/wallet files; coordinate any necessary shared changes with controller.

**Interfaces:** `billing.New(pool, accountsStore, ledger) *Service`. Publish exact exported types/methods in `internal/billing/types.go` early and tell controller. Methods must cover catalog draft configuration/publication, public catalog, subscription list, purchase/add-on, upgrade quote+accept, renewal settings, batches, orders, reservation+settlement, maintenance/recovery. Service methods must recheck authorization themselves. Controller adds transport/authentication and UI. Use existing `ledger.Ledger`, `database.LockAccount`/`LockAccountTx`, `wallet.Address`/`Asset`.

**Rules and deliverables:**

- Immutable offer versions with product, personal/team applicability, membership/add-on kind, month/year period, fixed price, included monthly credit, entitlements, rank, configurable add-on validity, upgrade rule and publication status. Drafts cannot be bought; no real products are automatically priced or published. Seed explicit sandbox-only sample offers via a separate `SeedSandbox` method, never general migrations.
- One active subscription per account/product. Purchase snapshot fixes term, grants and actual funding allocation. Product-wallet spend and explicitly authorized unified shortfall are one ledger transaction. Durable cash payment and fulfillment are separate states; replay does not recharge or regrant. Block unresolved account commands before generating new allocation snapshots.
- Calendar pure functions use UTC, preserve original anchor day, clamp missing days, never exceed paid-through. Annual term emits current month only, maintenance skips historical expired months. Unique grant source keys for base, upgrade, add-on; original amount must reconcile to all ledger buckets.
- Same-cycle upgrade quote locks subscription version, offer version, period and explicit prorated-price rule. Only a published configured rule can quote; integer/rational arithmetic rounds once. Quote expires at period boundary; upgrade credit = new monthly entitlement minus all issued this subscription period, not remaining credit. Two upgrades cannot repeat full grants. Cross-cycle changes must reject clearly if not yet supported, never silently alter terms.
- Renewal authorization and unified funding authorization are separate, explicit, versioned, bounded and expiring; revocation serializes with submitting. Auto-add-on absent/off. Renewal uses existing paid-through term and idempotency; failures do not extend membership.
- Batch ledger addresses for available/reserved/consumed/expired/withdrawn; reservations allocate earliest expiry then stable creation order; expired batches never reserve. Durable reserve/settle/top-up/release by logical call ID, request digest and actor/account/origin binding. Membership removed prevents new reserves; already submitted calls finish for original payer. Requests with no credits reject even if cash exists.
- Immutable published rational rate versions, integer input/output/cache_read/cache_write/reasoning counts with explicit mutually exclusive semantics, independently calculate sum then ceil once. Final settlement idempotent; provider retry does not double bill. Excess final charge capped to held amount with recorded platform difference, no negative credits. Missing usage stays unresolved/manual; release after expiry goes expired. Pure failure uses explicit trusted release.
- Refund requests freeze source batches, reject automatic full refunds for consumed or reserved credits (manual review with reason), revoke unused grants before refunding the saved cash allocation, use durable append-only commands. No refund may resurrect late grants.
- Meaningful tests first: amount/calendar boundaries; real isolated PostgreSQL and real Formance purchase/replay/concurrency; fault ledger only for controlled response-loss windows; two same-period upgrades; annual late maintenance; expiration during reservation; source-product shared pool; refund vs in-flight and late fulfillment; role/authorization revocation. Never mutate developer data in tests.

- [x] Write focused failing tests and implement each domain boundary.
- [x] Run package tests with real integration environment (read .local/dev.env without printing secrets).
- [x] Expose contract and document intentional unsupported paths; do not represent a stub as working.
- [x] Self-review and report files, invariants, verification, and remaining risks. Do not commit until controller coordinates shared tree.

## Task 2: Browser sessions, API and user console

**Files:** Create `internal/console/` for browser OIDC/session middleware and static hosting; migration `006_browser_sessions.sql`; `web/` React UI; create `internal/httpapi/billing.go`; extend `cmd/unihub` config/wiring and Makefile. Root owns Go/API/setup; frontend agent owns only `web/`.

**Interfaces:** Go serves console at `/`, `/auth/login`, `/auth/callback`, `/auth/logout`, `/auth/session`. Server-side sessions keep tokens off browser storage; cookie HttpOnly, SameSite=Lax, Secure in production, path=/, bounded lifetime. OIDC Code+PKCE/state/nonce, one-use transaction, exact configured callback. Session endpoint returns user/session status + CSRF token + sandbox flag (no credential). Same-origin cookie mutations require X-CSRF-Token and same origin; bearer API remains available for products. `/v1` existing account/wallet routes retain shapes. Billing routes under `/v1/catalog`, `/v1/accounts/{account}/subscriptions`, `/orders`, `/credits`, `/quotes`, `/settings`; exact wire contract published by controller before frontend integration.

- [x] Test state/code replay, missing/mismatched CSRF, insecure production config, session expiry/logout, bearer isolation, redirects and token secrecy; implement browser boundary.
- [x] Wire billing HTTP with service-only rate/reserve/settle endpoints and separate bounded credentials; browser cannot spoof actor/account/origin.
- [x] Build real-data Chinese console: persistent account switcher, overview, memberships, credits and batch expiry, add-ons, cash wallets and sandbox recharge, orders, members, renewal/funding settings. Preserve selection during request; stale responses may not overwrite another selected account. Disabled/empty/loading/error states are explicit; no invented fake production data.
- [x] UI uses approved logo, teal/mint/ink palette, restrained desktop console and responsive layouts. Financial amounts are formatted without Number precision loss. Confirm purchase/upgrade with visible account, price, term, current and future included credits; no hidden funding checkbox default.
- [x] Finance-only actions/values respect role; server remains final authority. Sandbox is visibly labeled and never described as paid real money.
- [x] Build frontend, test important formatting/account-race behavior, perform real browser login and business flow against API/ledger; capture evidence.

## Task 3: Restricted first-product integration

**Files:** Final exact files derive from read-only report `.local/product-integration-assessment.md`; UniHub restricted product-credential service and SDK, llmrouter independent platform billing path, SuperWagie independent platform login/account UI. Review existing repo instructions/status before edits and use isolated suitable checkout if necessary.

**Interfaces:** Product token is short-lived, audience llmrouter, binds verified actor/account/origin/allowed models and cannot be used for wallet finance. llmrouter uses a separate server credential to reserve/settle normalized usage. It must bypass its legacy token and wallet/subscription deductions only for verified platform traffic; failures never fall back to unbilled legacy path.

- [x] Inspect exact call paths and define supported first integration surface; preserve original BYOK/runtime identity behavior.
- [x] Implement platform credential/session contract and gateway adapter with persistent logical call id, rate snapshot and idempotency/request digest.
- [x] Implement SuperWagie platform login/account/entitlement/checkout/model path using separate bounded backend memory; discard refresh credentials and require login after restart. OS-protected persistent login remains a later native-desktop task (recorded implementation ruling).
- [x] Test both platform/no-double-charge and existing legacy paths, plus invalid/expired/revoked membership and uncertain settlement. Use actual upstream only if already authorized configured credentials exist; otherwise report that external acceptance remains pending, with local controlled-upstream proof clearly labeled.

## Task 4: Independent reviews and integrated acceptance

**Files:** `docs/subscriptions-credits-validation.md`, API documentation, README and plan checkboxes; scoped fixes wherever findings occur.

- [x] Independent task reviews and broad final review for spec fidelity, financial invariants, auth and frontend behavior; fix all actionable findings.
- [x] Run relevant unit/integration/race tests, vet/build, real browser flow, check diff/secrets/unrelated changes.
- [x] Record implemented versus externally pending boundaries accurately; code/test success is not production merchant, model or all-product acceptance.
- [x] Commit coherent local changes only. Overall platform remains incomplete while later product integrations and real commercial deployment are pending.
