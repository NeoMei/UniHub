# Complete commerce chain implementation plan

> Use subagent-driven-development for isolated ownership, focused regression and independent final review. User authorized implementation; no repeated approval required. Parallel independent ownership follows the session instruction, overriding the skill's generic serial preference.

**Goal:** Complete code for direct WeChat/Alipay checkout, verified settlement and refunds, native credit usage, and SuperWagie purchase/recovery UI.
**Architecture:** UniHub owns trusted merchant adapters, durable external checkout/receipts, idempotent ledger and grants, and explicit versioned native-credit conversion. SuperWagie displays authoritative offers, readiness, sessions and statuses. Old cash-funded calls and wallet purchases retain their semantics.
**Tech stack:** Go/stdlib, PostgreSQL/Formance; existing Node/React/TypeScript/Zod. No new packages.
**Spec:** docs/integrations/superwagie-direct-payments.md and .implementation/full-commerce-20261108/interfaces.md.

## Constraints and rulings

- No live charge, credential discovery/output, production mutation, publication or git operations. Existing managed worktrees are used.
- Product prices stay server authoritative. Explicit native conversion is configuration, not a guessed production tariff. Configuration absence disables new sales/use accurately.
- External payment never re-enters the wallet debit path; unresolved payment never gets a new provider order ID.
- REFUNDED requires both original-channel confirmation and refund ledger APPLIED. Receipt success with ledger failure is recoverable without another refund.
- Support external upgrade through an explicitly versioned cent-precision quote if safely implementable; use existing exact wallet quotes unchanged. Charge rounding is visible in the authoritative external quote, never hidden in an adapter.
- UI and cross-service changes use existing runtime boundaries, locales and theme tokens; unsupported runtimes remain explicit.

## Task 1 — Official provider adapters

Owner files: internal/payments/* and its docs/tests only. Consumes/produces exact interface in interfaces.md.
- [x] Add tests first for exact amounts, request signatures, verified response/notifications, wrong merchant/amount, duplicate/malformed input, provider timeout, query/close and original refund recovery.
- [x] Implement WeChat Native and Alipay page.pay/query/close/refund/query-refund with trusted config and stdlib cryptography. Follow primary official docs linked from the spec; verify current refund contracts online.
- [x] Run `go test ./internal/payments -count=1`; save report and freeze.

## Task 2 — External checkout and entitlement/refund settlement

Owner files: internal/billing/checkout*.go, external funding/refund hooks; database migration; internal/httpapi checkout routes/capabilities/scopes; API documentation. Shared billing native files belong to Task 3. Avoid cmd config (root owns).
- [x] Add tests for one checkout/order/grant under replay/concurrent notification, exact payer/offer/price, lost create/query responses, late payment, old wallet preserved, unpaid checkout doesn't block model use, pending refunds and ledger retry.
- [x] Implement durable order/receipt/provider identity before network action; don't hold account lock over provider IO. External payment source owns accounting independently of cash wallets.
- [x] Implement ordinary and product-finance HTTP surface with correct actor/scope/product binding, verified callback persistence and provider ACK.
- [x] Implement original-channel full refund for unused unreserved grants; consumed/reserved cases remain explicit manual review. Do not mark refund complete before provider confirmation and ledger application.
- [x] Cover cent-precision upgrade quote/checkout, stale-version and delayed-paid review without hidden rounding or duplicate grants.
- [x] Run focused real local PostgreSQL/Formance tests; record non-skipped evidence and freeze.

## Task 3 — Native CREDIT metering

Owner files: internal/billing/native*.go and new native_credit*.go, profile config fields/hooks as coordinated; internal/modelaccess native/catalog/key fields and tests; internal/httpapi native calls only. No checkout HTTP edits.
- [x] Write tests proving native calls can reserve/settle/release CREDIT with immutable conversion, membership requirement, earliest expiry, exact rounded cost, usage-budget asset and unknown settlement recovery.
- [x] Preserve native CNY regression and old key funding; only explicit native-credit profile/key selection opts into points. No model-cost assumptions or production rates.
- [x] Expose authoritative readiness for usable CREDIT path and no cash fallback; coordinate any LLMRouter protocol changes before touching its canonical checkout.
- [x] Run focused integration tests and document config; freeze.

## Task 4 — Startup config and SuperWagie end-to-end UI

Owner: coordinator (may delegate after an independent slot frees). Files: cmd/unihub config/main; SuperWagie platform-account server and shared UI/contracts/locales/docs.
- [x] Parse protected merchant configuration paths at startup; missing channel is unavailable, malformed configured channel fails safely. Configure worker recovery too.
- [x] Add trusted checkout adapter and persistent original-request recovery; validate provider sessions and fixed official redirect host, no arbitrary HTML.
- [x] Add channel selection, WeChat QR / Alipay official link, checkout status, manual refresh/close, and restored pending checkout; no navigation-induced payment/task replay.
- [x] Replace hardcoded new-program block with authoritative native-credit and payment readiness; keep absent/malformed capability fail-closed.
- [x] Run focused Node/UI and config tests, then full type/lint/web build serially.

## Task 5 — Integration, review and handoff

- [x] Exercise complete local sequence: draft fixture catalog -> checkout -> signed simulated provider receipt -> ledger APPLIED -> grant -> CREDIT model reservation/settlement -> status -> refund where eligible.
- [x] Independently review payment authentication, scope/account isolation, replay/refund races and old cash compatibility; fix all actionable findings.
- [x] Inspect actual UI fixture light/dark/narrow states and browser console, using repo Chrome CDP helper with MacAppCodeSignClone disabled.
- [x] Record exact checks and distinguish coding/local integration from merchant/live-payment/desktop-release acceptance. No merge/release.

## Completion evidence

All coding tasks and controlled local checks are complete. See [implementation handoff](../../integrations/superwagie-commerce-implementation.md) and `.implementation/full-commerce-20261108/verification.json`. Merchant setup, approved production conversion/offers, deployment and real payment/provider acceptance remain separate operational work. No real money, source merge or release was performed.
