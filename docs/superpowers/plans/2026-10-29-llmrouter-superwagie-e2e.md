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

- [x] Build a resumable driver with mode-0600 private state and redacted public output.
- [x] Register one new browser user and verify llmrouter-browser password PKCE login and product session projection. Registration201, exact audience/access:read, code replay400; durable state preserves this user.
- [x] Recharge 10000000 CNY/6 units through sandbox APIs, replay same recharge ID and wait for APPLIED.
- [x] Issue one scoped, bounded key for DeepSeek-V4-Flash-A; assert listing never includes plaintext.
- [x] Make one tiny ordinary and one tiny streaming supplier call, using separate stable Idempotency-Key values; wait for SETTLED.
- [x] Compare gateway receipt and central call; independently sum Fraction rates and ceil once, reconcile budget and wallet. Replaying successful call is rejected without new call.
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


### Task 1b: Close broker financial authorization using existing actor authority

**Requirements:** The original complete product path includes current preferred product-broker and native registration flows, even when they do not have an OIDC identity. The implementation retains the existing central user/account/ledger architecture; no identity merge, balance move, or native-role rewrite is authorized. Read-only architecture evidence: `.superpowers/sdd/2026-10-29-llmrouter-superwagie-e2e/broker-finance-bridge-design.md`, Plan B. This is necessary work within the already authorized full-path objective, with no new user-facing commercial policy choice.

**Files:** New focused `internal/httpapi/product_finance.go` and tests; minimal shared-action authorization factoring in `server.go`; `RuntimeService` configuration/parser tests; backend-only `packages/userkit-go` finance transport/tests/docs. Peer owns LLMRouter BFF/UI/cache adaptation.

**Interface fixed for coordinated implementation:** private `/internal/v1/product-finance` prefix maps a finite set of existing `/v1` business route suffixes. Exactly one `Authorization: Bearer <runtime-service-token>` and one `X-UniHub-Actor-Authorization: Bearer <upa_actor>`; no Origin. Only current `upa_` assertions are accepted. Optional `finance_client_id` on runtime service selects a single registered product client of the exact service product/instance; newly explicit `finance:read` and `finance:write` operations gate the corresponding method. Missing or ambiguous policy mapping rejects at startup when finance is enabled. Existing services without finance keep their behavior.

**Finite initial routes:** GET `/me`, `/accounts`, `/capabilities`, `/catalog`, `/products`, `/products/{product}/billing-profile`, `/products/{product}/model-consistency`; GET `/accounts/{account}/resources`, `/wallets`, `/usage-budget`, `/api-keys`, `/calls`, `/calls/{call}`, `/usage/summary`, `/operations`, `/operations/{operation}`, `/recharges/{order}` (there is no existing GET recharge collection route); POST `/accounts/{account}/recharges`, `/recharges/{order}/sandbox-settle`, `/api-keys`; DELETE `/accounts/{account}/api-keys/{key}`; PUT `/accounts/{account}/usage-budget`. Transfers, model credentials, subscription/orders read routes may be added only if required by the existing panels/knowledge task and subject to the same product client scopes/account roles. No admin/ops, team membership writes, arbitrary URL forwarding, runtime-quota changes, model-pool extra authority or reusable bearer exchange.

- [x] Implement the smallest shared-handler wrapper, verify fresh actor/auth-version/global/product activity and product:use, derive actor solely from the assertion, apply the exact registered product scopes and billing-product restrictions, preserve route patterns/path variables and transactional account checks.
- [x] Implement a bounded server-only userkit transport with fixed target origin and finite operation/method/path mapping; preserve idempotency, strict headers/body/query bounds, no redirects or transport retry of writes. Keep raw credentials out of logs/browser/DTOs.
- [x] Add actual isolated HTTP tests for missing/duplicate/wrong credentials, Origin, wrong product/instance/client/scopes, expired/revoked actors, active-state changes, unrelated account/key/order/call, exact same financial-role enforcement, sandbox credit replay and unchanged original OIDC routes.
- [x] Independently review source and tests before publishing frozen contract to peer; root runs real PostgreSQL/Formance tests and preserves all native fund/key/role snapshots.
- [ ] Peer integrates BFF with fresh actor after current local session/lifecycle validation, server-reported finance readiness, SID/session-version/login-method authority generation, stale response abort and key/CSRF clearing. Explicit sandbox-settle must match current sandbox config. Root verifies actual browser flow after deployment.



### Task 1c: Bind broker finance to a live central source session

**Goal:** Preferred product_broker sessions can access the same central finance UI while central-derived logout/revocation stops that access. Independent native sessions retain their existing realm, as explicitly required by the approved administration design lines 112–116.

**Requirements/design evidence:** `.superpowers/sdd/2026-10-29-llmrouter-superwagie-e2e/broker-source-proof-spike.md`. This is the bounded missing source proof in the existing authorized broker integration; no new identity linking or commercial choice. Do not disable the source gate merely to make UI succeed.

**Center contract:** `ProductBrowserGrant` adds backend-only `source_proof_token` and `source_proof_expires_at`. Token is `ubsp_` plus 64 random lowercase hex chars (256 bits), persisted only as SHA256. It binds original central `browser_sessions.id_hash`, central user UUID, exact registered destination client/product/instance and expiry no later than that source session. Initial one-use `ubp_` binding proof remains distinct. Old pending codes without a source hash never gain financial source authority.

**Private verifier:** `POST /internal/v1/product-runtime/identity/browser/source/verify`, single existing runtime service bearer, existing explicit `identity:browser` operation, no Origin, strict bounded body `{source_proof_token,central_user_id}`. Fixed expected client derives from service `finance_client_id`, validated against current registered browser client/product/instance; body user ID is comparison-only against the stored central UUID. Success 204, generic denial otherwise. No source proof in URL, browser, logs, or general bearer exchange. SDK method `(*RuntimeClient).VerifyProductBrowserSource(ctx, proof, centralUserID) error` uses that fixed endpoint, no retries/redirects. Existing service configuration need not gain a new operation solely for source checks.

**Validation:** Issuance, exchange and every verifier re-read source-session existence/expiry, linked same central UUID, global active status, `created_at >= session_not_before`, no pending identity_revocations for source issuer/subject, and existing native-origin source binding/current auth-version/access rules. Destination product:use is also current. Central logout deletes session; FK cascade or fresh join invalidates proof. DB errors fail closed. Existing OIDC bearer and independent native authentication are unchanged. This per-request check does not promise cancellation of writes accepted before a concurrent revocation.

**Files:** New next unused SQL migration (confirm numeric/name order; never renumber existing), focused productauth browser-source file/tests, narrow browser_broker/browser_http integration, exact private route in product_auth, backend-only userkit types/method/tests. Peer owns separate LR AES-GCM sealed source-proof row bound to source SID/local user/central UUID/product/instance/client/expiry and the current-SID→verify-source→mint-fresh-actor order. Keep broker financial denial until this works; missing proof requires explicit re-login.

- [x] Add explicit schema/code propagation and source-proof issuance; preserve legacy native handoff/OIDC paths and one-use bind semantics.
- [x] Add exact dual-bound service/source verifier and userkit method; test missing/duplicate/Origin/cross-client/product/instance/user/expired/malformed proof and DB failures.
- [x] Prove positive source verification then central session removal/cutoff/pending-provider-revoke/native-source lifecycle/global/product disable causes denial, including logout between authorization and exchange; preserve independent native cases.
- [ ] Independently review source and real isolated PostgreSQL migration/HTTP tests, then freeze contract to peer. Root owns production release and actual dedicated browser source-session revocation test.

### Task 2a: Preserve the verified result-reader token expiry

**Requirements:** Existing knowledge result authorization already validates the original product access token, identity and current task/account/product permissions. The gateway also needs that verified token expiry to reject expiry during result decryption/settlement checks, immediately before delivery. Return optional `actor_expires_at` Unix seconds only on a successful `purpose=result_read` response from `/internal/v1/model-credentials/verify`, taken from verified `auth.Claims.ExpiresAt`. Keep model credential `expires_at` unchanged and omit the actor field for ordinary verify/receipt/denied responses. No new realm or authority is introduced.

- [x] Extend the endpoint response and existing signed-access-token HTTP/DB acceptance to distinguish actor expiry from model credential expiry and preserve all current denials.
- [x] Observe regression failure, run real isolated PostgreSQL race tests, independently review, freeze the peer contract and publish the server increment.
- [ ] Peer rechecks both verified actor and credential expiry immediately before result delivery; knowledge production gate remains closed until the paired deployment and actual acceptance.
