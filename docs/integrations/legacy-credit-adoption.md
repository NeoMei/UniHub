# Legacy credit adoption on a local fixture

The UniHub adoption command inventories a single payer and an originating product. It uses PostgreSQL to bind each lot to its original order, recognizes the original UTC lot dates, and checks payment and grant postings against the recorded Formance transactions. The report lists real cash in the unified and original product wallets as `CNY/6`.

To query a fixture, set `UNIHUB_ADOPTION_DATABASE_URL`, `UNIHUB_ADOPTION_LEDGER_URL`, and `UNIHUB_ADOPTION_LEDGER_NAME` to existing local endpoints. Apply the repository's normal schema migrations to the disposable fixture beforehand. Use a finance owner or billing administrator for that payer.

```sh
go run ./cmd/unihub-credit-adoption --actor ACTOR --account ACCOUNT --product PRODUCT
go run ./cmd/unihub-credit-adoption --actor ACTOR --account ACCOUNT --product PRODUCT --batch BATCH --dry-run
```

The first command inventories the payer's lots. An applicable plan requires explicit lot IDs, `can_apply: true`, and an exact digest. Review the per-lot classification and the account-level blockers before applying. Apply requires a local PostgreSQL schema named `legacy_credit_fixture_*` and an existing local Formance ledger named `legacy-credit-fixture-*`. The DSN must explicitly set that schema as its search path.

```sh
go run ./cmd/unihub-credit-adoption --actor ACTOR --account ACCOUNT --product PRODUCT --batch BATCH --apply-digest DIGEST
go run ./cmd/unihub-credit-adoption --actor ACTOR --account ACCOUNT --product PRODUCT --batch BATCH --verify-digest DIGEST
```

An applied receipt reconciles all five credit buckets and both cash balances before and after. Adoption changes the lot's scope in place and preserves the original ledger address, source, grant quantity, and expiry. A legacy paid order keeps its original cash recognition and nominal credit amount. The current tariff determines the models those credits can buy. Current readback may show later consumption or expiry and separately preserves the original adoption receipt. Repeating the identical apply returns that receipt.

| Finding | Disposition |
| --- | --- |
| Unified or original product cash | Retain as cash at the original destination. Any separately authorized transfer uses the cash workflow. |
| Product-scoped, expired, or depleted credit lot | Retain its existing disposition. |
| Paid, unreserved and reconciled legacy credit lot | Candidate for explicit product adoption without changing its original expiry. |
| Other product, other payer, unknown ownership or missing price evidence | Resolve through authoritative records before selecting a lot. |
| Legacy checkout, refund, credit hold or settlement still open | Reconcile through its original command or merchant receipt before retrying preflight. |
| Active legacy term with future paid credit installments | Keep its original subscription and grant schedule until the remaining installments are issued. |
| Enabled legacy renewal mandate | Review it under the legacy contract. To move to a new scoped term, turn off the old mandate and purchase the new scoped membership once the old term has ended. Grant a new scoped renewal authorization explicitly. |
| Gateway balances without matched UniHub order and ledger evidence | Reconcile separately before proposing any import. |
