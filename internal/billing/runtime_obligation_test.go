package billing

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
)

func TestRuntimeAcceptedTaskCanFinishOriginalCostAfterRevocation(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	_, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, RuntimeAdjustmentRequest{RequestKey: "credit", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "credit", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion})
	if e != nil {
		t.Fatal(e)
	}
	r := RuntimeReserveRequest{CallID: "accepted-task", LocalUserID: "42", LocalKeyID: "7", AccountID: b.PersonalAccountID, RequestDigest: "body", Model: "video", Operation: "task", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "v1", PricingDigest: "original-price", Amount: "20"}
	if _, e = s.RuntimeReserve(ctx, scope, r); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RuntimeSettle(ctx, scope, r.CallID, RuntimeSettlementRequest{Amount: "20", PricingDigest: r.PricingDigest, Usage: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, b.UserID); e != nil {
		t.Fatal(e)
	}
	r.CallID = "new-provider-call"
	if _, e = s.RuntimeReserve(ctx, scope, r); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("new call after revocation: %v", e)
	}
	r.ParentCallID = "accepted-task"
	r.CallID = "fee-correction"
	r.Amount = "10"
	r.RequestDigest = "original-final-cost"
	bad := r
	bad.LocalKeyID = "different-key"
	if _, e = s.RuntimeReserve(ctx, scope, bad); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("original key changed: %v", e)
	}
	bad = r
	bad.PricingDigest = "changed-price"
	if _, e = s.RuntimeReserve(ctx, scope, bad); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("original price changed: %v", e)
	}
	for i := 0; i < 2; i++ {
		if _, e = s.RuntimeReserve(ctx, scope, r); e != nil {
			t.Fatal(e)
		}
		if _, e = s.RuntimeSettle(ctx, scope, r.CallID, RuntimeSettlementRequest{Amount: "10", PricingDigest: r.PricingDigest, Usage: []byte(`{}`)}); e != nil {
			t.Fatal(e)
		}
	}
	n, e := s.ledger.Balance(ctx, RuntimeAddress(scope, b.PersonalAccountID, "available"), RuntimeAsset)
	if e != nil || n != 70 {
		t.Fatalf("final accepted cost %d: %v", n, e)
	}
}

type rejectObligationOnce struct {
	ledger.Ledger
	once bool
}

func (l *rejectObligationOnce) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	if l.once {
		l.once = false
		return ledger.Transaction{}, ledger.ErrInsufficientFunds
	}
	return l.Ledger.Post(ctx, c)
}

func TestRuntimeAcceptedObligationRetriesProvenNoPostingRejection(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	_, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, RuntimeAdjustmentRequest{RequestKey: "credit", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "credit", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion})
	if e != nil {
		t.Fatal(e)
	}
	r := RuntimeReserveRequest{CallID: "parent", LocalUserID: "42", LocalKeyID: "7", AccountID: b.PersonalAccountID, RequestDigest: "body", Model: "m", Operation: "task", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "v1", PricingDigest: "price", Amount: "20"}
	if _, e = s.RuntimeReserve(ctx, scope, r); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RuntimeSettle(ctx, scope, r.CallID, RuntimeSettlementRequest{Amount: "20", PricingDigest: r.PricingDigest, Usage: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	r.ParentCallID = "parent"
	r.CallID = "child"
	r.Amount = "10"
	r.RequestDigest = "adjustment"
	s.ledger = &rejectObligationOnce{Ledger: s.ledger, once: true}
	if _, e = s.RuntimeReserve(ctx, scope, r); !errors.Is(e, ledger.ErrInsufficientFunds) {
		t.Fatalf("expected known no-posting rejection: %v", e)
	}
	for i := 0; i < 2; i++ {
		if _, e = s.RuntimeReserve(ctx, scope, r); e != nil {
			t.Fatal(e)
		}
		if _, e = s.RuntimeSettle(ctx, scope, r.CallID, RuntimeSettlementRequest{Amount: "10", PricingDigest: r.PricingDigest, Usage: []byte(`{}`)}); e != nil {
			t.Fatal(e)
		}
	}
	n, e := s.ledger.Balance(ctx, RuntimeAddress(scope, b.PersonalAccountID, "available"), RuntimeAsset)
	if e != nil || n != 70 {
		t.Fatalf("retry duplicated cost %d: %v", n, e)
	}
	var retries int
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE kind='runtime_reserve' AND effect->'runtime_call'->>'call_id'='child'`).Scan(&retries); e != nil || retries != 2 {
		t.Fatalf("immutable no-posting retry attempts %d: %v", retries, e)
	}
}

func TestRuntimeAcceptedCallFinalTopUpDoesNotReauthorizeNewUse(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	_, e := s.RuntimeAdjustCredit(ctx, scope, b.UserID, RuntimeAdjustmentRequest{RequestKey: "credit", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "credit", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion})
	if e != nil {
		t.Fatal(e)
	}
	r := RuntimeReserveRequest{CallID: "in-flight", LocalUserID: "42", LocalKeyID: "7", AccountID: b.PersonalAccountID, RequestDigest: "body", Model: "audio", Operation: "realtime", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "v1", PricingDigest: "price", Amount: "20"}
	if _, e = s.RuntimeReserve(ctx, scope, r); e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, b.UserID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RuntimeTopUp(ctx, scope, r.CallID, RuntimeTopUpRequest{RequestKey: "new-use", Amount: "10"}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("unproved new extension: %v", e)
	}
	s.ledger = &rejectObligationOnce{Ledger: s.ledger, once: true}
	if _, e = s.RuntimeTopUp(ctx, scope, r.CallID, RuntimeTopUpRequest{RequestKey: "final-invoice", Amount: "10", AcceptedCall: true}); !errors.Is(e, ledger.ErrInsufficientFunds) {
		t.Fatalf("expected known no-posting extension rejection: %v", e)
	}
	for i := 0; i < 2; i++ {
		if _, e = s.RuntimeTopUp(ctx, scope, r.CallID, RuntimeTopUpRequest{RequestKey: "final-invoice", Amount: "10", AcceptedCall: true}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.RuntimeSettle(ctx, scope, r.CallID, RuntimeSettlementRequest{Amount: "30", PricingDigest: r.PricingDigest, Usage: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	n, e := s.ledger.Balance(ctx, RuntimeAddress(scope, b.PersonalAccountID, "available"), RuntimeAsset)
	if e != nil || n != 70 {
		t.Fatalf("final topup %d: %v", n, e)
	}
}
