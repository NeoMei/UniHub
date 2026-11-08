# Official direct merchant adapters

`Provider` implements WeChat Native APIv3 and Alipay desktop `alipay.trade.page.pay` with Go's standard library. It does not store checkout state, post accounting entries, grant benefits or acknowledge callbacks. The billing service must durably persist and deduplicate a verified receipt before acknowledging it.

## Startup configuration

Construct `NewWeChat(WeChatConfig)` or `NewAlipay(AlipayConfig)` only from protected server configuration. Missing required configuration returns `ErrUnconfigured`; malformed supplied keys, identifiers or URLs return `ErrInvalid`. Advertise only successfully constructed providers. Neither adapter accepts a gateway address from a payer or configuration: production endpoints are fixed to `https://api.mch.weixin.qq.com` and `https://openapi.alipay.com/gateway.do`.

- WeChat: `AppID`, `MerchantID`, `CertificateSerial`, `NotifyURL`, RSA `PrivateKey`, exactly 32 bytes of `APIv3Key`, and `PlatformKeys` keyed by the exact platform certificate serial/public-key ID. Explicitly trust and provision these platform keys; an unknown notification/response serial fails verification. The private key must match the installed merchant certificate serial.
- Alipay: `AppID`, `SellerID`, `NotifyURL`, `ReturnURL`, RSA application `PrivateKey` and trusted Alipay `PublicKey`. This implementation supports **public-key mode**. Certificate-mode request serial/root-certificate parameters are not implemented. Loading a certificate to extract its public key does not implement Alipay certificate mode.
- `ParsePrivateKeyPEM` supports one PKCS#1 or PKCS#8 RSA key. `ParsePublicKeyPEM` supports one PKIX/PKCS#1 RSA public key or a currently valid X.509 certificate. Keys must be at least 2048 bits. Startup owns secret-file permissions and identity/certificate provisioning. These helpers never fetch or trust an unknown remote certificate.
- `HTTPClient` optionally supplies a trusted server-owned transport/proxy. Its client value is copied, default timeout is 20 seconds, and redirects are always disabled. Tests use this seam to route fixed official URLs to local TLS fixtures. Key pointers supplied to constructors must remain immutable for their lifetime. Rotation requires constructing a new adapter; no automatic certificate refresh is performed.

Both callback/return URLs must be HTTPS with no credentials/fragment and no nonstandard port. Returned Alipay sessions always use the fixed official HTTPS gateway. WeChat sessions must have the official `weixin://wxpay/bizpayurl` shape. No provider HTML is executed.

The executable loads these adapters with `PAYMENT_MODE=direct` and an absolute `PAYMENT_PROVIDERS_FILE`. The [example](../../examples/payments/direct-merchants.example.json) contains placeholders and file references only. Omit a channel to disable it; at least one must be configured. Private key/APIv3 key files must be regular absolute paths, at most 64 KiB, with no group/other permissions. Unknown JSON fields, invalid keys and incomplete channel configuration fail startup. API and worker processes need the same configuration.

`ConfigurationID()` binds provider, merchant/seller and application identity, and is frozen on checkout. Signing/trusted key rotation under that same identity permits recovery. In static-file mode, changing merchant or application fails closed for existing orders; finish their recovery first or retain the original deployment/configuration until resolved. Managed administration retains each original identity in encrypted PostgreSQL revisions and selects its latest credentials for recovery, even after the current channel is disabled or replaced. See [managed configuration](../paymentconfig/README.md). No key material enters the identity hash or order snapshot.

## Accounting and state contract

Amounts are canonical positive decimal **integer CNY/6** strings. One fen is 10,000 units. Sub-cent, noncanonical, zero, negative and overflow inputs are rejected; no adapter rounds. WeChat's integer request amount is additionally bounded to signed 32-bit fen. Provider decimal amounts are parsed exactly without floating point. The billing service must compare each verified receipt against its frozen amount, currency, merchant configuration and order/transaction identity.

`Result.Status`: `PENDING`, `PAID`, `CLOSED`, `NOT_FOUND`. A non-success or unknown business/protocol status remains an error, not closure. In particular, Alipay `NOT_FOUND` before the fixed signed page-pay expiration does **not** prove that the link is unpayable. Close failures never imply successful closure. A transport failure does not prove that a create, close or refund was not executed; recover with the original identifiers.

`RefundResult.RefundID` is the caller's stable merchant refund ID. `TransactionID` is the original payment transaction ID. Refund calls submit that same ID on every retry and return `PENDING` after successful acceptance; use `QueryRefund` to independently establish `SUCCEEDED`. WeChat requires the returned original order, transaction, refund ID, total/refund amounts, CNY and `ORIGINAL` channel to match. An alternative channel or abnormal refund is left for review. Alipay confirms only exact per-request `refund_amount` and matching IDs with `REFUND_SUCCESS`; `refund_fee` from the submission is cumulative and cannot establish that result. A verified Alipay `10000` query without `refund_status` means the refund was not received or failed: it maps to retryable `NOT_FOUND`, so billing submits/retries the same refund ID and amount. Any supplied identity/amount must match even in that unconfirmed state. If query supplies `total_amount`, it must match the original total.

## Verification and notification ownership

WeChat signs each HTTP method/path/query/body and verifies the original response bytes, trusted serial, timestamp (five-minute window), nonce and RSA-SHA256 signature before decoding. Successful notifications additionally require authenticated AES-256-GCM resource decryption and matching app/merchant. Alipay signs RSA2 requests, verifies the unmodified response JSON member bytes, and binds the returned order to the trusted request. Query responses can omit app/seller; any supplied values must match configuration. Form notifications must contain matching app and seller, RSA2 and a valid signature.

Ambiguous duplicate JSON fields (including case-fold equivalents), duplicate form parameters and duplicate WeChat signature headers are rejected. Notifications may legitimately be delivered repeatedly; the adapter verifies them repeatedly, while the billing receipt store owns durable replay/idempotency protection. The adapter never trusts a browser return URL as payment evidence. Sanitized sentinel errors do not contain gateway response bodies, authorization material or credentials.

## Primary protocol references

Checked 2026-11-08; these references establish protocol contracts, not merchant enablement or live acceptance.

- [WeChat Native create](https://pay.wechatpay.cn/doc/v3/merchant/4012791877), [request signing](https://pay.wechatpay.cn/doc/v3/merchant/4012365336), [payment notification](https://pay.wechatpay.cn/doc/v3/merchant/4012791882).
- [WeChat Native refund submission](https://pay.wechatpay.cn/doc/v3/merchant/4012791883) and [refund query](https://pay.wechatpay.cn/doc/v3/merchant/4012791884): original refund identifiers, amounts, status and original channel; acceptance requires subsequent confirmation.
- [Official Alipay page-pay model](https://github.com/alipay/alipay-sdk-java-all/blob/master/v2/src/main/java/com/alipay/api/domain/AlipayTradePagePayModel.java) and [official SDK signing/page execution](https://github.com/alipay/alipay-sdk-nodejs-all/blob/master/src/alipay.ts).
- [Official Alipay refund response model](https://github.com/alipay/alipay-sdk-java-all/blob/master/v2/src/main/java/com/alipay/api/response/AlipayTradeRefundResponse.java) and [refund query contract](https://aipay.alipay.com/docs/vibe-pay/ai-web-app-payment-qianyi/api-list/alipay-trade-fastpay-refund-query.html): query success code alone is insufficient; require `REFUND_SUCCESS` and the per-request amount.

Focused local verification: `go test ./internal/payments -count=1` and `go test -race ./internal/payments -count=1`. Fixtures generate ephemeral RSA keys, serve signed responses over local TLS, and verify outgoing request signatures. No tests connect to merchants or exercise real money.

## Administration metadata

`ConfigurationMetadata()` exposes public merchant/application identifiers, certificate serial and trusted platform key IDs, fixed URLs and credential-presence booleans. It exposes no key contents, fingerprints or file paths. The platform finance-read endpoint uses this projection for read-only static mode; ordinary product capabilities receive channel names only.
