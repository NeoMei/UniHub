package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

func productAutoAddonFixture(t *testing.T, trigger, threshold string) (fixture, string, NativeConversion) {
	t.Helper()
	f, cid, _ := nativeCreditFixture(t)
	conversion := exactProductCreditTestConversion()
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
	r := creditNativeRequest(f, cid, "product-credit-hold", "15", conversion)
	r.CreditScope, r.SourceVerified = ProductCreditScopeV1, true
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	next := creditNativeRequest(f, cid, "would-overcommit", "3", conversion)
	next.CreditScope, next.SourceVerified, next.MinimumCreditAmount = ProductCreditScopeV1, true, "3"
	if _, e := f.s.ReserveNative(ctx, "llmrouter", next); !errors.Is(e, ErrInsufficientCredits) {
		t.Fatalf("short reserve: %v", e)
	}
	if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 4001000)
	settle := NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "8", Amount: "8"}
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

func TestAutoAddonResumesTheSameTriggerAfterCashRecharge(t *testing.T) {
	f, cid, conversion := productAutoAddonFixture(t, "threshold", "10")
	ctx := t.Context()
	address := wallet.Address(f.account, "")
	if _, e := f.l.Post(ctx, ledger.Command{Reference: "spend-wallet-before-auto-addon", Postings: []ledger.Posting{{Source: address, Destination: "world", Asset: CashAsset, Amount: 4001000}}}); e != nil {
		t.Fatal(e)
	}
	r := creditNativeRequest(f, cid, "auto-addon-without-cash", "8", conversion)
	r.CreditScope, r.SourceVerified = ProductCreditScopeV1, true
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatal(e)
	}
	settle := NativeSettlementRequest{CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "8", Amount: "8"}
	if _, e := f.s.SettleNative(ctx, "llmrouter", settle); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
			t.Fatal(e)
		}
	}
	cashBalance(t, f, address, 0)
	fundCash(t, f, "", 2500000)
	for range 2 {
		if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
			t.Fatal(e)
		}
	}
	cashBalance(t, f, address, 500000)
	w, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	if e != nil || w.Available != "200000008" || len(w.Batches) != 2 {
		t.Fatalf("cash recharge should fulfill precisely one pack for the original trigger: %+v %v", w, e)
	}
}

func TestAutoAddonExhaustionRequiresZeroBalance(t *testing.T) {
	f, cid, conversion := productAutoAddonFixture(t, "exhausted", "")
	ctx := context.Background()
	r := creditNativeRequest(f, cid, "quote-risk-margin", "23", conversion)
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
	cashBalance(t, f, wallet.Address(f.account, ""), 4001000)
	consume := creditNativeRequest(f, cid, "spend-remaining-credits", "16", conversion)
	consume.CreditScope, consume.SourceVerified = ProductCreditScopeV1, true
	if _, e := f.s.ReserveNative(ctx, "llmrouter", consume); e != nil {
		t.Fatal(e)
	}
	settle := NativeSettlementRequest{CallID: consume.CallID, PricingDigest: consume.Quote.PricingDigest, NativeQuota: "16", Amount: "16"}
	if _, e := f.s.SettleNative(ctx, "llmrouter", settle); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.maintainProductCreditAutoAddons(ctx, time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 2001000)
	if _, e := f.s.ReserveNative(ctx, "llmrouter", r); e != nil {
		t.Fatalf("retry failed after funding: %v", e)
	}
}

func TestAutoAddonTriggersOnceAfterRealExpiryDuringActiveMembership(t *testing.T) {
	f, _, _ := productAutoAddonFixture(t, "exhausted", "")
	ctx := context.Background()
	if _, e := f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{offer,monthly_credit}','"0"') WHERE account_id=$1`, f.account); e != nil {
		t.Fatal(e)
	}
	when := time.Now().UTC().Add(2 * time.Hour)
	f.s.now = func() time.Time { return when }
	for range 2 {
		if _, e := f.s.Maintain(ctx, when); e != nil {
			t.Fatal(e)
		}
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 2001000)
	w, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	if e != nil || w.Available != "200000000" || len(w.Batches) != 2 || w.Batches[0].Expired != "16" {
		t.Fatalf("real expiry didn't trigger exactly one add-on: %+v %v", w, e)
	}
}

func TestPaidMonthlyGrantRearmsWithoutBuyingAnotherAddon(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	membership, addon := publishProductCreditOffers(t, f, "month")
	if _, e := f.s.SetProductCreditAutoAddon(ctx, f.actor, f.account, "superwagie", AutoAddonPolicyRequest{Enabled: true, OfferID: addon.ID, Trigger: "threshold", Threshold: "5000000"}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.pool.Exec(ctx, `UPDATE billing_product_credit_auto_addon_policies SET armed=false WHERE account_id=$1 AND product_id='superwagie'`, f.account); e != nil {
		t.Fatal(e)
	}
	f.buy(t, membership.ID, "new-current-grant")
	if _, e := f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	var armed bool
	if e := f.s.pool.QueryRow(ctx, `SELECT armed FROM billing_product_credit_auto_addon_policies WHERE account_id=$1 AND product_id='superwagie'`, f.account).Scan(&armed); e != nil || !armed {
		t.Fatalf("confirmed membership grant did not rearm the threshold: %v %v", armed, e)
	}
	cashBalance(t, f, wallet.Address(f.account, ""), 997000000)
}
