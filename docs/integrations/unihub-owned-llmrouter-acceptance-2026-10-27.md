# UniHub-owned LLMRouter acceptance — 2026-10-27

Public LLMRouter identity and billing now belong to UniHub. Native users, wallets,
API tokens and subscriptions remain behind the separate loopback operator
listener. Public native login/token/top-up/relay requests return 403. The private
channel API still requires operator authentication (401 without credentials).
Signed callbacks remain available for existing orders.

## Deployment and rollback

- UniHub source `587d1bf`, release `/opt/unihub/releases/20261027-unihub-owned-587d1bf`.
- LLMRouter source `f6dbbf0a1`, image `sha256:42b427cab2748898561d1505101d06fbcfb7b932fbd3d1bb9d5fd152953635b3`.
- SuperWagie source `21ac9ce43`; prior `f9dd62ea8` supplied 22-model defaults.
- Remote evidence: `/opt/llmrouter/operations/unihub-owned-20261027/`.
- Cutover backup `backup-20261027T120118`; previous UniHub `/opt/unihub/releases/v0.4.6`, previous container `llmrouter-before-unihub-owned-120122`.
- Final adapter upgrade backup `upgrade-121619`; previous container `llmrouter-before-upgrade-121636`.
- Database/config backups preceded later mutations. Container hostname mapping for
  the private UniHub service is explicit and survives recreation.

## Actual acceptance

All 22 published chat models returned HTTP 200, settled in CNY/6 and rejected
logical-call replay with HTTP 409. Ordinary and streaming modes alternated across
the catalog. All 22 had exactly one stored provider attempt.

SuperWagie's actual account/model runtimes logged in using its headless client,
selected the account, obtained a product-bound grant, sent streaming
`DeepSeek-V4-Flash-A` through the managed local proxy/public gateway, received
`[DONE]` and reconciled a SETTLED receipt charging 14 CNY micros. A packaged desktop
click-through was not run.

Independent exact-rational recomputation matched all 23 call charges: **28498 CNY
micros (CNY 0.028498)**. After sandbox recharge of 100000000 CNY micros, the wallet
held **99971502**, exactly recharge minus consumption. The native routing
principal's quota, used quota and request count remained zero.

Both flat-price models settled at **73 CNY micros/request**. Other prices use the
versioned token-rate manifest: native operator price conversion uses USD2RMB 7.3
and ratio factor 14.6 CNY micros/token. This is not supplier invoice reconciliation.

An early MiniMax sandbox test retained an unconfirmed hold before its usage-alias
fix. A one-time operator waiver for that isolated acceptance account was backed up
under `sandbox-waiver-121959`; gateway and UniHub now report RELEASED. Supplier
cost is absorbed by the operator. No production-user call was replayed or waived.

## Checks and limits

UniHub full Go race tests passed with isolated PostgreSQL/Formance. Actual TCP
gateway tests covered CREDIT/CNY, lost settlement response, restart recovery,
duplicate rejection and unchanged native quota. LLMRouter focused/race tests,
frontend type/build and 23 UniHub frontend tests passed. SuperWagie model/account
suites and type-check passed. Independent reviews found no remaining confirmed
blocker. Zhipu streaming was specifically verified with final SETTLED receipts.

Both health endpoints and public static assets return 200. Rate consistency has
22 models, no missing or duplicate rates. Sandbox-only extra model/rate records
remain separate.

Supplier calls were real and charged upstream as authorized. UniHub recharge and
membership funding were **sandbox**; real merchant collection/activation was not
tested. OCR, embedding, rerank, speech, image and video are outside this chat grant.

## Implementation decisions and tradeoffs

Ruling: This migration retains a private native operator/compatibility surface rather than deleting native tables — because channel administration and controlled legacy recovery still require it — cost if wrong: native code remains larger, but deletion would make rollback and operations unsafe.

Ruling: The first public operation remains text Chat Completions/streaming/function tools — because existing UniHub billing contracts cover that surface — cost if wrong: non-chat skills need a later operation-specific contract.

Ruling: A private listener bound to exact loopback address supplies the native operator boundary; Origin/Referer/X-Forwarded-Host and product browser management secrets supply no authority — because headers are forgeable — cost if wrong: operators require SSH tunneling for native administration.

Ruling: Task 2 must include public legacy relay denial and UI navigation/auth redirects, not only the already-separated platform handler — because route-only billing changes leave native users and duplicated finances visible — cost if wrong: larger frontend regression surface; covered with focused UI tests/build.

Ruling: Task 3 includes a rate-unit audit and versioned rate publication using exact native pricing semantics rather than copying ModelRatio — because ratios and CNY micro-units differ — cost if wrong: changed prospective prices; existing call snapshots must remain immutable.

Ruling: Reuse current dedicated feature branch for LLMRouter; no unrelated files are staged, no shared-branch publication until acceptance. UniHub task implementation moves to a codex feature branch before edits — because current in-place task commits already exist and should not be reset — cost if wrong: less checkout isolation, mitigated with file ownership and serialized implementers.

Ruling: Add bounded per-request chat metering for the two native flat-price models — existing generic chat supports token units only but the approved 22-model service requires accurate request tariffs — cost if wrong: a small metering/adapter contract extension must be maintained; tests cover request count and mixed-unit rejection.

Ruling: Add product-specific model policy to UniHub issuance and read-only consistency projection — global published rates alone cannot enforce each product's model allowlist — cost if wrong: deployments must supply policy for configured llmrouter/SuperWagie, otherwise grants fail closed.

Ruling: Use existing versioned BillingProfile.Usage.Models for product allowlists instead of a new environment policy — reuses audited operator/deployment profile updates — cost if wrong: prospective profile changes also affect model authorization, with nil reserved for legacy behavior.

Ruling: Temporarily continue with executing-plans in controller after native model capacity failures on sol/astra/luna and routed-provider encrypted-task rejection — otherwise no implementer can proceed — cost if wrong: reduced context isolation, mitigated by retained RED tests and a later independent review retry before publish.

Ruling: Preserve SuperWagie membership/credit components while adding prepaid model funding as default against its llmrouter wallet — CNY tariffs only — cost if wrong: an explicit future CREDIT tariff policy is needed, never an invented exchange.

Ruling: Waive the single isolated pre-fix MiniMax sandbox hold using backed-up operator intent — guessing original usage is wrong — cost if wrong: platform bears one tiny test call; production users untouched.

GitHub publication: UniHub PR #7, LLMRouter PR #5, SuperWagie-Open PR #4. PRs remain open. LLMRouter pr-quality passed. SuperWagie regular CI queued; its separate review automation failed because repository GitHub App ID is absent, before code review ran.
