# LLMRouter then SuperWagie end-to-end acceptance plan

> **For agentic workers:** Use subagent-driven-development; keep independent implementation and review gates. Preserve the user-authorized order.

**Goal:** Complete the actual UniHub user path through LLMRouter first, then SuperWagie, including knowledge model calls and exact billing.

**Architecture:** Retain existing UniHub identity/account/ledger authority and LLMRouter execution ownership. Reuse the existing platform account, managed API key, task grant, operation contract, and knowledge broker implementations. Coordinate active gateway work before freezing any deployment.

**Tech Stack:** Go, PostgreSQL, Formance, Python acceptance drivers, SuperWagie JavaScript runtime, actual browser/desktop.

**Spec:** User request dated 2026-10-29; existing design docs/superpowers/specs/2026-10-27-llmrouter-unihub-owned-identity-billing-design.md with later native product-parity constraints taking precedence.

## Global Constraints

- Finish LLMRouter acceptance before SuperWagie production cutover.
- PAYMENT_MODE=sandbox remains intentional; no merchant activation or real-money recharge.
- Preserve existing LLMRouter users, roles, API keys, balances, history and native operations.
- Use dedicated acceptance users and small synthetic documents; never index personal libraries.
- Actual upstream model calls are authorized. Before paid knowledge indexing disclose estimate/time and obtain explicit confirmation.
- No login passwords, bearer tokens, API key plaintext, or supplier secrets in public artifacts.
- Do not edit other active worktrees or deploy concurrently. User authorized messaging the active integration task for coordination.
- A local test, HTTP 200, supplier fixture or runtime driver does not prove a packaged desktop/user-interface path.
- Never redispatch a request whose outcome is unknown. Reuse durable idempotency identifiers for settlement recovery only.
- Exact CNY/6 integer amounts and rational rate calculations; verify one charge per logical call and no native double billing.

### Task 1: LLMRouter live chat path and reproducible acceptance

**Files:** .local/integration-e2e-20261029/llmrouter_live.py and private state; resulting sanitized report under the same directory; final docs/integrations/llmrouter-superwagie-e2e-2026-10-29.md.
**Consumes:** scripts/headless_e2e.py Client; deployed public registration/login/recharges/api-keys; /v1/models, /v1/chat/completions and /platform/v1/calls/{id}.
**Produces:** acceptance user/account IDs, sandbox operation, key ID, actual call IDs, immutable rates/usage, exact recomputation and wallet delta; explicit UI checks still required.

- [ ] Build a resumable driver with mode-0600 private state and redacted public output.
- [ ] Register one new llmrouter-headless user and verify password PKCE login and product session projection.
- [ ] Recharge 10000000 CNY/6 units through sandbox APIs, replay same recharge ID and wait for APPLIED.
- [ ] Issue one scoped, bounded key for DeepSeek-V4-Flash-A; assert listing never includes plaintext.
- [ ] Make one tiny ordinary and one tiny streaming supplier call, using separate stable Idempotency-Key values; wait for SETTLED.
- [ ] Compare gateway receipt and central call; independently sum Fraction rates and ceil once, reconcile budget and wallet. Replaying successful call is rejected without new call.
- [ ] Revoke key and prove new invocation denied; retain access credentials only for follow-up controlled checks.
- [ ] Route central-identity keys, wallet and overview to existing central account components, retaining native-identity behavior and payer-isolated caches; generate chat examples only from known chat models.
- [ ] Independent review of driver/report; browser registration/login/recharge/key and UI log readback.

### Task 2: LLMRouter knowledge operation integration and safe deployment

**Files:** Coordinated gateway and UniHub operation/config changes in their owning branches; acceptance driver/report in this workspace.
**Consumes:** Task 1 baseline, peer integration task's reviewed frozen commits, current supplier protocol/rates.
**Produces:** deployed embedding/rerank operation bindings, current permission checks, precise receipts and ledger evidence.

- [ ] Obtain frozen commit IDs and review/test evidence from active integration task; inspect final diffs without overwriting unfinished edits.
- [ ] Confirm exact embedding/rerank capabilities, product allowlists, wire operation grants and published rates; reject ambiguous rates.
- [ ] Back up runtime/data, verify isolated restore, freeze package hashes; deploy services serially with rollback.
- [ ] Execute tiny embedding/rerank requests and assert result binding, one dispatch, SETTLED, exact wallet/ledger charge.
- [ ] Verify cross-user/result isolation, revoked credential rejection and existing native compatibility.
- [ ] Independent task review and public runtime readback.

### Task 3: SuperWagie account, chat and knowledge acceptance

**Files:** config/superwagie and docs/integrations/superwagie-production-onboarding.md; peer-owned SuperWagie knowledge/runtime integration; dedicated acceptance scripts and report.
**Consumes:** completed Tasks 1–2, reviewed knowledge broker wiring, stable knowledge backend request IDs.
**Produces:** actual SuperWagie register/login/account/recharge/model/chat/knowledge use plus reconciled bills.

- [ ] Register/readback exact loopback PKCE browser client, merge manifest/runtime service without changing llmrouter configuration.
- [ ] Verify headless and browser login, account choice, sandbox recharge and backend grant issuance from actual product.
- [ ] Verify model configuration in actual runtime and packaged UI, ordinary/streaming call plus settlement.
- [ ] Preview one 300-byte text fixture, disclose actual estimate and request cost confirmation; then index, retrieve and answer through actual knowledge backend.
- [ ] Reconcile every operation, call receipt, provider attempt, exact charge and Formance wallet balance; no residual holds or double charge.
- [ ] Final independent whole-change review and publish evidence with each acceptance item distinctly marked.
