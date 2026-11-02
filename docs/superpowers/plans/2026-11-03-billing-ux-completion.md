# Billing UX and Integration Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Bring UniHub’s third-party onboarding, billing configuration, purchase, recharge, and post-purchase experiences to a truthful, mainstream SaaS baseline while preserving the current ledger and permission boundaries.

**Architecture:** Keep the existing React account console and admin console. Add a shared capabilities model so purchase and recharge actions are gated by actual deployment state, add a unified billing hub that composes existing catalog/subscription/credits/wallet/order APIs, and keep all money mutations behind the existing confirmation/idempotency flow. Extend admin configuration screens only for the BillingProfile/Offer fields already supported by the backend; expose onboarding as an honest checklist and exportable plan rather than pretending to register secrets from the browser.

**Tech Stack:** React 19, TypeScript, Vite, Vitest, Testing Library, existing UniHub JSON API and CSS tokens.

**Spec:** User request to complete the gaps identified in the billing UX review; source requirements are captured in [docs/unihub-third-party-integration.md](../../unihub-third-party-integration.md), [docs/composable-billing.md](../../composable-billing.md), and [api/openapi.yaml](../../../api/openapi.yaml).

## Global Constraints

- Never present sandbox settlement as real payment.
- Product and account permissions remain server-authoritative; UI controls only reflect capability.
- Product tokens cannot write commercial configuration; operator/admin boundaries remain intact.
- Preserve decimal integer strings for money, credits, usage, limits, and versions.
- Preserve the original idempotency key and read back uncertain operations; never auto-submit a new key.
- Do not put client secrets, payment credentials, provider tokens, or refresh tokens in browser state.
- Use existing CSS and component conventions; do not introduce a new UI framework.
- Every behavior change starts with a failing Vitest/Testing Library test.

---

### Task 1: Add truthful payment and product capability state

**Files:**
- Modify: `web/src/types.ts`
- Modify: `web/src/App.tsx`
- Modify: `web/src/forms.tsx`
- Test: `web/src/App.test.tsx`

**Interfaces:**
- Consumes: `GET /v1/capabilities`, existing account/session resources.
- Produces: `AccountConsole` passes `capabilities` to billing components; purchase/recharge controls render disabled explanations when real payment or product-wallet recharge is unavailable.

- [x] Write a failing test that returns `real_payments_configured:false` and asserts purchase controls are disabled with a clear explanation.
- [x] Run the focused test and verify it fails because the console currently does not read capabilities.
- [x] Add the typed capability fields and load `/v1/capabilities` once per account console.
- [x] Gate formal purchase/recharge buttons while preserving read-only catalog and wallet visibility.
- [x] Run the focused test and the existing App tests.
- [x] Commit `feat: gate billing actions by deployment capabilities`.

### Task 2: Build a unified plans and usage hub

**Files:**
- Create: `web/src/BillingHub.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/styles.css`
- Test: `web/src/BillingHub.test.tsx`

**Interfaces:**
- Consumes: catalog, subscriptions, credits, wallets, orders, product names, capabilities.
- Produces: a single `套餐与用量` page with current plan, usage summary, plan comparison, add-on entry, wallet entry, and order status entry.

- [x] Write a failing component test for current-plan highlighting, monthly/yearly offer grouping, payment-disabled copy, and an empty-state CTA.
- [x] Verify the focused test fails because no unified hub exists.
- [x] Implement the hub using existing `Panel`, `Table`, `Status`, and `Notice` primitives; keep internal assets behind human-readable labels.
- [x] Add navigation from overview and sidebar while retaining deep links to existing membership/add-on pages.
- [x] Add responsive styles for one-column phone layout and keyboard-visible controls.
- [x] Run focused and full web tests.
- [x] Commit `feat: add unified plans and usage hub`.

### Task 3: Improve checkout and post-purchase recovery

**Files:**
- Modify: `web/src/App.tsx`
- Modify: `web/src/forms.tsx`
- Modify: `web/src/ui.tsx`
- Modify: `web/src/styles.css`
- Test: `web/src/App.test.tsx`
- Test: `web/src/forms.test.tsx`

**Interfaces:**
- Consumes: existing `ActionSpec`, `Idempotency-Key`, order/operation readback, `payment_status`, `fulfillment_status`, `refund_status`.
- Produces: checkout summary with account, product, price, term, included credits, funding source, refund rule, and a post-submit status panel with refresh/readback actions.

- [x] Write failing tests for checkout summary, payment-disabled state, order accepted state, pending/unknown status, and readback without a second mutation.
- [x] Verify tests fail against the current generic confirmation modal.
- [x] Add a dedicated checkout modal/status component while reusing `act` for mutation and the existing polling cadence.
- [x] Show payment and fulfillment as separate statuses; show next action for `PENDING`, `UNKNOWN`, manual review, and success.
- [x] Add receipt/order links and a “查看账单” action after success.
- [x] Run focused and full web tests.
- [x] Commit `feat: add checkout status and recovery flow`.

### Task 4: Make recharge and wallet UX production-shaped

**Files:**
- Modify: `web/src/forms.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/styles.css`
- Test: `web/src/forms.test.tsx`

**Interfaces:**
- Consumes: wallets, operations, capabilities, recharge/transfer endpoints.
- Produces: wallet summary with human-readable units, top-up presets, explicit sandbox/production labeling, transfer direction, operation timeline, and readback CTA.

- [x] Write failing tests for no-payment state, sandbox-only state, preset amount selection, and UNKNOWN operation guidance.
- [x] Verify focused tests fail.
- [x] Add amount presets and a visible unit explanation without changing wire-format integer strings.
- [x] Separate “充值” from “统一钱包划拨” visually and explain account/product ownership.
- [x] Add operation timeline/status copy and a direct refresh action.
- [x] Run focused and full web tests.
- [x] Commit `feat: improve wallet and recharge experience`.

### Task 5: Extend admin billing profile and offer editors

**Files:**
- Modify: `web/src/admin/Operations.tsx`
- Modify: `web/src/admin/Operations.test.tsx`
- Modify: `web/src/admin/admin.css`

**Interfaces:**
- Consumes: existing admin billing-profile and offer mutation endpoints.
- Produces: full supported BillingProfile composition controls and offer fields for entitlements/refund/upgrade rules, with version/conflict handling.

- [x] Write failing tests proving the editor can select credits, prepaid cash, both, subscription requirement, model allowlist, refund rule, and upgrade rule.
- [x] Verify focused admin tests fail because the UI currently exposes only three presets and omits important Offer fields.
- [x] Implement a custom editor with client-side invariant hints matching BillingProfile.Validate; keep the server as final authority.
- [x] Preserve draft/publish semantics and show conflict/readback guidance.
- [x] Run admin tests and full web tests.
- [x] Commit `feat: expose composable billing configuration in admin`.

### Task 6: Add a third-party onboarding workspace

**Files:**
- Create: `web/src/admin/Onboarding.tsx`
- Modify: `web/src/admin/AdminConsole.tsx`
- Modify: `web/src/admin/admin.css`
- Test: `web/src/admin/Onboarding.test.tsx`

**Interfaces:**
- Consumes: operator-provided product values and existing read-only capability/status endpoints; no browser secret registration.
- Produces: honest step-by-step onboarding workspace for product ID, callback/origin checklist, scopes, manifest, billing profile, test connection, CLI command export, and readback checklist.

- [x] Write failing tests for required fields, least-privilege scope selection, generated prepare command, and blocked apply without server-side credentials.
- [x] Verify focused tests fail because the admin console has no onboarding route.
- [x] Implement the wizard as a plan/export surface; clearly label apply/readback as server-side operations.
- [x] Add it to the platform admin navigation with a dedicated permission.
- [x] Run focused and full web tests.
- [x] Commit `feat: add third-party onboarding workspace`.

### Task 7: Mobile/accessibility and visual QA

**Files:**
- Modify: `web/src/styles.css`
- Modify: `web/src/admin/admin.css`
- Test: `web/src/BillingHub.test.tsx`
- Test: `web/src/forms.test.tsx`

**Interfaces:**
- Consumes: all prior screens.
- Produces: phone-safe comparison rows, readable checkout modals, visible focus states, labels for controls, and honest loading/empty/error states.

- [x] Add tests for accessible names and disabled/aria explanations on critical purchase controls.
- [x] Verify failures before CSS/markup changes.
- [x] Tune breakpoints for plan cards, wallet forms, order timelines, and admin onboarding without changing business behavior.
- [x] Run web tests, build, and a static Markdown/link check for the user-facing integration guide.
- [x] Commit `fix: harden billing UX for mobile and accessibility`.

### Task 8: Final verification and release notes

**Files:**
- Modify: `docs/unihub-third-party-integration.md`
- Create: `docs/billing-ux-release-notes.md`

- [x] Run `npm --prefix web test` and `npm --prefix web run build`.
- [x] Run any existing SDK/billing tests required by the changed contracts.
- [x] Verify no new payment claim is made while `real_payments_configured` is false.
- [x] Document what is code-complete, what requires operator configuration, and what remains external-provider acceptance.
- [x] Commit `docs: document billing UX completion and acceptance boundaries`.

