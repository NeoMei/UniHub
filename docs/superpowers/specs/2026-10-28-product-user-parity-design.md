# Product user parity and UniHub adapters

Status: approved in conversation on 2026-10-28. This supersedes the two-business-path design in `2026-10-28-nonbreaking-unified-users-design.md` wherever they conflict.

## Approved requirements

- UniHub unifies identity, authorization management and the final financial ledger. LLMRouter retains its complete product, metering and price calculation, and settles through an adapter.
- Under equal product roles, resources, settings and funds, product behavior and pages are identical for every identity source. Login and registration keep the original product experience; UniHub supplies the integrated backend capabilities.
- A mature product retains its existing user subsystem and authenticators. UniHub maps its users and financial accounts through adapters. A new product without a user subsystem may build its own subsystem from UniHub source components and extend it.
- Original administrator credit adjustment continues to create spendable LLMRouter-only credit. It does not merely raise a budget and does not mint transferable, withdrawable or cross-product cash.
- Do not publish, deploy, rotate credentials, merge identities by email, delete old data, or run production migrations as part of local implementation.

## Identity and authorization

LLMRouter has one ordinary product user model and one product authorization path. Stable `(issuer, subject)` mappings associate central identities with the original local user ID. New central logins provision an ordinary product user. Existing mappings retain their local IDs, settings, groups, keys and history. An existing independent account may be linked only by trusted migration evidence or verification of both identities.

Original password, OAuth, MFA, Passkey, recovery and step-up flows remain supported. For mature systems, the product is a narrowly scoped trusted authenticator: after its complete original authentication succeeds, a server-only assertion for the bound local user resolves to the central subject and current authorization. Assertions are short-lived, product/instance scoped, replay controlled, and cannot accept an arbitrary browser-selected central subject or role. New products can use the existing headless central registration/password APIs and userkit.

UniHub is the single authority for managed identity status and product authorization. Original product management pages use delegated central operations scoped to that product and instance. Role projection replaces old permissions exactly, never unions stale local privileges. Product admins cannot acquire global account, cash adjustment or another instance's administration rights. Legacy administrator migration requires an explicit trusted import, never automatic elevation on login. Disabled central access or central failure never falls back to stale local grants.

A completed original product authentication can establish the normal central browser session through a browser-bound three-stage handoff. A second product uses a one-use PKCE code and scoped binding proof to adopt the same canonical subject. Proofs and codes retain the source authentication epoch and are invalidated when it changes. The existing Casdoor OIDC flow remains available with one bounded `prompt=none` attempt. No central session or `interaction_required` returns to the original form without a loop. State, nonce, PKCE and return URL remain server validated.

## One product business pipeline

All product users use the original dashboard, profile, keys, usage logs, models, settings and administrative pages. Remove feature gates and alternative product implementations chosen solely by `identity_source`. Identity provenance remains available for authentication and auditing only.

API keys retain the original full model, IP, group, cross-group retry, expiry, state and limit behavior. Each key has a fixed payer binding; changing a user's current team cannot silently change who an existing key charges. The real local user, key and group enter the existing relay; a shared routing principal cannot substitute for them. Existing asynchronous tasks and all supported protocols retain their existing product paths.

## Final ledger and funding adapter

LLMRouter calculates price once, including its model/group/cache/expression/native metering rules. The central runtime accepts this trusted server calculation and validates actor, payer, product scope, amounts, limits and idempotency. It does not price the same call again using an independent catalog.

The runtime unit for preserved legacy LLMRouter credits is `LR_QUOTA/0`, version `lr-quota-v1`, with identity conversion from the original quota integer. Ledger addresses include product, instance and payer. This is an asset in the existing central final ledger, not an additional local spendable balance. Conversion from general credits/cash must be explicitly authorized with a versioned exact rule; there is no implicit currency or scope conversion.

Balance is spendable money/credit; user budgets and key quotas are limits. Original add/set quota operations produce audited central adjustments of the corresponding spendable credit, with compare-and-set protection for absolute set operations. Product-specific credit adjustment permission does not permit global cash minting.

All calls pin their payer, asset, price snapshot/version, calculation digest and settlement backend. Reserve, top-up, settle and release are idempotent with conflict detection. Retry reuses a logical call and its reservation. Timeouts create an inspectable pending record, not a local-wallet fallback or automatic retry of the provider. Durable recovery retries financial commands only.

## Migration and cutover

Keep stable product user IDs, existing key secrets, groups, settings, history and counters. Import actual remaining balance once, keyed by product/instance/local user/batch and immutable snapshot digest. Never import used quota, request count or each key's remaining limit as money. Keep a frozen source snapshot and the central receipt.

Funding cutover is an explicit per-account state, separate from identity source. New calls cannot spend both old and central balances. Calls started before cutover settle through their pinned old backend; cutover must account for outstanding reservations. On uncertain central import, block spend until readback reconciles it; do not clear balances first or restore local spending blindly.

## Reusable source kit

The SDK exposes identity linkage/provisioning, trusted authentication, product authorization and financial runtime adapters. Mature products adapt their own domain services; new products use a reference user/session subsystem. Neither path creates a reduced second product UI or relay. Public contracts include scope, retry/idempotency, denied/unavailable distinctions and migration rules.

## Required verification

Use real application services and isolated databases. HTTP provider boundaries may be controlled; do not replace the behavior under test. Test ordinary and UniHub-linked users with identical roles/funds against original login, user administration, full key lifecycle/options, group/IP/model enforcement, original relay, metering, price calculation, logs, balance adjustment and settlement. Verify revocation, payer isolation, instance isolation, duplicate financial requests, unknown outcomes, restart recovery and one-time migration. Verify standalone mode separately.

Browser tests exercise the real original UI and compare same-role identities. Build and typecheck both projects and the source SDK, run affected/full appropriate tests, and independently review the diff. Local verification is not production deployment or real-supplier acceptance.


## Accepted usage and financial recovery

Credential revocation prevents new model admissions. It does not erase an already accepted supplier obligation. Supplemental costs bind to an immutable settled root call with the same product, instance, user, original key, payer, model, operation, asset, unit and pricing digest; no child may become a new parent. Accepted final-usage top-ups and supplemental costs require the separate `calls:reprice` service operation. They do not grant model dispatch authority. New admissions still obey current product access and budgets; final accepted costs are counted in future budget reads and need real available funds.

Insufficient funds leave the accepted obligation pending. Recovery reuses its original logical call and receipt. Only a proven `REJECTED/insufficient_funds` command with no posting under its original ledger reference may create another immutable attempt; UNKNOWN or SUBMITTING must first reconcile that original reference. Credits are never manufactured to clear a debt.

Existing-account financial import is offline-only and requires reviewed external writer/batch/cache reconciliation and backup evidence. A fresh CLI process or an in-process mutex cannot prove that another old gateway has drained. No production migration occurs as part of implementation.
