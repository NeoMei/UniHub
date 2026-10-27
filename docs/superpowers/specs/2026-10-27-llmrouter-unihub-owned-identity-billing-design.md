# LLMRouter UniHub-Owned Identity and Billing

## Goal

Make UniHub the only user-facing identity and billing authority for LLMRouter while preserving a separately protected native operator/compatibility surface for migrations, channel administration, diagnostics, and explicitly approved legacy clients.

## Decision

Public LLMRouter access uses UniHub identity, account selection, subscriptions, wallets, model credentials, model rates, reservations, and settlement. LLMRouter remains the model execution service: it owns provider channels, account pools, upstream retry/recovery, technical request logs, and the trusted model-service adapter.

LLMRouter's native users, native API tokens, local wallet/quota, top-up pages, subscriptions, and public login are removed from the normal product surface. They remain available only behind an internal operator/compatibility boundary that is disabled for public origins and cannot be selected by a UniHub platform call.

## Public and internal boundaries

### Public UniHub-owned surface

- `/unihub` product login and account projection.
- UniHub model-credential issuance for `llmrouter` and explicit account/task/model binding.
- `/platform/v1/chat/completions` and supported streaming/function-tool requests.
- UniHub model rates and CNY/CREDIT settlement.
- Public model catalog derived from published UniHub rates and the approved product allowlist.
- Technical status/receipt views that omit provider secrets, native wallet state, and native user identifiers.

### Internal LLMRouter surface

- Channel/provider/account-pool administration.
- Supplier credentials, channel health and model routing diagnostics.
- Native user/admin login only through a private management origin or management token.
- Legacy native relay/API tokens only when an explicit compatibility switch and trusted source policy are enabled.
- Native billing records remain auditable for the compatibility surface but are never used for UniHub platform traffic.

## Request and billing flow

1. A user authenticates to UniHub and selects a personal/team payer.
2. UniHub issues a short-lived model credential bound to actor, account, origin product, task, funding mode, asset, profile version, and allowed model.
3. LLMRouter verifies the credential with UniHub before upstream dispatch.
4. LLMRouter reserves the exact published UniHub rate, stores the immutable call snapshot, and sends the request to the selected provider channel.
5. LLMRouter normalizes observed usage and settles or releases the UniHub call exactly once.
6. Replays use the same logical call ID and request digest; they never dispatch a second supplier request.

Native wallet/quota pre-consumption is bypassed for this path. A missing or rejected UniHub grant fails before provider dispatch and never falls back to native billing.

## Data migration

- Do not copy native users, passwords, API tokens, balances, subscriptions, or referral state into UniHub automatically.
- Keep existing native rows and ledgers for internal compatibility and reconciliation.
- Add a read-only mapping/reporting view only when an operator explicitly binds an old account to a UniHub account; binding never silently transfers balances.
- Disable new public native top-ups and subscriptions before enabling the UniHub-only product gate.
- Preserve rollback environment files, database dumps, and the previous public route configuration for every deployment.

## Configuration contract

LLMRouter deployment must have:

- `UNIHUB_PRODUCT_ENABLED=true`.
- `UNIHUB_PLATFORM_ENABLED=true` for the public model route.
- A non-empty `UNIHUB_PRODUCT_MODELS` allowlist.
- One unambiguous published `CNY/6` or `CREDIT/6` rate for every allowlisted model.
- A dedicated zero-quota routing principal and fixed routing group.
- A server-only `UNIHUB_MODEL_SERVICE_TOKEN`.
- `NATIVE_PUBLIC_ACCESS=false` (new) unless a controlled compatibility migration explicitly enables it.
- `NATIVE_ADMIN_ORIGIN` or equivalent private management policy for operator routes.

SuperWagie managed runtime defaults to the public LLMRouter platform gateway and published model catalog when no local override is supplied. The runtime keeps local provider credentials and ordinary OpenCode providers separate from the UniHub-managed provider.

## Failure and recovery rules

- UniHub unavailable: reject new platform calls; do not use native balance fallback.
- Rate missing or ambiguous: reject before reservation and provider dispatch.
- Reservation unknown: retain durable intent and require reconciliation.
- Settlement unknown: retain the original call and block automatic client retry.
- Native compatibility disabled: reject native login, token creation, top-up, and relay with an explicit migration/disabled response.
- Internal operator authentication failure: reject without exposing UniHub or provider credentials.

## Acceptance criteria

- Public LLMRouter login and account resources come from UniHub only.
- Public native signup, native top-up, native subscription, and native user-token routes are unavailable.
- Internal operator/channel management still works through the private boundary.
- Every published SuperWagie model has a UniHub rate and an LLMRouter route.
- SuperWagie can discover the UniHub-managed model catalog with no local model override.
- A valid model credential can make an ordinary/streaming call and settle once.
- Replaying the same logical call cannot dispatch twice or double-charge.
- Invalid, expired, revoked, wrong-account, and wrong-model credentials fail before provider dispatch.
- Existing native compatibility calls remain available only when the explicit internal switch is enabled.

## Out of scope for this migration

- Moving OCR, embeddings, rerank, speech, image, or video operations into the generic chat grant. Each requires its own UniHub operation contract and native-unit rate.
- Real merchant/payment activation beyond the currently configured UniHub environment.
- Automatic historical balance conversion or account identity merging.
