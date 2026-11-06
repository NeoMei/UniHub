package billing

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/wallet"
)

func TestNativeScopeUpgradePreservesExistingCallTopUp(t *testing.T) {
	f, cid := nativeFixture(t)
	ctx := context.Background()
	_, err := f.s.pool.Exec(ctx, `UPDATE model_credentials SET model_scope='selected',pricing_source='',credential_version=1,models='["legacy-model"]' WHERE id=$1`, cid)
	if err != nil {
		t.Fatal(err)
	}
	rate := cashRate("review-legacy-rate")
	rate.Model = "legacy-model"
	if _, err = f.s.PublishRate(ctx, f.actor, rate); err != nil {
		t.Fatal(err)
	}
	call, err := f.s.ReserveWithScope(ctx, "llmrouter", ReserveRequest{CallID: "review-inflight", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", CredentialID: cid, RequestDigest: "review", RateID: rate.ID, Amount: "30"}, "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.TopUp(ctx, "llmrouter", call.CallID, "5", "before-upgrade"); err != nil {
		t.Fatalf("before upgrade: %v", err)
	}
	_, err = f.s.pool.Exec(ctx, `UPDATE model_credentials SET model_scope='llmrouter_available',pricing_source='llmrouter_native',credential_version=credential_version+1 WHERE id=$1`, cid)
	if err != nil {
		t.Fatal(err)
	}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, err = f.s.TopUp(ctx, "llmrouter", call.CallID, "5", "after-upgrade"); !errors.Is(err, ErrPending) {
		t.Fatalf("same Key's existing call stopped after scope expansion: %v", err)
	}
	if _, err = f.s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.s.GetCall(ctx, "llmrouter", call.CallID)
	if err != nil || recovered.Held != "40" || recovered.Rate.ID != rate.ID || recovered.NativePricing != nil {
		t.Fatalf("legacy recovery changed frozen contract: %+v %v", recovered, err)
	}
	if _, err = f.s.ReserveWithScope(ctx, "llmrouter", ReserveRequest{CallID: "review-new-legacy", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", CredentialID: cid, RequestDigest: "review-new", RateID: rate.ID, Amount: "5"}, "default", ""); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("new legacy reservation used upgraded key: %v", err)
	}
	if _, err = f.s.TopUp(ctx, "llmrouter", call.CallID, "11", "over-budget"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("legacy continuation bypassed key budget: %v", err)
	}
	_, err = f.s.pool.Exec(ctx, `UPDATE model_credentials SET revoked_at=now() WHERE id=$1`, cid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.TopUp(ctx, "llmrouter", call.CallID, "1", "revoked"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("legacy continuation bypassed revocation: %v", err)
	}
	settled, err := f.s.Settle(ctx, "llmrouter", SettlementRequest{CallID: call.CallID, Usage: &Usage{Input: "40", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}})
	if err != nil || settled.Charged != "40" || settled.Rate.ID != rate.ID {
		t.Fatalf("legacy price lost after upgrade: %+v %v", settled, err)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 60)
	cashBalance(t, f, "commercial:revenue:llmrouter", 40)
}
