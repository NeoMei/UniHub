package billing

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

func TestRuntimeCanceledTopUpReturnsDefiniteRejectionOnReplay(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	_, err := s.RuntimeAdjustCredit(ctx, scope, b.UserID, RuntimeAdjustmentRequest{RequestKey: "gift", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Reason: "credit", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion})
	if err != nil {
		t.Fatal(err)
	}
	call, err := s.RuntimeReserve(ctx, scope, RuntimeReserveRequest{CallID: "call", LocalUserID: "42", LocalKeyID: "7", AccountID: b.PersonalAccountID, RequestDigest: "body", Model: "m", Operation: "chat", Asset: RuntimeAsset, UnitVersion: RuntimeUnitVersion, PriceVersion: "v1", PricingDigest: "price", Amount: "20"})
	if err != nil {
		t.Fatal(err)
	}
	in := RuntimeTopUpRequest{RequestKey: "extension", Amount: "10"}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(tx)
	cmd, err := s.createCommand(ctx, tx, b.PersonalAccountID, b.UserID, "runtime_topup", runtimeID(scope, "topup", "call\x00extension"), nil, effect{RuntimeCall: &call, RuntimeRequestDigest: hash(in)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE billing_commands SET status='CANCELED' WHERE id=$1`, cmd.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = s.RuntimeTopUp(ctx, scope, "call", in)
	if !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("canceled extension returned non-terminal result: %v", err)
	}
}
