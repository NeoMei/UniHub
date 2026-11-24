# Product credits and shared wallet

A registered downstream client opts in with `X-UniHub-Product-Credit-Wallet: product_v1`. Use the new view only when `GET /v1/capabilities` returns `product_credit_wallet: true` and the product's managed model broker is available. Legacy requests continue using their existing contract.

- `GET /v1/accounts/{account}/product-credits` returns the calling product's credit lots, spendable and reserved amounts, active membership and paid-through date. An inactive membership leaves unexpired add-on lots intact. Previously admitted calls finish against their original allocations.
- `GET /v1/accounts/{account}/product-wallet` returns one CNY wallet, orders and renewal settings for the same payer and the client's registered downstream billing products. `llmrouter` is an upstream settlement account.
- `PUT /v1/accounts/{account}/product-wallet/{product}/renewal` updates `kind`, `enabled`, and `expected_version`. An add-on policy also accepts a published `offer_id`, an `exhausted` or `threshold` trigger, and the applicable `threshold` in `CREDIT/6`. Use the returned version for subsequent edits.

Exhaustion requires a confirmed zero balance. A threshold trigger requires a
balance below the configured threshold. When the shared wallet cannot fund a
triggered add-on, the same trigger waits for funds. The worker rechecks the
policy version, membership, and product credit balance before buying once.
- New checkout and Key creation use the opt-in header. Eligible offers carry `credit_scope: product_v1`. Wallet recharge uses the registered source product as order attribution and the same payer's unified wallet as the destination.

All CNY and credit amounts are decimal integer strings in their declared six-place assets. New product-scoped offers convert ¥1 of compute funding to 100 credits. Published offers freeze the service and compute allocation and the entitlement schedule. UniHub's ledger remains authoritative.

Subscription dates follow the activation day in Beijing time. A short month ends on its last day and the following months return to the original day as soon as it exists. Annual terms end on the corresponding date the following year. The first monthly grant is issued at activation. Each remaining monthly grant is issued at its period boundary and expires at the next boundary. A new add-on expires one month from its purchase. It preserves that deadline when a membership lapses or restarts.

An on-schedule automatic renewal uses the unified wallet and keeps the membership's original calendar anchor. An unsuccessful debit leaves the membership inactive. When the worker retries a declined renewal under the current authorization, the successful debit starts a full new term. A worker that already missed the first scheduled monthly grant also starts a full new term.

The [SuperWagie draft catalog](../../examples/billing/superwagie-product-credit-offers.draft.json) defines three undiscounted monthly and annual memberships. A paid membership includes one fixed monthly credit grant. The annual compute allocation funds 12 such grants. Add-on purchase prices fund only compute.

## Membership upgrades

A scoped upgrade quote charges the new offer's full price and estimates a return against the previous paid order. The new subscription starts when payment takes effect. The quote and checkout link the old order and subscription using `product_credit_upgrade`. The checkout also returns the new subscription ID. The new subscription receives its full first-period credits and an original calendar anchor.

Upgrade fulfillment freezes the old subscription lots and withdraws unused credits that remained valid at cutover. An in-flight call consumes its original reservation first. Unused reservations from a lot that was valid at cutover are withdrawn when the call is settled or released. Any confirmed amount above the original hold may be reserved from the active replacement subscription of the same product. Insufficient replacement credits leave the call pending. The old return combines unused service time with unused current and future subscription credits at the original compute allocation. Previously expired subscription credits and separate add-ons are outside the calculation. A provider refund uses the original transaction and is rounded down to the nearest fen. A wallet payment returns to the wallet that funded it.

Order states `UPGRADE_PENDING`, `UPGRADE_RELEASING`, `UPGRADE_RETURNED`, and `UPGRADE_REVIEW` describe the previous order's return. The worker records the cash and the original refund ID before sending it to the payment provider. It only finalizes the return after the original provider confirms the amount. A definitive provider closure retains the liability for review.

Product-scoped reservation and settlement amounts are signed 64-bit microcredit integers sent as strings. The gateway rejects quoted amounts that exceed the supported range before supplier dispatch.

## Managed model broker

In a trusted isolated product service, obtain the payer's product-scoped Key and call `POST /internal/v1/product-runtime/model-proofs`. The runtime credential needs the registered product and instance plus the `model:authorize` operation. Present the user's verified product access token in `X-UniHub-Actor-Authorization` and the scoped Key in the JSON field `token`. Configure a dedicated server-side `PRODUCT_MODEL_PROOF_SIGNING_KEY` (at least 32 bytes). Return the call result to the app through the broker; keep the runtime credential and durable Key outside app processes and provider requests.

The LLMRouter request carries the scoped Key, short proof in `X-UniHub-Product-Authorization`, and the same caller `Idempotency-Key` for a logical request. The gateway forwards the opaque proof on trusted verification and reserve requests. A verified proof is bound to the current issuer runtime, product, instance, account, actor, Key ID and Key version. Model admission rechecks membership and available product funds. Recovery follows the original scoped receipt.

### SuperWagie managed model policy

Set `usage.managed_models` in the SuperWagie billing profile. The profile version is also the policy version. An unconfigured billing profile omits the field. The public, unauthenticated `GET /v1/products/superwagie/managed-models` returns only `product_id`, `version` and `managed_models`. An enabled product without a policy returns `managed_models: null`. Signed-in SuperWagie `product_v1` native model discovery includes `managed_models` and `managed_models_version` when configured. Its `models` array still describes the available provider catalog.

Each configured operation lists public model IDs in order. The first chat ID is the default, and subsequent IDs are ordered fallbacks. An omitted operation inherits legacy behavior, and `[]` disables that operation. In the admin editor, leave an operation blank to inherit legacy behavior or enter `[]` to disable it. Separate IDs with commas. A profile with `{"chat":["deepseek-flash"]}` selects DeepSeek 4.1 Flash as the single managed chat model. Add vetted streaming and tool-call backups in the order SuperWagie should try them. Save validates the policy structure and public ID syntax. Verify live provider readiness through LLMRouter routing or direct probes.

Before admitting a new scoped SuperWagie call, the gateway verifies with the public `model` and actual `wire_operation`. For conversational requests, it derives `managed_operation` from the payload and forwards `"chat"` or `"vision"` on both `POST /internal/v1/model-credentials/verify` and `POST /internal/v1/calls/native-reserve`. This value is required on conversational wires when either operation has a configured list. Other operations derive from the wire. The identity-only verify uses no model or wire. The model-and-wire verify uses the default purpose, and historical receipt verification uses `purpose: "receipt"`.

A policy rejection returns HTTP 403 with `{"error":"managed_model_not_allowed"}` before creating a new hold. Existing reserve retries, settlement and receipt reads use the original call contract. Keep the policy unconfigured during rollout. Deploy UniHub’s accepting decoder before the gateway begins sending `managed_operation`. Publish a policy only after verifying that the gateway forwards the classification on both paths.

Admin saves append a database version. A restart without a staged SuperWagie entry uses that version. When `PRODUCT_BILLING_FILE` contains SuperWagie, a stale version or different content at the same version blocks startup and preserves the database snapshot. An explicitly higher file version replaces the whole profile. For admin-managed policy, omit the SuperWagie entry from the staged file. File-managed policy can use `usage.managed_models` in the reviewed SuperWagie profile passed to `stage-billing`.

## Activation

Configure and verify the LLMRouter tariff and product routes, then publish a scoped UniHub billing profile and new offers. Register each product instance and its authorized shared-wallet billing products and scopes. Use a pilot payer to check split, issue, consumption, monthly expiration, renewal and same-order refund. Independently reconcile legacy cash, credit lots, unsettled calls, payment intents and merchant receipts before any historical migration.
