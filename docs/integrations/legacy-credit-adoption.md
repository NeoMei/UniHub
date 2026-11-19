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

## Approved SuperWagie production cutover

Build `./cmd/unihub-credit-adoption-production` from the reviewed UniHub release source. The independent fixture command retains its local schema and ledger restriction. Run the production command on the UniHub host under the service user. Bind `APP_ENV`, `UNIHUB_ADOPTION_DATABASE_URL`, `UNIHUB_ADOPTION_LEDGER_URL`, and `UNIHUB_ADOPTION_LEDGER_NAME` to the running service configuration without printing the values.

The production command is scoped to SuperWagie. It requires the active owner or billing administrator, the exact payer and batch IDs. Capture and review a private, read-only preview before enabling the product broker. A deployment can stage the code and migration tables before the scoped product profile is active. That preview reports the missing profile as a blocker and still checks the original payments, grants and lot balances. Stage the scoped profile while product offers and broker authorization remain disabled, then capture a fresh preview for cutover.

```sh
umask 077
unihub-credit-adoption-production --mode preview --actor ACTOR --account PAYER \
  --batch LOT_1 --batch LOT_2 --batch LOT_3 > production-plan.json
sha256sum production-plan.json
```

Review the same file against the authorized payer, lot amounts and original deadlines. Compare it with the gateway's outstanding call evidence. An unresolved customer-side hold or unsettled payment blocks adoption. An unresolved supplier outcome with a confirmed terminal central call has no customer-side hold. Preserve that gateway record.

Use the reviewed file and its exact SHA-256 for apply. The operation validates the file digest and the canonical plan digest. Under the account and product locks, it reads the current plan again. New calls, orders or wallet activity invalidate the prior plan even when balances happen to match. Migration 035 and a matching current plan are required, and successful apply preserves the immutable receipt.

```sh
unihub-credit-adoption-production --mode apply --actor ACTOR --account PAYER \
  --batch LOT_1 --batch LOT_2 --batch LOT_3 \
  --approved-plan production-plan.json --approved-plan-sha256 APPROVED_SHA256 > applied.json
unihub-credit-adoption-production --mode verify --actor ACTOR --account PAYER \
  --batch LOT_1 --batch LOT_2 --batch LOT_3 \
  --approved-plan production-plan.json --approved-plan-sha256 APPROVED_SHA256 > verified.json
```

If apply returns an uncertain result, verify with the same file and digest before retrying. A repeat apply with that plan returns the original receipt and current lot balances. During a deployment rollback, keep the receipt. Restore live scope only through a separately audited reversal that proves no reservation, consumption, expiry or withdrawal occurred after adoption. Keep the product broker closed while checking the affected lots.
