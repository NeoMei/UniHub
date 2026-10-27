# Product user foundation — final acceptance and review

Implementation complete on local feature branches:

| Repository | Base | Accepted implementation | Branch |
| --- | --- | --- | --- |
| UniHub | `a61586a00f50a4f5a0a447d826817a2bbb090447` | `775c63ced1d39b46aa0020e60e0e3938b1abe24c` | `codex/product-user-foundation` |
| LLMRouter | `5191107c08a6e391d10d19f7ec58267eb74629f4` | `b05f4b3087d6d24356924fb3b92bcfa61d9ce452` | `codex/unihub-product-userkit` |

Final verdict: **Spec PASS; Quality APPROVE**. The broad review and one scoped
repair review close all confirmed material findings. Local branches are preserved;
no merge, push, production deployment or publication occurred.

## Final exact-revision verification

- UniHub `go test -race -count=1 ./...`: PASS at the accepted revision, with
  real isolated local PostgreSQL/Formance and the LLMRouter integration path.
  Log: `/tmp/unihub-foundation-final-repaired-full-race.log`.
- LLMRouter `go test ./...`: PASS at the accepted revision. Some unchanged Go
  packages used the normal test cache; this is not an uncached full run.
  Log: `/tmp/llmrouter-foundation-final-repaired-full-go.log`.
- Repair coverage: protected CLI 9 Python tests (20 new table cases), actual
  CLI/config/real-PostgreSQL registration race tests, failed/recovered discovery,
  concurrent login/callback preservation and canceled-waiter route regressions,
  affected router/platformaccount race tests, and exact-source kit verification.
- Earlier accepted checks cover both frontends, SDK, shared kit, independent
  generated consumer, graceful executable lifecycle and the actual local Casdoor
  two-product browser matrix. The repair did not change shared-kit or frontend
  sources, so those checks were not repeated.

[Integration guide](../../integrations/product-user-foundation.md) and
[actual local acceptance](../../integrations/product-user-foundation-acceptance-2026-10-27.md)
record reproduction, cleanup and boundaries. The source starter has process-local
sessions and a single-process sample business store; production SQL/session/replica
configuration remains deployment work. Actual self-signup, production HTTPS,
external standalone IdP, supplier calls, payments and production deployment are
not claimed. One uniquely named empty test ledger remains as documented.

## Decisions recorded during implementation

- Work in place on dedicated feature branches, avoiding a new worktree and preserving current session docs/dependencies — task implementation is authorized and user prefers uninterrupted execution — cost if wrong: less filesystem isolation, mitigated by one implementation worker and explicit file ownership.

- Pin unpublished shared Go kit as a verified checked-in third_party module with relative replace until formal release — both repos must build independently without remote publication — cost if wrong: extra dependency snapshot to upgrade, mitigated by exact-source sync and provenance verification.

- Use canonical module github.com/NeoMei/UniHub/packages/userkit-go and server-side OIDC+BFF for LR — nested module releases need path correspondence and upstream token must stay backend-only — cost if wrong: additional BFF/session implementation, avoids browser credential duplication and package release failure.

- Explicit browser_sso client capability in onboarding tooling enables Casdoor shared sign-in session/auto sign-in and interactive sign-in without granting headless password_login — owning pinned Casdoor controllers/auth.go only retains identity session for configured signin session/consent, current helper forces both session flags false — cost if wrong: new optional registry flag and app readback, legacy client flags preserved. Task3 covers this functional SSO prerequisite.

## Scoped repair review

# Final repair wave — scoped re-review

Reviewed only the immutable UniHub `2d7e000..775c63c` and LLMRouter
`545cb9d8d..b05f4b308` repair packages against `final-fix-brief.md` and the two
original findings in `final-review.md`, with `final-fix-report.md` and supplied
test logs. This is the single scoped re-review, not another whole-branch review.
No implementation changes, helpers, test reruns or Git diff re-derivation.

**Spec verdict: PASS for the final repair wave.** Both original requirements are
addressed and the existing approved boundaries are preserved.

**Quality verdict: APPROVE.** No new material breakage was confirmed in the repair
diffs. Both original findings are closed.

## Original findings

### 1. Access-only CLI enrollment acquires a commercial dependency — ADDRESSED

`scripts/product_client.py` now computes selected scopes first and defaults the
billing mapping to `[]` when none is commercial. An explicitly supplied
`--billing-product` list is preserved, while commercial capabilities retain
`[product_id]`; omitted scopes retain the prior `accounts:read,catalog:read`
default. The commercial scope set agrees with the owning central startup
classification already reviewed. This repairs the actual documented CLI path
without changing central registration or unrelated client policy.

The new Go regression invokes the real documented Notes `prepare` command,
extracts its exact prepared client, loads it through `readConfig`, and calls the
owning registration function after real PostgreSQL migrations. Separate isolated
schemas assert no commercial row insertion and successful startup registration
with an existing disabled same-slug row that stays disabled. The Python CLI table
also covers identity/account/member scopes, all 16 commercial scopes, omitted
scope defaults, explicit repeated mappings and absence of a registry write.
Documentation now describes the actual scope-derived behavior rather than the
previous inaccurate metadata claim.

The report records behavioral RED failures for both unintended insertion and the
disabled-row startup error, followed by GREEN Python and real-DB race coverage.
This satisfies the requested integration regression rather than substituting a
manually emptied harness mapping.

### 2. First discovery outage permanently disables SSO retry — ADDRESSED

`router/unihub_sso.go` replaces `sync.Once`/permanent `initErr` with a
`singleflight.Group` and synchronized successful Browser cache. A failed attempt
leaves the cache empty, so the next login can retry. Concurrent callers share a
ten-second initialization attempt; a canceled waiter can return independently.
Initialization rechecks the cache inside the shared operation and stores only a
successful Browser. Later logins and callbacks retain that same Browser and
transaction store. The callback read remains short and does not hold a lock
across discovery. Existing login failure, cookie, safe-return and callback/token
verification paths are preserved.

The added registered-route tests make discovery fail then recover without
rebuilding the router; run concurrent initial logins and retain all five callback
transactions/native proof bindings; reject a replay without another exchange;
and allow a canceled waiter to return while a shared attempt is blocked, followed
by recovery after that attempt fails. The fixture verifies S256 and signed RS256
tokens through the existing shared Browser. The reported original retry failure
is RED before repair and GREEN afterward; affected router/platformaccount race
tests pass. The new deadline and successful-cache lifetime are documented.

## Evidence assessed

- Worker report: Python 9 tests passed (20 new CLI table cases); real PostgreSQL
  `cmd/unihub` and `internal/products` uncached race checks passed; all three new
  SSO route regressions and the affected router/platformaccount race checks
  passed; exact shared snapshot check remained pinned to `ca2f0a6`.
- Read supplied affected race log tails: router `30.392s`, platformaccount
  `1.407s`, and products `1.420s`, with no reported race failure.
- Read `/tmp/unihub-foundation-final-repaired-full-race.log`: all listed central
  packages pass. Parent identifies this as the exact final-revision full race
  run with real local PostgreSQL/Formance and the LLMRouter integration path.
- Read `/tmp/llmrouter-foundation-final-repaired-full-go.log`: successful full Go
  package output; the parent reports command completion with PASS. Cached package
  output is not described as an uncached full LLMRouter run.

These repairs change neither the shared module contract nor frontend business
flows. The new signed HTTP fixtures and registration tests are separate from the
previous actual Casdoor/browser acceptance. No additional real provider,
self-signup, production HTTPS, supplier, payment, deployment or publication
acceptance is inferred, and none was repeated for review.

## Nonblocking out-of-scope observations

None newly identified from these repair diffs. Existing process-local Browser
transactions/sticky routing and successful provider metadata caching remain
explicit documented limitations; they do not reopen either original finding.
