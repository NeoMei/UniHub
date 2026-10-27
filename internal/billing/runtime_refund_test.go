package billing

import (
	"context"
	"errors"
	"testing"
)

func TestRuntimeRefundKeepsOriginalPayerAndCannotRefundMoreThanCharged(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	_, err := s.RuntimeAdjustCredit(ctx, scope, b.UserID, RuntimeAdjustmentRequest{RequestKey: "gift", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "original credit", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RuntimeReserve(ctx, scope, RuntimeReserveRequest{CallID: "async-task", LocalUserID: "42", LocalKeyID: "7", AccountID: b.PersonalAccountID, RequestDigest: "original", Model: "video", Operation: "task", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "original-v1", PricingDigest: "price", Amount: "60"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RuntimeSettle(ctx, scope, "async-task", RuntimeSettlementRequest{Amount: "60", PricingDigest: "price", Usage: []byte(`{"requests":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	req := RuntimeRefundRequest{RequestKey: "provider-failed", Amount: "20", Reason: "confirmed failed task"}
	for i := 0; i < 2; i++ {
		if _, err = s.RuntimeRefund(ctx, scope, "async-task", req); err != nil {
			t.Fatal(err)
		}
	}
	balance, err := s.RuntimeBalance(ctx, scope, "42", b.PersonalAccountID)
	if err != nil || balance.Available != "60" || balance.Consumed != "40" {
		t.Fatalf("balance %+v %v", balance, err)
	}
	req.Amount = "21"
	if _, err = s.RuntimeRefund(ctx, scope, "async-task", req); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting refund: %v", err)
	}
	req.RequestKey = "too-much"
	req.Amount = "41"
	if _, err = s.RuntimeRefund(ctx, scope, "async-task", req); !errors.Is(err, ErrConflict) {
		t.Fatalf("over-refund: %v", err)
	}
}
