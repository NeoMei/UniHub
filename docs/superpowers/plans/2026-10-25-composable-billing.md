# Composable Product Billing Implementation Plan

> **For agentic workers:** Use subagent-driven-development for independent domain tasks; keep shared source ownership explicit and review all changes before completion.

**Goal:** Products configure composable subscription, monthly-credit, addon and direct CNY prepaid usage primitives using one backend.
**Architecture:** Versioned product billing profiles control new sales/access. Existing durable account commands support two separate usage assets with immutable per-call attribution; one generic llmrouter upgrade selects rates by verified asset.
**Tech Stack:** Go, PostgreSQL, Formance, TypeScript SDK, Python configuration tools.
**Spec:** ../specs/2026-10-25-composable-billing-design.md

## Global Constraints

- Cash is CNY/6, credits CREDIT/6; exact integer strings, no implicit conversion/fallback.
- All model cash is llmrouter product wallet; no source-product or unified-wallet debit without existing explicit funding flow.
- Preserve legacy profile/version0, paid terms and in-flight command recovery. Default membership+credits never falls back to cash.
- Headless API remains primary; billing configuration is separate from IdP client policy/gate.
- User-authorized scope is these three presets plus valid combinations, with direct cash chosen explicitly. No live merchant/provider purchase.

## Tasks

- [x] Profiles and pure subscription: add migration010 profile versions and credential columns; implement billing.Profile/Profiles/SaveProfile/ApplyConfiguredProfiles and templates; gate new catalog sales, accept monthly_credit0 for membership, skip zero monthly grants and require manual pure-member refunds. Test real PG/Formance legacy and profile transitions. Own profile.go/tests, catalog.go, purchases.go, upgrades.go/refunds.go only necessary guards.
- [x] Usage cash and credentials: add Rate.Asset and Call immutable billing fields, CNY hold/finalization, exported BillingUsageEligibility, profile-aware credential Issue/Verify, tests for no membership prepaid/insufficient cash/release/topup/recovery. Own billing/types.go, credits.go, cash_usage.go/tests, modelaccess/**. Consume ProfileOn and its exported access contract.
- [x] Generic router: read exact grant/rate asset, select without ambiguity, persist asset/funding, verify recovery and receipts; keep old empty CREDIT and model pin compatibility. Own only existing llmrouter integration platformbilling sources/tests after its instructions are read.
- [x] Controller/API/config/reporting: add independent PRODUCT_BILLING_FILE validated deployment configuration, ops profile update/read and product read; asset-aware reports, resources, SDK/OpenAPI and sample console; documented JSON presets and E2E. Root owns cmd/httpapi/query/SDK/docs/web (delegate distinct paths after first domain tasks finish).
- [x] Independent integrated review and regression, actual headless three-mode acceptance, preserve release main until implementation is verified; report actual deployment/publication state accurately.

## Rulings

- No extra design confirmation: the user explicitly requested composition and confirmed direct cash; prior shared model payer, monthly reset and headless ownership decisions remain authoritative.
- Profile selection can enable both assets but never guesses fallback. One call is one asset; optional credential funding_mode is an explicit user/product choice within trusted policy.
- Existing product registration alone retains legacy profile0. A declared profile can be operator-updated or deployed idempotently with explicit version, never silently roll back a newer published profile.
- Pure membership cannot infer unused service from absence of CREDIT consumption; newly authored pure offers with unused_only_v1 are rejected, preserving manual refund as safe default.

验收记录：docs/composable-billing-validation-2026-10-25.md。所有模式实现与验证完成；本轮不移动已发布标签、不自动修改线上产品收费配置。
