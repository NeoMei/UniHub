package billing

import (
	"context"
	"errors"
	"testing"
)

func TestManagedKeyBudgetKeepsUnknownReserveAndTopupExposureUntilRecovery(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	fundCash(t, f, "llmrouter", 100)
	cid := cashCredential(t, f)
	if _, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET credential_kind='api_key',max_total='60',max_per_call='60' WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	reserve := func(id, amount string) (Call, error) {
		return f.s.ReserveWithScope(ctx, "router", ReserveRequest{CallID: id, ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: id, RateID: "cash-rate", Amount: amount, CredentialID: cid}, "default", "task-"+id)
	}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	call, e := reserve("unknown-budget", "40")
	if !errors.Is(e, ErrPending) {
		t.Fatalf("lost reserve: %+v %v", call, e)
	}
	assertTotals := func(held, used string) {
		t.Helper()
		h, u, e := UsageTotalsOn(ctx, f.s.pool, f.account, "superwagie", "default", CashAsset, cid)
		if e != nil || h != held || u != used {
			t.Fatalf("totals %s/%s want %s/%s: %v", h, u, held, used, e)
		}
	}
	assertTotals("40", "0")
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, e = f.s.TopUp(ctx, "router", "unknown-budget", "20", "grow"); !errors.Is(e, ErrPending) {
		t.Fatalf("lost topup: %v", e)
	}
	assertTotals("60", "0")
	// A lower new cap must not cause recovery to reauthorize an already applied
	// hold. It prevents new exposure; it cannot undo the ledger's accepted hold.
	if _, e = f.s.SetUsageBudget(ctx, f.actor, f.account, "superwagie", "default", FundingPrepaidCash, UsageBudgetRequest{MaxTotal: "1"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	assertTotals("60", "0")
	if _, e = reserve("over-budget", "1"); e == nil {
		t.Fatal("new exposure bypassed lowered cap")
	}
	if _, e = f.s.Release(ctx, "router", "unknown-budget", "not-dispatched"); e != nil {
		t.Fatal(e)
	}
	assertTotals("0", "0")
	if _, e = reserve("released-budget", "1"); e != nil {
		t.Fatal(e)
	}
}

func TestUsageLimitValidationPreservesExactStringsAndZeroCap(t *testing.T) {
	for _, value := range []string{"0", "1", "9007199254740993", "9223372036854775807", ""} {
		if e := ValidateUsageLimits(value, value); e != nil {
			t.Fatalf("valid %q: %v", value, e)
		}
	}
	for _, value := range []string{"-1", "+1", "1.1", "01", " 1", "9223372036854775808"} {
		if e := ValidateUsageLimits(value, ""); e == nil {
			t.Fatalf("invalid accepted %q", value)
		}
	}
	if e := enforceUsageLimit("0", "", "0", "0", 1, 1); e == nil {
		t.Fatal("zero cap became unlimited")
	}
	if remaining := UsageRemaining("9007199254740993", "1", "1"); remaining == nil || *remaining != "9007199254740991" {
		t.Fatalf("rounded remaining %v", remaining)
	}
}
