# UniHub Account and Wallet Foundation Implementation Plan

> **For agentic workers:** Use test-driven development and independent review to implement the tasks below. This is the first independently testable slice of phase one, not the whole product rollout.

**Goal:** Build a runnable Go API with verified platform identities, personal/team billing accounts, product registration, sandbox recharge orders, unified-to-product transfers, and recoverable Formance postings.

**Architecture:** PostgreSQL owns business metadata and durable commands. Formance Ledger owns cash balances. Casdoor OIDC access tokens identify users; local permissions determine access to each account. Sandbox payment processing exercises the verified-notification path without real money.

**Tech Stack:** Go, PostgreSQL, Formance Ledger v2.4.16; coreos/go-oidc/v3 for OIDC JWT verification and pgx/v5 for PostgreSQL.

**Spec:** ../specs/2026-10-24-unihub-design.md, sections 1–3, 5–6, 9 (slice 1).

## Global Constraints

- Display name UniHub; engineering slug unihub.
- User identity is `(issuer, subject)`, never email as a merge key.
- Personal/team funds cannot cross account boundaries; a transfer moves unified-wallet cash into one product wallet owned by the same billing account.
- Cash asset CNY/6, decimal integers serialized as strings; external recharge amounts must be whole cents.
- Formance is the only authoritative cash balance store; PostgreSQL must not maintain independently writable balances.
- All mutations require current authorization; only owner/billing_admin may manage funds. Member-management permission does not imply finance permission.
- Business idempotency keys bind immutable input; unknown external results are queried/replayed with the same reference and key.
- No real merchant configuration, production deployment, third-party connections, MCP/API gateway, subscriptions or model consumption in this slice. The latter business capabilities remain in the next slice, not dropped from phase one.
- No credentials, dependency caches or local database files in version control.

## Interfaces and ownership

`internal/accounts` owns identity/account/member/invitation persistence. `internal/ledger` owns the Formance HTTP adapter. `internal/auth` owns strict Casdoor access-token verification. `internal/wallet` owns recharge/transfer commands and recovery. `internal/httpapi` exposes the application; `cmd/unihub` configures API and worker modes.

Database initialization is versioned SQL under `internal/database/migrations`; `internal/database.Migrate(ctx, pool)` applies it. Integration tests use an explicit dedicated database and isolated schema; tests never truncate a developer's default database.

## Task 1: Identity, accounts, and membership

Files: `internal/accounts/{store,identity,membership}.go`, `internal/accounts/store_test.go`, `internal/database/{migrate.go,migrations/001_accounts.sql}`.

Interfaces: `accounts.New(pool) *Store`; `EnsureIdentity(ctx, Identity{Issuer,Subject,Name,Email}) (User,error)`; `ListAccounts(ctx,userID) ([]Account,error)`; `CreateTeam(ctx,userID,name) (Account,error)`; `Role(ctx,userID,accountID) (string,error)`; invitation creation addressed to an existing UniHub user ID, explicit acceptance by that user; owner/admin manage invitations, owner controls finance-role changes, final owner cannot be removed.

- [ ] Write integration cases against an isolated PostgreSQL schema. Same issuer/subject returns the same user and one personal account; same email/different subject produces separate users. Verify invitation requires acceptance, wrong recipient cannot accept, admin cannot grant finance access, and last owner cannot be removed.
- [ ] Run the tests and record missing-behavior failures before implementation.
- [ ] Implement identity creation in one transaction with uniqueness constraints, account membership lookup and locked membership mutations.
- [ ] Run the account integration tests and `go test -race` for the package.

## Task 2: Ledger adapter and money boundaries

Files: `internal/ledger/{ledger,formance}.go`, `internal/ledger/formance_test.go`, `internal/money/{amount,amount_test}.go`.

Port:
```go
type Posting struct { Source, Destination, Asset string; Amount int64 }
type Command struct { Reference string; Postings []Posting }
type Transaction struct { ID, Reference string }
type Ledger interface {
    Post(context.Context, Command) (Transaction, error)
    Find(context.Context, string) (*Transaction, error)
    Balance(context.Context, string, string) (int64, error)
}
```

- [ ] Write HTTP contract tests for successful postings, insufficient funds, unknown transport outcomes, reference lookup, large integer decoding, and idempotency conflict; independently test amount parsing with `1000000`, `0`, `-1`, `1.2`, scientific notation, overflow and fractional cents.
- [ ] Implement the adapter against pinned v2.4.16 routes; set the same deterministic idempotency key and reference on every replay.
- [ ] Verify against the real local Formance container: deposit `100000000`, two transfers of `80000000`; exactly one can succeed and no account becomes negative.

## Task 3: Durable recharge and transfer service

Files: `internal/wallet/{service,payment,worker}.go`, `internal/database/migrations/002_wallet.sql`, `internal/wallet/service_test.go`.

Persistence: recharge orders bind account, target product, amount, currency and merchant. Verified payment receipts have unique provider transaction IDs. Operations bind actor, account, immutable payload digest, request key, reference, status and ledger transaction ID. States are CREATED, SUBMITTING, APPLIED, REJECTED, UNKNOWN, CANCELED. Per-account execution uses PostgreSQL advisory locking on a dedicated connection; unresolved operations retain ordering before later operations for that account.

- [ ] Write tests for unauthorized account access, same key/different payload, duplicate payment callbacks, wrong amount/currency/merchant, two orders sharing a payment transaction, concurrent overspend and response lost after ledger commit.
- [ ] Implement order creation, HMAC-verified sandbox receipt handling and durable operation creation in one database transaction. Browser success never credits money.
- [ ] Implement recovery with original reference/key; do not report a recharge APPLIED until its ledger posting is known. Authorization is rechecked before a new transfer submits; in-flight operations recover rather than silently cancel.
- [ ] Exercise crash/retry by reconstructing the service over the same PostgreSQL data; assert a single posting and unchanged financial ownership.

## Task 4: Verified HTTP API and runnable service

Files: `internal/auth/{oidc,oidc_test}.go`, `internal/httpapi/{server,server_test}.go`, `cmd/unihub/main.go`, `.env.example`, `compose.yaml`, `Makefile`, `scripts/`.

HTTP routes under `/v1`: `/me`, `/accounts`, `/accounts/{id}/members`, `/accounts/{id}/invitations`, `/invitations/{id}/accept`, `/products`, `/accounts/{id}/wallets`, `/accounts/{id}/recharges`, `/accounts/{id}/transfers`, `/accounts/{id}/operations`, `/accounts/{id}/operations/{operation_id}`. Mutations use `Idempotency-Key` for money operations. Payment webhook has its own signature authentication. `/healthz` is public. Sandbox settlement endpoint requires an authenticated account finance role plus explicit sandbox mode; production rejects sandbox mode at startup.

- [ ] Test a signed test issuer: correct access token accepted; wrong issuer/audience, expired token, unsigned token and Casdoor ID/refresh token rejected. Tests may use an in-process issuer; runtime has no trusted-user header or bypass authentication mode.
- [ ] Test API authorization and request validation, including unknown JSON fields, invalid money, missing idempotency key and cross-account operation lookup.
- [ ] Implement strict configuration and HTTP body/time limits. Keep OIDC discovery/JWKS verification and all database operations bounded by context/timeouts.
- [ ] Build and run the API/worker against local services; verify health and unauthenticated denial. Publish documented curl contracts with parameter names, not real credentials.

## Task 5: Independent review and acceptance record

Files: `docs/account-wallet-foundation-validation.md`, `README.md`.

- [ ] Have an independent reviewer inspect identity isolation, duplicate/replayed money flows, authorization boundaries and restart recovery.
- [ ] Address material findings and run relevant regressions, then run `go test -race ./...` and `go vet ./...` plus real PostgreSQL/Formance integration tests.
- [ ] Record exact passed checks and limits. No claim of Casdoor browser acceptance, real payment acceptance, SuperWagie integration or production readiness without corresponding execution evidence.
- [ ] Document startup, test configuration, sandbox limitations and the next phase-one slice (subscriptions/credits/user interface and real product integration).

## Execution decisions

The workspace has no existing repository or application. Work stays in the dedicated `unihub` directory and initializes local version control; no remote repository or publication is created. The current slice intentionally exposes backend APIs first, so financial and identity invariants can be verified before frontend and subscription work depend on them.
