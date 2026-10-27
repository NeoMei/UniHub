# Cross-repository product acceptance

The opt-in test `internal/httpapi/product_cross_repository_test.go` runs the real UniHub HTTP handlers, an isolated PostgreSQL schema and a unique Formance ledger. It launches the sibling LLMRouter testcase `router/product_cross_repository_test.go` in a separate process with the race detector. LLMRouter runs two real TLS HTTP servers, each with its own SQLite database and registered product instance.

Run from the UniHub checkout after configuring disposable test services:

```sh
UNIHUB_TEST_DATABASE_URL='postgres:///postgres?host=/tmp' \
UNIHUB_TEST_LEDGER_URL='http://127.0.0.1:53068' \
UNIHUB_CROSS_PRODUCT_LR_DIR='/absolute/path/to/llmrouter' \
go test -race -count=1 -timeout=120s \
  -run '^TestCrossRepositoryProductBrowserAndOriginalBilling$' -v ./internal/httpapi
```

An ordinary UniHub run without `UNIHUB_CROSS_PRODUCT_LR_DIR` skips this gate. An ordinary LLMRouter run skips its companion testcase unless the parent supplies the ephemeral authority URL. Skips do not establish cross-repository acceptance.

## Assertions

The source instance uses the original registration and password-login handlers. Registration applies the original 500-quota signup policy and retains the original product user ID. Its original refresh session performs the three-stage handoff and creates the central browser session.

The second instance receives a real authorization code, exchanges it with S256 PKCE, binds the same canonical central subject, initializes an ordinary product user, applies the same signup policy and creates the configured original default API key once. The original refresh API returns the centrally spendable 500 quota even though the local mutable wallet is zero.

The user creates another key through the original key API. Its finite limit, model restrictions and group remain product fields. The original `PreConsumeBilling` / `BillingSettler` pipeline reserves 60 quota and settles 37. Repeating settlement does not make another debit. The key has 163 remaining and 37 used; the original user counters show 37 used and one request. The original self API returns 463 available quota.

Re-entering the second instance reuses the existing product user and keys. It does not award signup funds again. The test then disables and soft-deletes the explicitly created key. New calls remain rejected both before and after a final-cost adjustment. The already accepted original call is repriced from 37 to 47 quota through `RepriceProductCall`; repeating the same adjustment has no additional effect. The original key remains disabled/deleted with 153 remaining and 47 used. The user still has one request, with 47 used and no spendable local wallet.

UniHub independently reads one canonical user, two instance bindings and exactly two settled financial calls: the original 37-quota charge and its 10-quota accepted-cost child. A SQL join verifies the child retains the original payer, local user, key, model, operation and pricing digest. Direct final-ledger readback shows 500 remaining in the source instance and 453 in the second instance. The intermediate original self API assertion remains 463 before the later verified adjustment.

## Scope

This is an HTTP protocol and SQL/ledger acceptance test. It sends actual HTTP requests to UniHub and actual TLS HTTP requests to both LLMRouter routers. The browser driver follows redirects and carries the relevant cookies explicitly. It does not render Chromium pages or prove browser-enforced cross-origin/SameSite behavior; frontend browser tests and the broker's rejection tests cover separate evidence.

The financial adapter currently supports LLMRouter's `LR_QUOTA/0` asset and `lr-quota-v1` unit. This financial test therefore uses `product_id=llmrouter` with instances `default` and `second`. Distinct-product identity SSO is covered separately by `TestNativeBrowserHandoffCreatesCentralSessionAndSecondProductBinding`. Other new products can reuse the identity/authorization SDK; their money integration needs an explicit asset and settlement adapter. This test does not grant them LLMRouter quota or convert promotional credit into general cash.

The settlement amount and later final-cost observation are supplied as test usage to the original billing and repricing interfaces. No external model supplier, real payment, provider reconciliation, production account, production migration or deployment is exercised. OIDC discovery is a local fixture because this chain uses the trusted original product authenticator.
