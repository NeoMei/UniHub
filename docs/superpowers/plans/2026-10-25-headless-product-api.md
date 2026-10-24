# UniHub independent product API implementation plan

**Goal:** Complete the user-approved API-first integration surface, leaving UI and feature decisions to products.
**Spec:** ../specs/2026-10-25-headless-product-api-design.md
**Workspace:** Existing dedicated codex/account-wallet-foundation checkout; base d303451. No unrelated repositories or production services are changed.

## Global constraints

- Preserve account RBAC, exact independent token audiences, ledger authority, decimal integer strings, money command locks/idempotency and unknown-result recovery.
- Product policy is explicit, default deny for new capabilities; clients cannot self-grant scopes, actor IDs or trusted settlement credentials.
- No required console/HTML redirect for the supported headless API workflow; the current console remains a separate example client.
- Public native clients use S256 PKCE; no management secret or long-lived shared secret in a native binary.
- Sandbox prices/payments remain explicitly sandbox. No real merchant/provider execution without configured authorized credentials.
- Registration/password flows remain disabled until upstream policy and audit privacy gates have been verified.

## Tasks

- [x] Identity adapter and JSON endpoints (agent headless_identity_design): own internal/identityapi/** and migration009; exact transactions, create-only registrations, safe upstream DTOs, rate limits, truthful challenges, signed-token checks, tests.
- [x] Product registry and scoped APIs (agent headless_product_access): own internal/products/**, httpapi policy/filters, focused wallet visibility and billing product lookups; exact origins, resource scopes and account RBAC; tests.
- [x] Usage query domain (agent headless_api_design): own billing/query.go/tests and migration008; trustworthy times, live keyset pagination, integer summaries and role/origin privacy; real PG/Formance tests.
- [x] Integration/controller: configuration and handler wiring, origin-bound model credential revocation, upstream audit privacy deployment solution, SDK/OpenAPI/docs, generic product onboarding and headless HTTP acceptance.
- [x] Independent review/fixes, targeted and integrated verification, controlled deployment, local commits, report implementation and external boundaries accurately.

Implementation commit: `5c9d4c4`; proxy-denial acceptance correction: `5a38aab`. Server deployment `20261025-headless-v1`, real privacy gate, public JSON+SDK flows and restart persistence accepted; see `docs/headless-product-api-validation-2026-10-25.md`. Real payment setup and later product adapters remain explicitly outside this phase.
