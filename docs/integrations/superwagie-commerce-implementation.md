# SuperWagie commerce implementation

Implemented on 2026-11-08 in the UniHub, SuperWagie and LLMRouter worktrees. Customer-facing Chinese terminology is **积分**. The source and controlled local validation are complete; production deployment, merchant configuration and live acceptance have not occurred.

## Delivered behavior

- Membership and add-on purchase uses the authoritative server catalog. WeChat Native displays a QR code; Alipay opens the signed official desktop checkout after explicit confirmation. These are one-time payments.
- Original checkout identities survive uncertain responses and normal restart. Payment, fulfillment and refund states remain separate. Repeated notifications do not duplicate orders or grants. External payments never debit old cash wallets.
- Upgrades use a separate exact-cent external quote, with the rounding rule recorded. Expired or changed quotes require new confirmation. Late paid receipts remain recorded for review/refund.
- Full unused/unreserved refunds use the original provider transaction and stable refund ID. Independent provider confirmation and ledger application are both required for success. Used/reserved grants, changed memberships and unsupported refund policies enter review.
- Explicit credit keys use CREDIT/6 with a configured versioned rational conversion. Reservation, top-up, settlement, release, expiry, budget and reports retain the selected asset and source batches. No cash fallback occurs. Existing cash keys/providers remain available.
- Credit setup validates runtime readiness, discovery and the issued key contract. Selection itself only reads state. Model bindings change through the existing explicit connection-test/application workflow.
- Proven insufficient-credit responses reach the membership/add-on guidance. Budget, cash and uncertain failures keep their own semantics. Opening guidance does not buy a package or repeat a model call.
- Server startup reads protected merchant files. Missing configuration disables the channel; invalid supplied configuration fails startup. Provider recovery has a separate timeout budget from ordinary financial recovery, and a failed prefix cannot indefinitely starve newer records.

## Verification

| Check | Result | Evidence under `.implementation/full-commerce-20261108/` |
| --- | --- | --- |
| UniHub `go test ./... -count=1` | Passed with isolated local PostgreSQL/Formance | `unihub-full-tests.log` |
| UniHub `go vet ./...` | Passed | `unihub-vet.log` |
| Payment, checkout and recovery race checks | Passed | `financial-race-tests.log` |
| Signed Alipay receipt → membership grant → native CREDIT settlement → add-on refund | Passed; real adapter and local ledger, controlled merchant responses | `composed-chain.log` |
| LLMRouter platformbilling/service/router/controller tests | 971 top-level passes, 13 pre-existing opt-in top-level skips | `llmrouter-affected-tests.jsonl` |
| LLMRouter affected `go vet` | Passed | `llmrouter-vet.log` |
| SuperWagie workspace type-check and lint | Passed | `superwagie-typecheck.log`, `superwagie-lint.log` |
| SuperWagie Web build | Passed; CSS optimization, dependency annotation and chunk-size warnings remain in output | `superwagie-build.log` |
| Commerce/key focused tests and actual Chrome fixtures | Passed; explicit actions, recovery, readiness, Chinese narrow/dark and wide/light layouts | `superwagie-report.md`, `sw-credit-keys-report.md` |
| OpenAPI and example JSON parsing | Passed | `document-schema-check.log` |
| Independent backend/frontend reviews | Actionable findings fixed and verified | `backend-review.md`, `frontend-review.md` |

The native credit and payment tests use controlled HTTP/identity/catalog fixtures. They do not prove a deployed three-process real-provider run. Skipped opt-in database/live cases are not counted as acceptance. Native desktop packaging and Windows installation were not exercised.

## Configuration and deployment inputs

Use [direct merchant configuration](superwagie-direct-payments.md), the [file-reference example](../../examples/payments/direct-merchants.example.json), and the [native CREDIT contract](native-credit-metering.md). The executable requires `PAYMENT_MODE=direct` with `PAYMENT_PROVIDERS_FILE`; API and worker use the same merchant identity. Private key/APIv3 files require owner-only permissions. Alipay currently supports public-key mode. Merchant and application identity changes require finishing or retaining the original configuration for outstanding orders.

An approved production conversion version/ratio and compatible deployed services are required before `native_credits.ready` is true. The ¥39/¥99 memberships and ¥9.8/¥98 add-ons remain [unpublished drafts](superwagie-points-draft.md). Their existing manual-refund policy is retained; automated unused refunds require an explicitly configured supported refund rule. No tariff, live product, old balance or production credential was changed.

Database migrations 026–028 add external checkout/receipt storage, the native CREDIT key contract, and fair recovery ordering. Deploy the compatible billing/runtime implementations together, configure protected merchant callbacks and the approved native conversion, then perform separately authorized live payment, model usage and refund acceptance before opening sales.

SuperWagie's existing local commerce writer lock can remain after an ungraceful exit. Its documented recovery requires stopping all writers, preserving `state.json`, and removing only the stale lock. Automatic stale-lock recovery is not part of this implementation. Server receipts/ledger commands have independent recovery.

## Worktrees

- UniHub: `/Volumes/codex/new-worktrees/unihub-subscription-checkout-20261108/unihub`
- SuperWagie: `/Volumes/codex/new-worktrees/superwagie-subscription-pricing-20261108/SuperWagie-open`
- LLMRouter: `/Volumes/codex/new-worktrees/llmrouter-credit-checkout-20261108/llmrouter`

No commit, merge, deployment or release was performed. Combined verification metadata is in `.implementation/full-commerce-20261108/verification.json`.
