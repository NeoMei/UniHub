# Account-scoped model consistency

`GET /v1/products/{product}/model-consistency?account_id={payer}&funding_mode={mode}` returns the existing model-consistency DTO for the selected payer and funding mode. `account_id` and `funding_mode` must both occur exactly once; `mode` is `prepaid_cash` or `credits`. Cash maps to `CNY/6`, credits to `CREDIT/6`.

The caller must use a valid access token, be an active current member of the payer account, and have `product:use` for a registered product instance. The product's active billing profile must permit the selected funding mode; a profile requiring a subscription also requires an active subscription for that payer. Product clients are bound to their registered product and instance, and need `catalog:read`. Console callers select the product in the path and use its default instance. A denied or unregistered scope returns no rate projection.

The response retains `product_id`, `asset`, `allowlist`, `published_rates`, `missing_models`, `extra_models`, and `duplicate_models`. Rates use the existing rate DTO and are limited to the selected asset and the product's allowed models. `extra_models` is empty in this scoped response. A nil model allowlist retains the existing unrestricted catalog policy; an explicit empty allowlist permits no models. This endpoint is read-only and does not create credentials, reserve funds, or change balances.

Requests without the two new parameters retain the established unscoped response and its default-funding behavior. Callers needing payer-scoped authorization must send both parameters; a scoped request with a missing, duplicated, or invalid parameter is rejected.
