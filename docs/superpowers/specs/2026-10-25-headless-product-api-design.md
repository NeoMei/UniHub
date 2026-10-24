# UniHub headless product API contract

The user explicitly requires UniHub to be a standalone backend for independent products: every product owns its UI, integrates API results into its own business logic, and must not depend on the UniHub console for identity, recharge or resource queries. This is the approved scope; this document records it without a new approval round.

## Responsibilities

UniHub owns verified identity, user/team membership, explicit product capabilities, authoritative cash and model-credit accounting, immutable commercial terms, payment verification and trusted model settlement. Products own UI, feature presentation, prompts and local feature decisions. The existing console remains a reference client and is not a required integration surface. Backend API access and account/asset authorization remain enforced regardless of client UI.

## Identity

Casdoor remains password and OAuth token authority. UniHub exposes JSON registration (create-only, no automatic login), password authorization and PKCE code exchange. Public native clients keep their verifier and never embed client secrets. The API fixes the configured organization/application/client/redirect, requires S256/state/nonce, binds upstream code+nonce+client+redirect in durable expiring one-use transactions, verifies issued access/ID tokens, and never persists passwords or returns management credentials. Unsupported MFA, CAPTCHA, consent and forced password changes return explicit interaction-required results; they never bypass upstream checks. Registration flags default off, password policy is explicit, email is unverified, ambiguous create results recover by a preallocated user ID and never overwrite an existing user. Unused refresh tokens are not offered as working refresh support.

Identity management uses a separate organization-bound server credential, never a built-in administration application and never a product secret delivered to a client. Its adapter permits only the exact fixed create/read/policy operations. Authentication-body and upstream audit privacy are release gates; nonce/state/code must not enter logs, databases or outgoing audit webhooks unsanitized. Capabilities stay disabled if that gate is unverified.

## Product registry and authorization

A strict array of public Client records is indexed by distinct client_id. Fields are product_id, client_id, application, organization, redirect_uris, origins, scopes, billing_products, password_login and registration. No secret is allowed in this file. One product may have multiple clients. Audience overlap with the console, operations or another client is invalid. Legacy environment clients retain their old limited allowlist; explicit clients receive exactly their configured scopes and no implicit finance permission.

Scopes govern which APIs a product may call; all existing account RBAC, advisory locks, current-member checks and money invariants remain. Wallet/order/subscription/quote/settings data are restricted to the current product and explicitly configured billing products. Shared model credits are account resources and are returned through safe resource projections without exposing another product's private order/provider details. Model credentials stay bound to origin and task; revocation cannot cross origin.

Products may create recharges for their approved product wallets and perform approved unified-to-product transfers. Unified-wallet direct recharge through product credentials remains unavailable until immutable origin attribution exists; console user APIs retain that existing capability. This boundary matches the first-phase per-product recharge policy and must be visible in capability metadata, not claimed as a complete supported route. Production still rejects an unconfigured payment channel; sandbox does not mean real funds.

Browser CORS accepts only registered exact HTTPS origins or explicit loopback development origins, supports bearer requests without cross-origin cookies, and matches the authenticated client on actual requests. Native/server calls without Origin remain supported. No trusted-user request header or arbitrary actor/body claim is introduced.

## Query and integration

Existing account/team/wallet/subscription/purchase/settings APIs are reusable without UI under explicit scopes. Add capability/resource summary, per-call query and usage summary APIs. Calls support bounded time/model/origin filters and keyset pagination; a product's origin is derived from verified registration and cannot be overridden. Members see their own calls without financial details; owners/billing administrators may see account calls for that product. Results state actor/account visibility and live-query accuracy, omit provider cost/digests/allocations, and never invent timestamps for old data. Summary quantities are exact integer strings, settled charges distinct from pending/unknown counts.

Publish an actual UniHub OpenAPI contract, a portable TypeScript HTTP SDK and an independent headless client acceptance flow. SDKs contain no UI or product feature policy. They surface pending/rejected/conflict states without automatic financial retries or guessed success. A newly configured product ID must work without adding a source-code switch.

## Acceptance and deployment

Test with real isolated PostgreSQL and Formance and real signed issuer fixtures, then actual Casdoor JSON registration/login/token and HTTP financial/query flows without loading any console HTML. Preserve old product and console authentication tests. Reject revoked roles, cross-account and cross-product access, wrong origins, unsafe scopes, code replay/mismatches and malformed money. Complete independent review before local commit and controlled server upgrade. Real payment merchants/providers remain external configuration, not simulated production success.
