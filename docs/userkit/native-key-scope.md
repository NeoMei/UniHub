# LLMRouter catalog and pricing for a managed API Key

Registered SuperWagie instances can use `model_scope: "llmrouter_available"` for
durable API Keys funded by `prepaid_cash` in `CNY/6`. LLMRouter owns the current
model catalog, route admission and native prices. UniHub retains Key ownership,
account membership, product and subscription access, budgets and ledger commands.
Task credentials and existing `selected` Keys retain their finite model scope.

## Key lifecycle

`POST /v1/accounts/{account}/api-keys` accepts `model_scope`. Omission means
`selected`. Dynamic Keys can start with an empty `models` array. The array is
historical metadata for dynamic Keys and does not authorize or limit models.

`POST /v1/accounts/{account}/api-keys/{key}/scope` accepts
`{"model_scope":"llmrouter_available","origin_product_id":"superwagie","instance_id":"default"}`.
The origin and instance must match the authenticated product policy. The actor
must own the Key or hold the account's owner or billing-admin role. Both actor
and Key holder must retain account and product access.

This one-way transition preserves the bearer digest, Key ID, payer, limits,
expiry and funding. It adds an audit event and increments `credential_version`.
Repeating the request is idempotent. Responses contain `model_scope`,
`pricing_source: "llmrouter_native"` and `credential_version`; only creation
returns a bearer. The private product-finance route exposes the same action.

## Catalog

`POST /v1/accounts/{account}/model-discovery` accepts `model_scope`, defaulting
to selected. The dynamic path validates the actor and payer, then calls the
fixed LLMRouter `POST /internal/v1/provider-catalog` endpoint. Configure both
`LLMROUTER_PROVIDER_CATALOG_URL` and `LLMROUTER_PROVIDER_CATALOG_TOKEN`. The URL
requires HTTPS or loopback HTTP and the exact endpoint path. Redirects are
disabled. Requests time out after 5 seconds and responses are limited to 2 MiB.

The request carries `origin_product_id`, `instance_id`, `actor_id`, `account_id`.
The response must echo that scope and return `schema_version: 1`, a nonempty
`catalog_version`, `model_scope`, `pricing_source` and a `models` array. Model
fields retain the existing discovery shape. `capability_metadata` preserves
LLMRouter's operation metadata unchanged. A failed or malformed response is an
error. Dynamic discovery has no central profile-model or tariff intersection.
The actual Key's `GET /v1/models` follows the same live catalog.

Internal introspection validates Key authority without calling the router back.
LLMRouter checks current model and operation availability before dispatch.

## Native quote and settlement

Only the authenticated LLMRouter billing service can call
`POST /internal/v1/calls/native-reserve`. Its body contains `credential`,
`instance_id`, `task_id`, `call_id`, `request_digest`, `model`, `wire_operation`
and `quote`. The quote contains:

- `pricing_version: "lr-native-v1"` and the lowercase SHA-256 `pricing_digest`.
- `billing_model`, `wire_operation`, `route_id`, `channel_id` and server-selected `group`.
- `native_quota_max`, `asset: "CNY/6"` and `max_amount`.
- `conversion` with `version: "lr-quota-usd-cny-v1"`, positive `numerator` and
  `denominator`, and `rounding: "lr-cumulative-ceil-v1"`.

Numeric values are canonical decimal strings. Legacy routes use `route_id: "0"`;
channel IDs are positive. UniHub verifies that `max_amount` is the ceiling of
quota multiplied by the frozen conversion. LLMRouter persists the complete
native pricing snapshot before dispatch. Its digest binds that snapshot to the
call. The call stores `native_pricing` and `credential_version`.

`POST /internal/v1/calls/{call}/native-settle` accepts `pricing_digest`,
`native_quota`, `amount`, optional `usage`, and optional decimal `provider_cost`.
UniHub verifies the actual amount against the same conversion. The ordinary
settle endpoint rejects native-price calls. Native settle rejects selected-price
calls. A changed quote or final replay conflicts.

The existing call read, top-up and release routes remain available. A charge
above the hold returns `operation_pending` before any settlement posting. A
successful top-up must pass the original Key and account budgets before retrying
settlement. Charges are never truncated to the hold. A zero-price call creates
call and command evidence without a zero-value ledger posting.

A new native reservation rejected solely by a Key or account budget returns
`402 {"error":"usage_budget_exceeded"}` before any call, command or hold exists.
Only this exact response proves a budget rejection has no hold. Other `403`
responses and uncertain writes retain their existing reconciliation behavior.
Selected-scope reservation and top-up budget denials retain `403`.

Calls remain in `billing_calls`. Prepaid funding uses the existing LLMRouter
product wallet for the selected personal or team payer. The unified wallet and
native runtime identity bindings are unchanged. Native admissions, top-ups and
command recovery acquire administration locks before the account lock. Accepted
financial obligations can settle after subsequent Key revocation or expiry.
A legacy call created before scope upgrade keeps its original price snapshot.
Its top-ups and recovery continue with the retained model history and current
Key, account and budget checks. The upgraded Key cannot start new legacy-price
reservations.

Public call reports identify the price owner as `product` and do not expose
the native price snapshot. Complete legacy token counters remain reportable.
Other native observations stay in the internal receipt; their public token
coverage is unavailable. Missing observations do not erase settled charges.

Migration `025_native_key_scope.sql` is additive. Deploy compatible router and
UniHub servers before upgrading Keys. An older binary cannot execute this new
pricing protocol; rollback must coordinate both servers and preserve the
schema, Key scope and in-flight call records for recovery.
