# Center catalog integration: fix round 1

- Review finding: `URL.Query()` silently discarded malformed query pairs, accepting a valid scoped pair with malformed duplicate `account_id` or extra parameter. A malformed lone reserved parameter could fall through to the legacy projection.
- Base: `08c13e1fdb3136c8ce2579524dc617f3d63d35fe`.
- Implementation head: `ee7bb257070712f64c8ed8f4be94cded925077bf`; this report is committed separately.
- Implementation diff SHA-256: `9a928eb45f731b8942a7798de883ddd8fa561860dd4e506152383e97b867f077` (`git diff --binary 08c13e1fdb3136c8ce2579524dc617f3d63d35fe ee7bb257070712f64c8ed8f4be94cded925077bf | shasum -a 256`).

The route now calls `url.ParseQuery(r.URL.RawQuery)` and rejects parse errors with `400 invalid_request` before selecting scoped or legacy dispatch. Valid no-query and ordinary valid unscoped requests retain their prior dispatch; malformed percent encoding and unescaped semicolons are rejected, including cases where the malformed reserved parameter would otherwise disappear. The existing all-or-none/exactly-once scoped validation is unchanged.

Added a no-database action-level regression covering malformed duplicate/extra pairs, semicolon forms, malformed lone reserved parameters and ordinary single-parameter requests. Extended the existing authenticated HTTP database fixture with the same focused negative cases and no-rate-disclosure checks. No other endpoint, billing semantics, video guard, release/version or deployment change.

Verification used `GOPROXY=off GOTOOLCHAIN=local`:

- Before the production fix, `go test -count=1 ./internal/httpapi -run '^TestModelConsistencyRejectsMalformedQueryBeforeServices$'` failed for six malformed encoding/semicolon cases because they reached services instead of rejecting the query. The two ordinary incomplete-pair cases already rejected correctly.
- After the fix, the same focused unit command passed all eight cases.
- `go test -count=1 -run '^$' ./internal/httpapi` passed the compile check; zero fixture tests executed.
- `git diff --check` and staged diff check passed.

No full unconfigured suite was rerun, no database/ledger was accessed or provisioned, and no network/supplier request was made. The authenticated HTTP fixture awaits the coordinator's isolated PostgreSQL run against the implementation head; earlier remote tests against `08c13e1` do not verify this fix.
