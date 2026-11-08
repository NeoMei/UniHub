# SuperWagie direct merchant payments

Decision date: 2026-11-08.

## Confirmed choices and current status

The maintainer selected WeChat Pay and Alipay, using the product owner's own merchant accounts connected directly to the official APIs. Membership and add-on prices continue to come from UniHub's authoritative catalog.

The checkout backend increment is now implemented in this working tree: durable external checkout/receipt storage, separate external accounting, immutable cent-precision upgrade quotes, original-channel refund recovery, and ordinary/product-finance HTTP routes. These source changes and local PostgreSQL/Formance fixtures do not establish merchant configuration, deployed availability, live payment or production tariff approval. Missing providers return `payment_not_configured`. New non-sandbox `pricing_program=superwagie-ai-points-v1` sales require both explicit native-credit policy and the configured runtime protocol readiness probe; accepted payment/recovery never re-enters that new-sale gate.

## Customer experience

1. Select a membership or add-on and review the payer account, exact total, included points and expiration terms.
2. Select WeChat Pay or Alipay. Confirming creates one durable checkout with a fixed merchant order number, amount and expiration.
3. WeChat: show the Native payment QR code returned by the server. Alipay: open the official desktop checkout in the system browser, retaining the SuperWagie status panel.
4. The panel shows waiting for payment, verifying payment, activating benefits, or completed. Returning from a browser or pressing “I have paid” only queries status.
5. The app refreshes authoritative membership and points after verified payment and completed fulfillment. It does not automatically restart a previously failed AI task.
6. Reopening the panel recovers the original checkout. Switching payment channels first resolves or closes the old payment attempt; it must not create two simultaneously payable copies of one purchase.

The first release uses explicit one-time payments. Automatic recurring billing is a separate mandate flow and is not enabled by choosing these channels. Existing wallet purchase stays a separate legacy option.

WeChat Native returns a `code_url` for QR payment. Alipay's official `pageExecute` supports generating a signed URL for `alipay.trade.page.pay`; this avoids assuming access to the separate face-to-face payment product. [WeChat Native](https://pay.wechatpay.cn/doc/v3/merchant/4012791877), [Alipay SDK website-payment support](https://github.com/alipay/alipay-sdk-nodejs-all#pageexecute-示例代码).

## Required UniHub boundary

Introduce a persistent external checkout distinct from wallet recharge and existing wallet-funded commercial orders. Freeze the account, actor, product, offer snapshot/version, quote when applicable, amount, currency, provider, merchant configuration version, expiration and original idempotency key before issuing a payment artifact.

Implemented API routes (public prefix `/v1`; existing private finance proxy prefix `/internal/v1/product-finance`):

| Operation | API | Contract |
| --- | --- | --- |
| Create / recover checkout | `POST /accounts/{account}/checkouts` | Body identifies either an offer or an upgrade quote plus provider; original idempotency key required; client cannot set the amount |
| Read status | `GET /accounts/{account}/checkouts/{id}` | Account finance authorization and product scope; never creates another purchase |
| Request reconciliation | `POST /accounts/{account}/checkouts/{id}/reconcile` | Query the bound provider and recover the original operation |
| Close attempt | `POST /accounts/{account}/checkouts/{id}/close` | A close request is not proof of closure; do not release the purchase lock until resolved |
| Receive notification | Provider-specific webhook | Raw body verification, receipt binding and durable deduplication before acknowledgment |

All product-finance access must use the existing actor proof, scope, account role and product allowlist rules. Merchant identifiers are selected by trusted server configuration. Browser-supplied notify URLs, return URLs, credentials, amounts and gateway hosts are not accepted.

Do not route a verified external payment through `billing.Purchase` or `payLocked`: those methods always fund the purchase by debiting the product wallet, with an optional authorized unified-wallet transfer. Doing so after receiving external payment would charge twice or mix historical funds into the purchase.

Add an explicit external payment source and a verified-receipt settlement entry point. Record the actual provider payment transaction and frozen amount using durable, idempotent ledger commands; reuse the existing entitlement fulfillment only after payment posting is confirmed. The chosen accounting treatment must identify received-but-unfulfilled funds, including late payments requiring review, independently of the user's cash wallets. A provider receipt must never be represented by a fake sandbox settlement or by invented product/unified funding amounts.

Pending checkout exclusion belongs at the commercial-purchase boundary. Do not place an unpaid checkout in the existing `CREATED` billing-command queue: that queue also serializes model usage and would block ordinary AI work while someone considers a purchase. Do not hold an account lock or SQL transaction across a gateway network call.

## Recovery and payment evidence

Maintain separate payment and fulfillment states. Expected payment states include `CREATED`, `PENDING`, `UNKNOWN`, `PAID`, `CLOSED` and `REVIEW`; fulfillment includes `NOT_STARTED`, `PENDING`, `FULFILLED` and `REVIEW`. A payment error cannot overwrite an already verified success, and a failed fulfillment cannot make a paid checkout payable again.

- Only authenticated provider notifications or verified server-side queries prove payment. Validate the bound merchant, application where supplied, order number, exact amount, currency and transaction identity.
- Persist verified receipts and enforce unique provider/merchant/transaction identifiers. Duplicate notifications and repeated queries must converge on the same receipt, accounting reference, order and grant.
- Provider timeout preserves `UNKNOWN` and the original merchant order number. Do not retry with a new order or extend a recovered link's deadline.
- A timer expiring on the client is not proof that payment is impossible. Reconcile and close remotely. A late verified success must still be accounted for and either fulfilled under a valid frozen contract or placed into an explicit paid review/refund flow.
- Alipay page-pay links can exist before a provider transaction exists. A query or close returning trade-not-exist before the signed link's absolute deadline does not prove the link can no longer be paid. Use a fixed signed `time_expire`. [Alipay expiration fields](https://github.com/alipay/alipay-sdk-java-all/blob/master/v2/src/main/java/com/alipay/api/domain/AlipayTradePagePayModel.java).

## Provider adapters

### WeChat Pay

Use Native APIv3 with signed create, query and close calls. Verify response/notification signatures using the configured trusted WeChat key or certificate selected by serial, then decrypt successful notification resources with the APIv3 key. Validate the resulting application, merchant, merchant order, CNY amount and transaction before accepting the receipt. Persist before acknowledging. [Request signing](https://pay.wechatpay.cn/doc/v3/merchant/4012365336), [query](https://pay.wechatpay.cn/doc/v3/merchant/4012791880), [close](https://pay.wechatpay.cn/doc/v3/merchant/4012791881), [notification](https://pay.wechatpay.cn/doc/v3/merchant/4012791882).

### Alipay

Use `alipay.trade.page.pay`, with server-side RSA2 signing and a fixed official gateway; open the resulting URL rather than inserting arbitrary returned HTML into the application. Use `alipay.trade.query` for recovery and `alipay.trade.close` for closure. Validate signatures on the original response payload before trusting business status. For asynchronous notifications, validate the signature, app ID, seller ID, original order and exact total; query responses do not guarantee app ID or seller ID, so bind them to the order's trusted merchant configuration. A return URL is only navigation. [Official signing implementation](https://github.com/alipay/alipay-sdk-nodejs-all/blob/master/src/util.ts), [query contract](https://developer.alibaba.com/docs/api.htm?apiId=757&docType=4), [notification contract](https://developer.alibaba.com/docs/doc.htm?articleId=105902&docType=1&treeId=270).

## Amounts, upgrades and refunds

Keep all amounts exact. UniHub CNY/6 amounts must be divisible by 10,000 to become an integer number of fen. The four proposed product prices satisfy this condition. Never use floating-point conversion.

Existing prorated upgrade quotes can contain sub-cent amounts. An adapter must not independently round the signed charge while fulfilling the original quote. External upgrades use `POST /accounts/{account}/quotes` with `payment_source:"external"`. The separate returned quote records `rounding_rule:"cny_cent_ceil_v1"`, exact `original_amount`, rounded-up integer-fen `amount`, preserved `period_end`, and a maximum 15-minute `expires_at`. Accept it only through checkout. Default/wallet quotes retain their exact historical prorated amount and existing acceptance endpoint. Subscription-version drift, receipt after checkout closure/deadline, and delayed fulfillment past the deadline enter paid REVIEW; cash remains in the received-but-unfulfilled account without a grant.

External refunds require their own original-channel receipt and recovery state. Current `RequestRefund` returns funds according to `ProductFunding` and `UnifiedFunding`; it cannot safely refund external purchases. Setting both to zero and reusing it could revoke points and mark a purchase refunded without returning money. Preserve manual review for external refunds, and do not mark `REFUNDED` until the provider refund has been independently confirmed. Commercial launch requires an operable, auditable original-channel refund procedure even if the first version is reviewed manually.

## Merchant configuration to prepare

Credentials must be installed through protected server configuration or secret-file references, not sent in chat, put into the client, committed to source, or printed in logs.

| Channel | Merchant-side information | Server-only key material |
| --- | --- | --- |
| WeChat | Merchant ID, bound AppID, Native product permission, merchant certificate serial, trusted WeChat public-key ID/certificate set, public HTTPS notification address | Merchant API private key; APIv3 key; trusted WeChat payment public key/platform certificates |
| Alipay | AppID, seller ID, desktop website payment product permission, notification and return addresses, public-key mode | Application private key; trusted Alipay public key (certificate-mode parameters are not implemented) |

The user's channel and direct-merchant decisions do not establish that these merchant capabilities, credentials or callback domains are already configured. [Alipay official configuration guide](https://github.com/alipay/alipay-sdk-nodejs-all#平台配置).

## Implementation and verification order

1. Add durable checkout/receipt schemas, exact-money conversion, explicit external funding source, unique constraints and account/product-scoped APIs.
2. Add official provider adapters and protected startup configuration. Exercise signing, tampered bodies, unknown keys, wrong merchants/amounts, response loss, duplicate notifications and late success with controlled tests.
3. Add independent recovery and original-channel refund handling. Verify exact posting/grant counts with local PostgreSQL/Formance and simulated providers; these tests are not live-payment evidence.
4. Connect the SuperWagie payment selector, QR/official-link presentation, original checkout recovery and fulfilled-state refresh. Retain the existing new-points sales gate.
5. After merchant setup, perform explicitly authorized small real payments, callback/readback, interruption recovery and refunds; separately finish native CREDIT usage and model-rate acceptance before publishing the points offers.

No merchant account, production configuration, live transaction or new product publication was changed by this implementation. Commercial deployment still requires separately verified merchant credentials/callbacks and native runtime support.

## Implemented backend operational contract

- `POST /accounts/{account}/checkouts` accepts exactly one `offer_id` / external `quote_id`, `provider`, and the original Idempotency-Key. `GET` collection/item, `/reconcile`, `/close`, and `/refund` preserve the same provider order. Refund requires the original refund key and a reason of at most 80 UTF-8 bytes.
- Webhooks are `POST /payments/wechat/notify` and `POST /payments/alipay/notify`. Provider verification and durable unique receipt binding precede ACK. WeChat returns code SUCCESS; Alipay returns literal `success`. A ledger outage does not erase a persisted payment receipt.
- Unpaid checkout records never enter `billing_commands`. They exclude competing commercial purchases while ordinary model usage continues. Provider create/query/close/refund IO and runtime readiness probes run outside account locks.
- Verified receipt accounting posts from `world` into `commercial:external:unfulfilled:{checkout}` with an idempotent `external_payment` command. Entitlement fulfillment moves that amount to product revenue and grants points in the same fulfillment command. Product/unified user cash balances are never used; `Order.payment_source=external` identifies this path.
- Full unused/unreserved refunds freeze the source batches first. The provider refund uses the original payment transaction, one persisted refund ID and exact amount. Refund request acceptance is never completion evidence: an independent QueryRefund must confirm SUCCEEDED, the original transaction, refund ID and amount. Only then does the `external_refund` ledger command revoke remaining available points, reverse revenue/unfulfilled cash into the checkout refund-clearing account and apply REFUNDED. Ledger uncertainty retains PENDING and the provider receipt, so recovery never creates another refund. Consumed/reserved grants, changed subscriptions/fulfilled upgrades and unsupported policies enter REVIEW with frozen benefits.
- The old wallet refund/review endpoints reject external orders; they cannot falsely report money returned through wallet funding fields. Checkout response `refund_status=SUCCEEDED` requires both provider confirmation and ledger APPLIED. Its `fulfillment_status=NOT_STARTED` after refund denotes revoked benefits; the underlying order records CANCELED.
- The worker runs checkout/provider recovery and normal financial recovery with independent deadlines. Checkout attempts are ordered by the last attempted time, and completed manual-review records do not monopolize the bounded batch. NOT_FOUND before the original absolute provider deadline does not release the commercial lock. Close requests require verified terminal query evidence. Provider timeouts keep UNKNOWN and the original identity/deadline; Create is never called on replay.
- Official adapters expose `ConfigurationID()` binding provider, merchant and application identity. Checkout freezes that ID. Rotating signing/trusted keys for the same identity allows recovery; changing merchant/application fails closed. Finish existing orders/refunds before changing those identities, or retain a deployment using the original identity until its queue is resolved. No credential or private-key value belongs in snapshots or responses.
- `payment_providers` advertises only configured provider objects. `native_credits` requires both versioned product policy and the configured live runtime readiness probe. Configuration readiness does not assert completed live-payment acceptance.


## Executable configuration

Set `PAYMENT_MODE=direct` and an absolute `PAYMENT_PROVIDERS_FILE` on both API and worker processes. See [merchant-file example](../../examples/payments/direct-merchants.example.json) and [adapter configuration](../../internal/payments/README.md). The example contains no credentials and is not production-ready as supplied. Merchant private/APIv3 key files require owner-only filesystem permissions. Invalid supplied configuration refuses startup; omitting a channel keeps it unavailable. Do not configure `PAYMENT_PROVIDERS_FILE` in disabled/sandbox mode.

Configure the approved versioned native CREDIT rational and membership policy separately using the [native-credit contract](native-credit-metering.md). Preserve cash funding in mixed-mode profiles and use `require_subscription_for_credits` when only credit usage requires membership. Production prices/rates and merchant capability cannot be inferred from the local fixtures.

## SuperWagie recovery limit

The local commerce journal preserves original intents across a normal restart. After an ungraceful process exit, its existing writer lock can remain and refuse reopening with `platform_commerce_store_busy`. Follow the owning platform-account documentation: stop all writers, preserve `state.json`, and remove only the stale lock before reopening. This implementation does not promise automatic recovery of that local lock. Server-side provider receipts and ledger commands retain their independent durable recovery.
