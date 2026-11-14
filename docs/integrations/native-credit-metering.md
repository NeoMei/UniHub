# Native AI CREDIT metering

## Deployment state

The implementation supports an opt-in `CREDIT/6` native LLMRouter key contract. No production tariff is included, no existing key or wallet is converted, and no production deployment or provider/payment acceptance is implied. A compatible LLMRouter deployment and an explicit UniHub product conversion are both required before native points sales are ready.

## Authoritative configuration

`BillingProfile.native_credit_conversion` is optional. When present, it contains:

- `version`: an explicit tariff identifier, 1–128 ASCII letters, digits, dot, underscore or hyphen, starting with a letter or digit; the reserved cash identifier `lr-quota-usd-cny-v1` is rejected.
- `numerator`, `denominator`: canonical positive decimal int64 strings. They express **CREDIT/6 micro-units per native quota unit**, not CNY per CREDIT or supplier cost.
- `rounding`: exactly `lr-cumulative-ceil-v1`.

The charge is `ceil(native_quota × numerator / denominator)`, calculated with arbitrary precision intermediates and bounded to nonnegative int64. A zero quota costs zero. Apply rounding once to the cumulative charge; a top-up reserves the difference between cumulative charge and the current hold. This avoids per-fragment rounding drift.

The product must be `superwagie`, have an explicit positive profile version, enable usage and allow `credits`. The operator profile endpoint and `ParseBillingProfiles` deployment configuration support this field. Both profile persistence paths reject reusing a tariff `version` with changed parameters anywhere in that product's append-only history, including after an intervening tariff or removal. A new ratio requires a new version. Configuration must come from the approved commercial conversion policy; sample/test ratios are not production prices.

Cash mode and membership policy remain explicit: preserve `prepaid_cash` in `usage.funding_modes` when old cash keys must continue authorizing new calls. Existing `usage.require_subscription` remains a product-wide policy. The additive `usage.require_subscription_for_credits` flag (default false) requires membership only for CREDIT usage; it never exempts cash when the global flag is true. New addon purchases require membership when either flag is true on an explicit profile. Existing accepted calls retain their original quote and can settle/release after policy or key revocation. New holds and top-ups still enforce current authority and membership.

A compatible mixed-mode policy shape is below. `native_credit_conversion` deliberately remains null: an approved rational/version must be supplied before runtime readiness can become true. This is documentation, not deployment configuration.

```json
{
  "product_id": "superwagie",
  "version": 1,
  "subscription": {"enabled": true},
  "included_credits": {"enabled": true},
  "addons": {"enabled": true},
  "usage": {
    "enabled": true,
    "require_subscription": false,
    "require_subscription_for_credits": true,
    "funding_modes": ["prepaid_cash", "credits"],
    "default_funding_mode": "prepaid_cash"
  },
  "native_credit_conversion": null
}
```


## Key and catalog contracts

A new point key uses existing API-key creation with:

```json
{"model_scope":"llmrouter_available","funding_mode":"credits"}
```

The native account model-discovery request likewise accepts explicit `funding_mode=credits`; omitting it uses the profile default. Other required fields and normal product/actor/account/instance authentication still apply. The returned key has `pricing_source=llmrouter_native_credit`, `asset=CREDIT/6`, and `funding_mode=credits`. Omitting native key funding remains `prepaid_cash`, even when the current profile defaults to credits. A scope upgrade preserves the key's financial contract and cannot turn a cash key into a point key.

Credential verification and model discovery return `native_credit_conversion` with the authorized current conversion. API-key listing/creation responses retain the ordinary key shape; they do not claim a lifetime fixed tariff. LLMRouter freezes the verified conversion into each call's price snapshot before reservation.

Provider catalog responses advertise `native_credit_protocol=lr-native-credit-v1`. Missing support blocks point discovery and point-key creation; old cash catalog behavior remains compatible. Runtime readiness uses the existing service-authenticated provider-catalog endpoint with `purpose=native_credit_readiness`, `origin_product_id=superwagie`, and no actor/account/instance selectors. This read-only negotiation reports protocol support only if the adapter and its native principal/group are configured.

`modelaccess.NativeCreditsRuntimeReadiness(ctx, product)` combines this protocol negotiation with current UniHub conversion policy. It does not fabricate an account, require an already-active membership, contact a provider, prove a merchant is configured, or prove financial profitability. Account-specific discovery additionally verifies the real account and product access. Unknown/unreachable/old protocol responses fail closed.

## Reserve, settlement, and recovery

A point quote has `pricing_version=lr-native-credit-v1`, `asset=CREDIT/6`, and the configured `conversion`; other native quote fields retain their existing meaning. New reservation validates exact conversion equality under a product policy lock. A replay checks the original immutable request before consulting a newer tariff. Credentials must have the matching native-credit marker, mode, asset, owner, payer, product, instance, and credential version. A cash quote cannot use a point key, and a point quote cannot use a cash key.

The native quote and LLMRouter price snapshot freeze conversion and native pricing together. Native quota remains the provider-metered quantity. `provider_cost` remains an independent optional CNY/6 observation and never determines the point charge.

An optional settlement `usage.metered` observation may use `metered_v2` even
though the call rate retains `semantics="llmrouter_native"`. This is audit
evidence, not a central tariff: `QueryUsageSummary` validates it with the same
native usage rules as `SettleNative`, includes its dimensions in
`native_usage` / `native_metered_calls`, and preserves the frozen native charge.
Malformed measurements or mixed legacy counters keep the summary in an error
state. Historical call snapshots are not rewritten.

UniHub allocates eligible CREDIT sources in earliest-expiry order and reserves source batches. Frozen, future, or expired batches are ineligible. Insufficient credits never trigger a CNY wallet fallback. Limits and reports use the key/call's `CREDIT/6` asset. Settlement consumes original reservations; unused amounts return to the original source, or its expired bucket if its term ended. Release uses the same source-aware path. Existing source-freeze and commercial-refund controls apply to these same real batches.

Unknown reserve/top-up/settlement outcomes retain durable commands and original idempotency. LLMRouter top-up recovery validates that old CREDIT allocations remain an unchanged prefix before accepting appended source allocations, then persists the new funding snapshot. It never adopts a changed payer or replaced original batch. An over-hold actual charge remains pending until a successful budgeted top-up; it is not truncated or moved to cash. Service recovery does not dispatch another model request.

## Local evidence and limits

The focused tests use isolated PostgreSQL schemas and unique Formance ledgers on loopback. They cover point key creation, verification and catalog; cash key compatibility; exact conversion and immutable tariff history; reserve/replay, earliest expiry, membership, budget, frozen sources, no cash fallback, top-up, settlement, release, zero usage, source expiration, independent provider cost, reporting, and lost settlement recovery. Real UniHub HTTP handlers are exercised with controlled identity and provider-catalog fixtures.

LLMRouter tests use local SQLite plus controlled UniHub HTTP responses. They cover point grants, immutable snapshot validation, dispatch/settlement, top-up source extension, unknown or incorrect receipts and recovery, malformed/cross-asset conversions, protocol negotiation, and quote creation with invalid CNY conversion configuration. These tests do not constitute a deployed cross-process provider call or real payment acceptance.

## Client setup and insufficient-credit response

SuperWagie exposes explicit 积分 / cash selection for account-key setup. Credit setup requires authoritative readiness, sends `funding_mode=credits`, validates discovery conversion and the returned native-credit pricing marker, and uses a separate durable receipt. Existing cash keys/providers remain available; changing a model binding follows the existing explicit probe-and-apply workflow. Selecting a mode only reads state.

A proven rejected native reservation with the exact UniHub `insufficient_credits` response and an authenticated CREDIT/6, credits, native-credit key returns HTTP 402 and OpenAI `error.code=platform_model_credits_required`. This contract reaches ordinary native relay, pre-consume and OCR entry points. Budget refusals, cash shortages and unknown outcomes do not receive that purchase-guidance code. SuperWagie opens membership/addon selection; it does not purchase or repeat the failed model call.
