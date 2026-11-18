# Product credits and shared wallet

A registered downstream client opts in with `X-UniHub-Product-Credit-Wallet: product_v1`. Use the new view only when `GET /v1/capabilities` returns `product_credit_wallet: true` and the product's managed model broker is available. Legacy requests continue using their existing contract.

- `GET /v1/accounts/{account}/product-credits` returns the calling product's credit lots, spendable and reserved amounts, active membership and paid-through date. An inactive membership leaves unexpired add-on lots intact. Previously admitted calls finish against their original allocations.
- `GET /v1/accounts/{account}/product-wallet` returns one CNY wallet, orders and renewal settings for the same payer and the client's registered downstream billing products. `llmrouter` is an upstream settlement account.
- `PUT /v1/accounts/{account}/product-wallet/{product}/renewal` updates `kind`, `enabled`, and `expected_version`. An add-on policy also accepts a published `offer_id`, an `exhausted` or `threshold` trigger, and the applicable `threshold` in `CREDIT/6`. Use the returned version for subsequent edits.
- New checkout and Key creation use the opt-in header. Eligible offers carry `credit_scope: product_v1`. Wallet recharge uses the registered source product as order attribution and the same payer's unified wallet as the destination.

All CNY and credit amounts are decimal integer strings in their declared six-place assets. The cash-to-credit ratio is exactly 1:10. Published offers freeze the service and compute allocation and the entitlement schedule. UniHub's ledger remains authoritative.

The first scoped gateway protocol retains the metering bound of 2,147,483,647 microcredits per reservation (2,147.483647 credits, equivalent to about ¥214.75). Requests that need a larger reservation are refused before supplier dispatch.

## Managed model broker

In a trusted isolated product service, obtain the payer's product-scoped Key and call `POST /internal/v1/product-runtime/model-proofs`. The runtime credential needs the registered product and instance plus the `model:authorize` operation. Present the user's verified product access token in `X-UniHub-Actor-Authorization` and the scoped Key in the JSON field `token`. Configure a dedicated server-side `PRODUCT_MODEL_PROOF_SIGNING_KEY` (at least 32 bytes). Return the call result to the app through the broker; keep the runtime credential and durable Key outside app processes and provider requests.

The LLMRouter request carries the scoped Key, short proof in `X-UniHub-Product-Authorization`, and the same caller `Idempotency-Key` for a logical request. The gateway forwards the opaque proof on trusted verification and reserve requests. A verified proof is bound to the current issuer runtime, product, instance, account, actor, Key ID and Key version. Model admission rechecks membership and available product funds. Recovery follows the original scoped receipt.

## Activation

Configure and verify the LLMRouter tariff and product routes, then publish a scoped UniHub billing profile and new offers. Register each product instance and its authorized shared-wallet billing products and scopes. Use a pilot payer to check split, issue, consumption, monthly expiration, renewal and same-order refund. Independently reconcile legacy cash, credit lots, unsettled calls, payment intents and merchant receipts before any historical migration.
