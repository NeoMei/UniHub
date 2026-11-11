package wallet

import (
	"context"
	"errors"
	"testing"

	"unihub/internal/accounts"
)

// ManualRecharge credits a cash wallet without a real payment. It is the
// direct-mode counterpart of the sandbox recharge: same allowlist + finance
// gate, same audited world->cash posting, but no merchant receipt/signature.

func TestManualRechargeCreditsUnifiedWalletAndIsAudited(t *testing.T) {
	s, l, u, a := fixture(t)
	ctx := context.Background()

	op, e := s.ManualRecharge(ctx, u.ID, a.ID, "", "300000000", "manual-1")
	if e != nil {
		t.Fatal(e)
	}
	if op.Kind != "recharge" || op.Status != "CREATED" {
		t.Fatalf("operation: %+v", op)
	}
	// Worker applies the world->cash posting (allowlisted actor passes the gate).
	if e = s.Process(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	bal, e := l.Balance(ctx, Address(a.ID, ""), Asset)
	if e != nil || bal != 300000000 {
		t.Fatalf("unified balance=%d err=%v", bal, e)
	}
	got, e := s.GetOperation(ctx, u.ID, a.ID, op.ID)
	if e != nil || got.Status != "APPLIED" || got.TransactionID == nil {
		t.Fatalf("operation after process: %+v %v", got, e)
	}
	// Audit trail records the manual credit.
	var actions int
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE account_id=$1 AND action='recharge.manual'`, a.ID).Scan(&actions); e != nil {
		t.Fatal(e)
	}
	if actions != 1 {
		t.Fatalf("audit rows=%d", actions)
	}
	// Replay with the same key is a no-op read (idempotent), no second posting.
	again, e := s.ManualRecharge(ctx, u.ID, a.ID, "", "300000000", "manual-1")
	if e != nil || again.ID != op.ID {
		t.Fatalf("replay: %+v %v", again, e)
	}
	if len(l.tx) != 1 {
		t.Fatalf("ledger transactions=%d, want 1", len(l.tx))
	}
}

func TestManualRechargeCreditsProductWallet(t *testing.T) {
	s, l, u, a := fixture(t)
	ctx := context.Background()
	op, e := s.ManualRecharge(ctx, u.ID, a.ID, "llmrouter", "1000000", "manual-prod")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Process(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	bal, e := l.Balance(ctx, Address(a.ID, "llmrouter"), Asset)
	if e != nil || bal != 1000000 {
		t.Fatalf("product balance=%d err=%v", bal, e)
	}
}

func TestManualRechargeDeniedWithoutAllowlist(t *testing.T) {
	s, base, u, a := fixture(t)
	ctx := context.Background()
	// A service whose allowlist was never configured must deny.
	restricted := New(s.pool, s.accounts, base)
	if _, e := restricted.ManualRecharge(ctx, u.ID, a.ID, "", "1000000", "denied"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("unconfigured allowlist must deny: %v", e)
	}
	assertRechargeRows(t, s, 0, 0, 0)
}

func TestManualRechargeDeniedForStrangerEvenIfAllowlisted(t *testing.T) {
	s, _, u, a := fixture(t)
	ctx := context.Background()
	other, e := s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://other", Subject: "stranger"})
	if e != nil {
		t.Fatal(e)
	}
	// Allowlist the stranger, but they hold no finance role on the account.
	s.ConfigureSandboxRechargeUsers([]string{u.ID, other.ID})
	if _, e := s.ManualRecharge(ctx, other.ID, a.ID, "", "1000000", "stranger"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("allowlisted stranger without finance role must be denied: %v", e)
	}
	assertRechargeRows(t, s, 0, 0, 0)
}

func TestManualRechargeIdempotencyConflictAndWholeCents(t *testing.T) {
	s, _, u, a := fixture(t)
	ctx := context.Background()
	if _, e := s.ManualRecharge(ctx, u.ID, a.ID, "", "1000000", "k"); e != nil {
		t.Fatal(e)
	}
	// Same key, different amount -> conflict, never a second write.
	if _, e := s.ManualRecharge(ctx, u.ID, a.ID, "", "2000000", "k"); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed amount under same key: %v", e)
	}
	// Sub-cent amount rejected (money.ParseRecharge requires whole fen).
	if _, e := s.ManualRecharge(ctx, u.ID, a.ID, "", "1", "whole-cents"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("sub-cent amount: %v", e)
	}
	assertRechargeRows(t, s, 0, 0, 1)
}
