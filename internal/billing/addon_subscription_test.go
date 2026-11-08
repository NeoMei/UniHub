package billing

import (
	"context"
	"testing"
	"time"
)

func TestAddonPurchaseRequiresSameProductActiveSubscriptionForExplicitProfile(t *testing.T) {
	for _, state := range []string{"missing", "expired", "cancelled", "other-product", "active", "legacy-profile", "independent-addon"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			f.auth(t)
			ctx := context.Background()
			if state != "legacy-profile" {
				profile := setProfile(t, f, "hybrid")
				if state == "independent-addon" {
					profile.Usage.RequireSubscription = false
					if _, err := f.s.SaveProfile(ctx, f.actor, profile.ProductID, profile); err != nil {
						t.Fatal(err)
					}
				}
			}
			addon := publishTestOffer(t, f, Offer{ID: "sw-addon", ProductID: "superwagie", Name: "AI points", AccountKind: "both", Kind: "addon", Price: "1000000", MonthlyCredit: "2000000", ValidityMonths: 12})
			switch state {
			case "active", "expired", "cancelled":
				membership := f.buy(t, "sandbox-sw-month-1", "membership")
				if state == "expired" {
					f.s.now = func() time.Time { return membership.End }
				}
				if state == "cancelled" {
					if _, err := f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{status}','"cancelled"') WHERE id=$1`, membership.SubscriptionID); err != nil {
						t.Fatal(err)
					}
				}
			case "other-product":
				other := f.offers[0]
				other.ID, other.ProductID = "other-product-membership", "llmrouter"
				publishTestOffer(t, f, other)
				fundCash(t, f, "llmrouter", 1000000)
				f.buy(t, other.ID, "other-membership")
			}
			before, err := f.s.Orders(ctx, f.actor, f.account)
			if err != nil {
				t.Fatal(err)
			}
			order, err := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: addon.ID, Key: "addon"})
			allowed := state == "active" || state == "legacy-profile" || state == "independent-addon"
			if allowed {
				if err != nil || order.PaymentStatus != "PAID" || order.FulfillmentStatus != "FULFILLED" || order.GrantAmount != "2000000" {
					t.Fatalf("eligible addon: order=%+v err=%v", order, err)
				}
				return
			}
			if err == nil || err.Error() != "billing: subscription_required" {
				t.Fatalf("ineligible addon sold: err=%v", err)
			}
			after, err := f.s.Orders(ctx, f.actor, f.account)
			if err != nil || len(after) != len(before) {
				t.Fatalf("rejected addon created an order: before=%d after=%d err=%v", len(before), len(after), err)
			}
		})
	}
}

func TestPaidAddonReplaySurvivesSubscriptionExpiry(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	setProfile(t, f, "hybrid")
	membership := f.buy(t, "sandbox-sw-month-1", "membership")
	addon := publishTestOffer(t, f, Offer{ID: "sw-addon", ProductID: "superwagie", Name: "AI points", AccountKind: "both", Kind: "addon", Price: "1000000", MonthlyCredit: "2000000", ValidityMonths: 12})
	first := f.buy(t, addon.ID, "paid-addon")
	f.s.now = func() time.Time { return membership.End }
	replay := f.buy(t, addon.ID, "paid-addon")
	if replay.ID != first.ID || replay.PaymentStatus != "PAID" || replay.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("paid addon replay changed: first=%+v replay=%+v", first, replay)
	}
	var count int
	if err := f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_batches WHERE order_id=$1`, first.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("paid addon replay duplicated its grant: count=%d err=%v", count, err)
	}
}

func TestAddonSubscriptionPolicyRecheckedBeforeNewPayment(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	addon := publishTestOffer(t, f, Offer{ID: "sw-addon", ProductID: "superwagie", Name: "AI points", AccountKind: "both", Kind: "addon", Price: "1000000", MonthlyCredit: "2000000", ValidityMonths: 12})
	paused := &pauseSaleBalance{Ledger: f.l, entered: make(chan struct{}), resume: make(chan struct{})}
	f.s.ledger = paused
	done := make(chan error, 1)
	go func() {
		_, err := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: addon.ID, Key: "racing-addon"})
		done <- err
	}()
	<-paused.entered
	setProfile(t, f, "hybrid")
	close(paused.resume)
	if err := <-done; err == nil || err.Error() != "billing: subscription_required" {
		t.Fatalf("new explicit subscription policy bypassed: %v", err)
	}
	var count int
	if err := f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_orders WHERE account_id=$1`, f.account).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected addon persisted an order: count=%d err=%v", count, err)
	}
}
