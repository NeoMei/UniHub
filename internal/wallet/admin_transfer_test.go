package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/ledger"
)

// adminTransferFixture gives the source unified wallet a preloaded balance and
// returns a second user's personal account as the destination.
func adminTransferFixture(t *testing.T) (*Service, *testLedger, accounts.User, accounts.Account, accounts.Account) {
	t.Helper()
	s, l, u, a := fixture(t)
	ctx := context.Background()
	other, e := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob", Name: "Bob"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := s.accounts.ListAccounts(ctx, other.ID)
	if e != nil || len(as) != 1 {
		t.Fatalf("target accounts %+v %v", as, e)
	}
	return s, l, u, a, as[0]
}

func staticTarget(account, product string) AdminTransferTarget {
	return func(context.Context, accounts.Querier) (string, string, error) { return account, product, nil }
}

// The defining property: money is moved, never minted. The source personal
// unified wallet decreases, the destination increases by exactly the same
// amount, and no posting has world as its source.
func TestAdminTransferIsConservedAndAudited(t *testing.T) {
	s, l, u, a, target := adminTransferFixture(t)
	ctx := context.Background()
	l.balance[Address(a.ID, "")] = 5_000_000

	op, e := s.AdminTransfer(ctx, u.ID, "3000000", "at-1", staticTarget(target.ID, ""))
	if e != nil {
		t.Fatal(e)
	}
	if op.Kind != "admin_transfer" || op.Status != "CREATED" || op.AccountID != a.ID {
		t.Fatalf("operation: %+v", op)
	}
	if e = s.Process(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	if got := l.balance[Address(a.ID, "")]; got != 2_000_000 {
		t.Fatalf("source balance=%d, want 2000000", got)
	}
	if got := l.balance[Address(target.ID, "")]; got != 3_000_000 {
		t.Fatalf("target balance=%d, want 3000000", got)
	}
	for _, tx := range l.tx {
		for _, p := range tx.Postings {
			if p.Source == "world" {
				t.Fatalf("conserved transfer must not mint from world: %+v", p)
			}
			if p.Source != Address(a.ID, "") || p.Destination != Address(target.ID, "") || p.Amount != 3_000_000 {
				t.Fatalf("unexpected posting: %+v", p)
			}
		}
	}
	got, e := s.GetOperation(ctx, u.ID, a.ID, op.ID)
	if e != nil || got.Status != "APPLIED" || got.TransactionID == nil {
		t.Fatalf("operation after process: %+v %v", got, e)
	}
	var actions int
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE account_id=$1 AND action='transfer.admin'`, a.ID).Scan(&actions); e != nil {
		t.Fatal(e)
	}
	if actions != 1 {
		t.Fatalf("audit rows=%d, want 1", actions)
	}
	// Replay with the same key is a no-op read; no second posting.
	again, e := s.AdminTransfer(ctx, u.ID, "3000000", "at-1", staticTarget(target.ID, ""))
	if e != nil || again.ID != op.ID {
		t.Fatalf("replay: %+v %v", again, e)
	}
	if len(l.tx) != 1 {
		t.Fatalf("ledger transactions=%d, want 1", len(l.tx))
	}
}

func TestAdminTransferCreditsProductWallet(t *testing.T) {
	s, l, u, a, target := adminTransferFixture(t)
	ctx := context.Background()
	l.balance[Address(a.ID, "")] = 2_000_000
	op, e := s.AdminTransfer(ctx, u.ID, "1000000", "at-prod", staticTarget(target.ID, "llmrouter"))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Process(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	if got := l.balance[Address(target.ID, "llmrouter")]; got != 1_000_000 {
		t.Fatalf("product balance=%d, want 1000000", got)
	}
	if got := l.balance[Address(target.ID, "")]; got != 0 {
		t.Fatalf("unified balance should stay 0, got %d", got)
	}
}

func TestAdminTransferInsufficientFundsRejected(t *testing.T) {
	s, l, u, a, target := adminTransferFixture(t)
	ctx := context.Background()
	l.balance[Address(a.ID, "")] = 10000 // 0.01 yuan, less than 1 yuan
	op, e := s.AdminTransfer(ctx, u.ID, "1000000", "at-poor", staticTarget(target.ID, ""))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Process(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	got, e := s.GetOperation(ctx, u.ID, a.ID, op.ID)
	if e != nil || got.Status != "REJECTED" || got.ErrorCode != "insufficient_funds" {
		t.Fatalf("insufficient funds: %+v %v", got, e)
	}
	if len(l.tx) != 0 {
		t.Fatalf("rejected transfer posted %d transactions", len(l.tx))
	}
}

func TestAdminTransferIdempotencyConflictAndWholeCents(t *testing.T) {
	s, _, u, _, target := adminTransferFixture(t)
	ctx := context.Background()
	if _, e := s.AdminTransfer(ctx, u.ID, "1000000", "k", staticTarget(target.ID, "")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.AdminTransfer(ctx, u.ID, "2000000", "k", staticTarget(target.ID, "")); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed amount under same key: %v", e)
	}
	if _, e := s.AdminTransfer(ctx, u.ID, "1", "whole-cents", staticTarget(target.ID, "")); !errors.Is(e, ErrInvalid) {
		t.Fatalf("sub-cent amount: %v", e)
	}
	if _, e := s.AdminTransfer(ctx, u.ID, "1000000", "", staticTarget(target.ID, "")); !errors.Is(e, ErrInvalid) {
		t.Fatalf("empty idempotency key: %v", e)
	}
}

func TestAdminTransferResolveGateDenied(t *testing.T) {
	s, _, u, _, target := adminTransferFixture(t)
	ctx := context.Background()
	_, e := s.AdminTransfer(ctx, u.ID, "1000000", "at-denied", func(context.Context, accounts.Querier) (string, string, error) {
		return target.ID, "", accounts.ErrForbidden
	})
	if !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("gate denial must propagate: %v", e)
	}
}

func TestAdminTransferRejectsUnknownProductAndEmptyTarget(t *testing.T) {
	s, _, u, _, target := adminTransferFixture(t)
	ctx := context.Background()
	if _, e := s.AdminTransfer(ctx, u.ID, "1000000", "at-badproduct", staticTarget(target.ID, "does-not-exist")); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unknown product: %v", e)
	}
	if _, e := s.AdminTransfer(ctx, u.ID, "1000000", "at-emptytarget", staticTarget("", "")); !errors.Is(e, ErrInvalid) {
		t.Fatalf("empty target: %v", e)
	}
}

// Source is derived from actor's personal_user_id, never from a caller field:
// an actor without a personal account cannot transfer.
func TestAdminTransferRequiresPersonalSource(t *testing.T) {
	s, _, _, _, target := adminTransferFixture(t)
	ctx := context.Background()
	_, e := s.AdminTransfer(ctx, "no-such-user", "1000000", "at-nosource", staticTarget(target.ID, ""))
	if !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("actor without personal account: %v", e)
	}
}

// Guard against a future worker change that would accidentally give the new
// kind a world source: the stored command is the conserved posting itself.
func TestAdminTransferStoredCommandIsConserved(t *testing.T) {
	s, _, u, a, target := adminTransferFixture(t)
	ctx := context.Background()
	op, e := s.AdminTransfer(ctx, u.ID, "1000000", "at-command", staticTarget(target.ID, "llmrouter"))
	if e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e = s.pool.QueryRow(ctx, `SELECT command FROM wallet_operations WHERE id=$1`, op.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var cmd ledger.Command
	if e = json.Unmarshal(raw, &cmd); e != nil {
		t.Fatal(e)
	}
	if len(cmd.Postings) != 1 || cmd.Postings[0].Source != Address(a.ID, "") || cmd.Postings[0].Destination != Address(target.ID, "llmrouter") {
		t.Fatalf("stored command: %+v", cmd)
	}
}
