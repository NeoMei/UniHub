package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"unihub/internal/wallet"
)

func productAutoAddonFixture(t *testing.T, trigger, threshold string) (fixture, string, NativeConversion) {
	t.Helper()
	f, cid, conversion := nativeCreditFixture(t)
	ctx := context.Background()
	_, addon := publishProductCreditOffers(t, f, "month")
	p, e := f.s.SetProductCreditAutoAddon(ctx, f.actor, f.account, "superwagie", AutoAddonPolicyRequest{Enabled: true, OfferID: addon.ID, Trigger: trigger, Threshold: threshold})
	if e != nil || p.Version != 1 {
		t.Fatalf("setting mandate: %+v %v", p, e)
	}
	now := time.Now().UTC()
	seedProductCreditBatch(t, f, Batch{ProductID: "superwagie", Kind: "base", Original: "16", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), CreatedAt: now}, ProductCreditScopeV1)
	if _, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET credit_scope='product_v1' WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	fundCash(t, f, "", 4000000)
	return f, cid, conversion
}

func TestAutoAddonDoesNotChargeFromARiskHold(t *testing.T) {
	f, cid, conversion := productAutoAddonFixture(t, "threshold", "10")
	ctx := context.Background()
	r := creditNativeRequest(f, cid, "product-credit-hold", "10", conversion)
	r.CreditScope, r.SourceVerified = ProductCreditScopeV1, true
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	next := creditNativeRequest(f, cid, "would-overcommit", "2", conversion)
	next.CreditScope, next.SourceVerified, next.MinimumCreditAmount = ProductCreditScopeV1, true, "3"
	if _, e := f.s.ReserveNative(ctx, "llmrouter", next); !errors.Is(e, ErrInsufficientCredits) {
		t.Fatalf("short reserve: %v", e)
	}
	if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 4001000)
	settle := NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "5", Amount: "8"}
	for range 2 {
		if _, e := f.s.SettleNative(ctx, "llmrouter", settle); e != nil {
			t.Fatal(e)
		}
	}
	// A lost response after the debit must recover the same original purchase.
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
			t.Fatal(e)
		}
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 2001000)
	var paid int
	if e := f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_orders WHERE account_id=$1 AND snapshot->>'auto_addon_policy_version'='1' AND snapshot->>'fulfillment_status'='FULFILLED'`, f.account).Scan(&paid); e != nil || paid != 1 {
		t.Fatalf("automatic order duplicated or incomplete: %d %v", paid, e)
	}
}

func TestAutoAddonExhaustionRequiresConfirmedMinimum(t *testing.T) {
	f, cid, conversion := productAutoAddonFixture(t, "exhausted", "")
	ctx := context.Background()
	r := creditNativeRequest(f, cid, "quote-risk-margin", "15", conversion)
	r.CreditScope, r.SourceVerified = ProductCreditScopeV1, true
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); !errors.Is(e, ErrInsufficientCredits) {
		t.Fatalf("risk hold: %v", e)
	}
	if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 4001000)
	r.CallID, r.MinimumCreditAmount = "confirmed-minimum", "17"
	for range 2 {
		if _, e := f.s.ReserveNative(ctx, "llmrouter", r); !errors.Is(e, ErrInsufficientCredits) {
			t.Fatalf("minimum should exceed available credits: %v", e)
		}
	}
	if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 2001000)
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatalf("retry failed after funding: %v", e)
	}
}
