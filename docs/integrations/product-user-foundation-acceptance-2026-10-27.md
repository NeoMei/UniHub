# Product user foundation local acceptance — 2026-10-27

Result: real local Casdoor browser SSO, isolated central PostgreSQL, LLMRouter
SQLite and an independently generated Notes product passed the scoped matrix.
This is local identity/access acceptance; no production release or publication,
real supplier/model call, charge, message or payment was performed.

## Actual provider and service evidence

The issuer was the running local Casdoor `http://localhost:58000`; central business
origin was `http://127.0.0.1:58118`, router `:58119`, generated Notes `:58120`, and
router's private setup socket `127.0.0.1:58121`. Browser login used fresh isolated
Playwright Chrome contexts, never a user's profile. The test administrator and
member were new ordinary users created by controlled local management setup, with
empty email/phone and a unique cleanup marker. This is not browser self-signup
acceptance. No cookie injection, signed provider fixture, mocked HTTP response,
HAR, trace, storage-state export or password screenshot was used.

Three fresh private applications had exact callbacks/audiences, no external
providers, and explicit `browser_sso=true`; the existing organization and all
existing applications/users remained unchanged. The webhook table was verified
empty before mutations and again after cleanup. Notes had only `access:read`,
proving kit capabilities/access without billing/accounts privileges.

| Actual browser / HTTP assertion | Observed |
| --- | --- |
| Administrator central login through Casdoor, `/v1/me` UUID, restart only isolated central with that UUID | `/v1/user-management` enabled/can_manage true |
| Member real router OIDC callback and kit live proof | default `member`, stable central UUID |
| Notes login in same member context | same central UUID, Casdoor SSO, no password re-entry |
| Notes JIT defaults, repeated access | same local identity, personal notebook, initialization count 1 |
| Notes resource write | own article 201; another local owner 403 |
| Ordinary router channel access | direct `/api/channel/` 403; rendered frontend deny |
| Central UI viewer save | PUT 200; assignment revision advanced and roles read back |
| Existing router session as viewer | channel GET 200; PUT 403; actual `/channels` UI rendered |
| Central UI operator save | current permission includes channel:write; actual safe name-only PUT 200 |
| Router role changed while Notes active | Notes remained member; same local identity/resources |
| Direct `/channels` navigation and reload | rendered channel card/table after server boundary fix |
| Frontend session restore/BFF | reload restored identity; `/api/unihub/bff/v1/accounts` 200 |
| Central channel-role revoke | next request using prior native session bearer returned 403 |
| Central global disable | prior Notes session 403 and prior router session 401/403; reenable read back |
| Notes and router UI logout | Notes current-user 401; router refresh 401 |
| Browser hygiene | no credential/token storage keys, script exceptions, local 5xx or native-security API leaks |

Direct authorization checks reused a native dashboard token from the actual
session refresh, kept in memory; no upstream identity bearer was returned to the
browser or persisted by the harness. Current central grants were never written
into elevated native roles. Final SQLite readback showed exactly one product
identity at common role `1`, `quota=0`, `used_quota=0`, `request_count=0`. The only
channel fixture stayed disabled, with loopback `127.0.0.1:1` as its unreachable
non-supplier URL; only its name changed. The unique ledger had **0 accounts and
0 transactions**, confirmed through actual Formance stats and transaction reads.

## Integration repairs validated by this run

- Protected onboarding now supports access:read, instance_id and explicit
  browser_sso. Legacy flags remain false/default; browser sign-in does not imply
  headless password login or registration.
- Production web middleware had rejected `/channels` and `/dashboard` HTML deep
  links with native403 despite correct frontend guards. Exact product GET/HEAD
  shell routes are now permitted; management/finance/relay APIs and unknown paths
  remain subject to their existing gates.
- Channel drawers eagerly queried native 2FA/passkey methods for a product
  identity, causing a visible forbidden toast. Product identities now skip those
  native credential-method requests; native identity behavior is unchanged.
- Existing service cleanup tests lacked the new UniHub binding table in their
  SQLite fixture. Only the fixture migration was corrected.

## Automated evidence categories

- Python protected helper: 8 tests passed; scaffold tests: 2 passed, including
  independent generated `go test ./...`, no overwrite, no absolute source path,
  explicit config and public empty-secret example.
- Notes example: `go test -race -count=1 ./...` passed transactional default,
  concurrency, durable identity, rollback and central/resource policy checks.
- Shared Go kit: uncached race suite passed, including signed RS256 test-provider
  tests. Those fixture tests are separate from the actual Casdoor assertions above.
- Central affected packages (`products`, `productusers`, `httpapi`, `cmd/unihub`)
  passed uncached race tests with actual PostgreSQL; no missing-DB skips.
- LLMRouter full `go test ./...` passed after fixture/web-boundary fixes; affected
  middleware/router/service race checks passed. Shared snapshot exact-source check
  stayed pinned to `ca2f0a6bcacbce74e41ef7a623c685d43c3872a1`.
- LLMRouter affected frontend: 33 tests, typecheck, changed-file lint and production
  build passed. Five configured mocked browser regressions passed; these are
  distinct from the real local provider matrix.
- Parent's prior full central race/PostgreSQL/Formance/synthetic cross-repo gateway
  suite was already green. Task3 did not claim those synthetic supplier fixtures
  as real external provider/payment acceptance.

## Screenshots

Final sanitized images are outside the repository, under
`/Users/neomei/.codex/visualizations/2026/10/27/01a17bc1-4abc-7c40-a64c-bb37a3a3bc70/`:

- `task3-notes-sso.png`: current Notes user/default notebook after actual SSO.
- `task3-central-role-saved.png`: central viewer grant and assignment revision.
- `task3-channel-viewer.png`: rendered plural `/channels` page, no native error toast.
- `task3-channel-operator.png`: rendered channel after actual permitted update/reload.
- `task3-channel-default-denied.png`, `task3-channel-revoked.png`: frontend denial.

Earlier captures using singular `/channel` were replaced and are not acceptance
of a rendered channel screen. Final viewer/operator capture explicitly required
the actual channel row and absence of the native-credential error toast.

## Cleanup and remaining state

The first private preflight directory `unihub-product-userkit-puacc7e785cd3` stopped
before any remote mutation because webhook API access was refused; it had no
resource allowlist, app/user/schema or ledger creation. The successful run was
`unihub-product-userkit-puacce75df9d4`, schema `puacc_puacce75df9d4`.

Exact owned process handles were stopped before deletion. Readback verified
ownership/name/client ID/description and user ID/marker before removing the three
new apps `app-puacce75df9d4-console`, `app-puacce75df9d4-router`,
`app-puacce75df9d4-notes` and users `puacce75df9d4admin`, `puacce75df9d4member`.
Application delete requires `organization: unihub` in its body under the pinned
Casdoor organization authorization filter; an initial refused delete was read
back before correcting that exact request. No uncertain delete was blindly
retried. Exact IdP absence and schema absence were checked again independently
for both run prefixes; webhook count remained zero.

The unique empty ledger **`puacc_puacce75df9d4` remains**: no supported safe
exact-ledger deletion was established. Existing ledgers and all shared containers
were preserved. Private run configurations/databases/logs and the empty first run
directory were removed after sanitized evidence extraction. No credentials are
included in this record.

## Limits

The starter is an intentionally small single-process durable business store, with
in-memory sessions/proofs; a production SQL provisioner, durable encrypted session
store and replica coordination remain deployment work. Standalone mode supplies
an explicit product-owned standard OIDC seam; its real external provider and
online revocation behavior were not exercised. Browser self-signup, production
HTTPS/cookie acceptance, supplier calls, payments and deployment were not tested.

### Lifecycle review correction

The starter now returns initialization/listen failures through deferred store
cleanup and handles SIGINT/SIGTERM with bounded HTTP shutdown before releasing its
database lock. Executable regressions build the actual starter, stop it with each
signal and restart against the same database without lock repair; a failed local
OIDC discovery followed by corrected startup also succeeds. These tests use an
HTTP discovery fixture only, with no login claims or real IdP mutation. The
original actual Casdoor business/access matrix above was not repeated for this
lifecycle-only change. Abrupt crashes/SIGKILL still require conservative owner
verification before manual lock recovery; the one-process store limit remains.
