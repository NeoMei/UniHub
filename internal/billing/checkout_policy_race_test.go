package billing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestExternalAddonCurrentMembershipPolicy(t *testing.T) {
	for _, creditOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(creditOnly), func(t *testing.T) {
			f, p := checkoutFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			profile, e := BillingPreset("superwagie", "hybrid")
			if e != nil {
				t.Fatal(e)
			}
			profile.Usage.RequireSubscription = false
			profile, e = f.s.SaveProfile(ctx, f.actor, "superwagie", profile)
			if e != nil {
				t.Fatal(e)
			}
			offer := Offer{ID: "review-sw-addon", ProductID: "superwagie", Name: "Add-on", AccountKind: "both", Kind: "addon", Price: "9800000", MonthlyCredit: "200000000", ValidityMonths: 12}
			if _, e = f.s.SaveOffer(ctx, f.actor, offer); e != nil {
				t.Fatal(e)
			}
			if _, e = f.s.PublishOffer(ctx, f.actor, offer.ID); e != nil {
				t.Fatal(e)
			}
			tx, e := f.s.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback(context.Background())
			if e = lockBillingProduct(ctx, tx, "superwagie"); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: offer.ID, Provider: "wechat", Key: "race-policy"})
				done <- e
			}()
			deadline := time.Now().Add(4 * time.Second)
			for {
				var waiting bool
				if e = f.s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%SELECT id FROM products WHERE id=$1 AND enabled FOR UPDATE%')`).Scan(&waiting); e != nil {
					t.Fatal(e)
				}
				if waiting {
					break
				}
				select {
				case early := <-done:
					t.Fatalf("checkout returned before product lock: %v", early)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("checkout did not reach product lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			profile.Version++
			profile.Usage.RequireSubscription = !creditOnly
			profile.Usage.RequireSubscriptionForCredits = creditOnly
			if e = insertBillingProfile(ctx, tx, f.actor, profile); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			current, readErr := f.s.Profile(ctx, "superwagie")
			if readErr != nil {
				t.Fatal(readErr)
			}
			t.Logf("committed membership policy=%v checkout_error=%v provider_creates=%d", current.Usage.RequireSubscription, e, len(p.requests))
			if !errors.Is(e, ErrSubscriptionRequired) {
				t.Fatal("new checkout ignored current membership policy after product lock")
			}
			var count int
			if e = f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_checkouts WHERE account_id=$1`, f.account).Scan(&count); e != nil {
				t.Fatal(e)
			}
			if count != 0 || len(p.requests) != 0 {
				t.Fatalf("admission leaked rows=%d provider=%d", count, len(p.requests))
			}
		})
	}
}

func TestExternalAddonAcceptedReplayAfterMembershipPolicyAndExpiry(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	profile, e := BillingPreset("superwagie", "hybrid")
	if e != nil {
		t.Fatal(e)
	}
	profile.Usage.RequireSubscription = false
	profile, e = f.s.SaveProfile(ctx, f.actor, "superwagie", profile)
	if e != nil {
		t.Fatal(e)
	}
	offer := Offer{ID: "replay-addon", ProductID: "superwagie", Name: "Add-on", AccountKind: "both", Kind: "addon", Price: "9800000", MonthlyCredit: "200000000", ValidityMonths: 12}
	if _, e = f.s.SaveOffer(ctx, f.actor, offer); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishOffer(ctx, f.actor, offer.ID); e != nil {
		t.Fatal(e)
	}
	request := CheckoutRequest{OfferID: offer.ID, Provider: "wechat", Key: "accepted-before-policy"}
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, request)
	if e != nil {
		t.Fatal(e)
	}
	profile.Usage.RequireSubscription = true
	if _, e = f.s.SaveProfile(ctx, f.actor, "superwagie", profile); e != nil {
		t.Fatal(e)
	}
	f.s.now = func() time.Time { return x.ExpiresAt.Add(time.Hour) }
	f.s.ConfigurePayments(nil)
	replay, e := f.s.CreateCheckout(ctx, f.actor, f.account, request)
	if e != nil || replay.ID != x.ID || len(p.requests) != 1 {
		t.Fatal("accepted replay entered fresh admission", replay, e, len(p.requests))
	}
}
