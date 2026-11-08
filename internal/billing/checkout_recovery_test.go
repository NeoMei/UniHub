package billing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"unihub/internal/payments"
)

// A replaced subscription must not leave an old refund freezing all usage.
func TestExternalCheckoutRefundReplacedSubscriptionNeedsReview(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-sw-month-1", Provider: "wechat", Key: "old-member"})
	if e != nil {
		t.Fatal(e)
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "old-member-paid", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil {
		t.Fatal(e)
	}
	// saveSubscription replaces the prior ID when a later membership takes over.
	if _, e = f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET id='replacement', snapshot=jsonb_set(snapshot,'{id}','"replacement"') WHERE account_id=$1`, f.account); e != nil {
		t.Fatal(e)
	}
	x, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "old-member-refund", "unused")
	if e != nil || x.RefundStatus != "REVIEW" || x.ErrorCode != "subscription_changed" {
		t.Fatalf("replaced membership refund must reach review: %+v %v", x, e)
	}
	var status string
	if e = f.s.pool.QueryRow(ctx, `SELECT snapshot->>'refund_status' FROM billing_orders WHERE id=$1`, x.OrderID).Scan(&status); e != nil || status != "MANUAL_REVIEW" {
		t.Fatalf("order remains blocking: %s %v", status, e)
	}
	if p.refunds != 0 {
		t.Fatal("changed subscription sent a provider refund")
	}
}

type recoveryTrackingProvider struct {
	*checkoutProvider
	queries map[string]int
}

func (p *recoveryTrackingProvider) Query(_ context.Context, id string) (payments.Result, error) {
	p.queries[id]++
	return payments.Result{}, errors.New("provider unavailable")
}

// An unavailable prefix must not monopolize the bounded worker batch forever.
func TestExternalCheckoutRecoveryRotatesUnsuccessfulPrefix(t *testing.T) {
	f, provider := checkoutFixture(t)
	p := &recoveryTrackingProvider{checkoutProvider: provider, queries: map[string]int{}}
	f.s.ConfigurePayments([]payments.Provider{p})
	ctx := context.Background()
	for i := 0; i < 201; i++ {
		key := fmt.Sprintf("recovery%03d", i)
		x := checkoutRecord{Checkout: Checkout{ID: key, AccountID: f.account, ProductID: "superwagie", Provider: "wechat", PaymentStatus: "PENDING", FulfillmentStatus: "NOT_STARTED", CreatedAt: f.s.now().Add(time.Duration(i) * time.Second), ExpiresAt: f.s.now().Add(time.Hour)}, MerchantID: p.MerchantID(), MerchantVersion: p.ConfigurationID()}
		if _, e := f.s.pool.Exec(ctx, `INSERT INTO billing_checkouts(id,account_id,product_id,request_key,request_digest,snapshot) VALUES($1,$2,'superwagie',$1,$1,$3)`, key, f.account, encode(x)); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.s.RecoverCheckouts(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.s.RecoverCheckouts(ctx); e != nil {
		t.Fatal(e)
	}
	if p.queries["recovery200"] == 0 {
		t.Fatal("newer checkout starved behind the first 200 unavailable orders")
	}
}

// Terminal review is an operator task, not an endlessly eligible worker job.
func TestExternalCheckoutRecoverySkipsTerminalReviews(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "terminal-review"})
	if e != nil {
		t.Fatal(e)
	}
	f.s.now = func() time.Time { return x.ExpiresAt.Add(time.Minute) }
	p.result = payments.Result{OrderID: x.ID, TransactionID: "terminal-review-paid", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.PaymentStatus != "REVIEW" {
		t.Fatalf("%+v %v", x, e)
	}
	// Remove settled command evidence only in the fixture. A finished review must
	// not call settleCheckout again (which requires the receipt command row).
	if _, e = f.s.pool.Exec(ctx, `DELETE FROM billing_commands WHERE kind='external_payment' AND request_key=$1 AND status='APPLIED'`, x.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.s.RecoverCheckouts(ctx); e != nil {
		t.Fatalf("terminal review was retried: %v", e)
	}
}

func TestExternalCheckoutReplacedUpgradeKeepsPaymentReceipt(t *testing.T) {
	for _, delayedLedger := range []bool{false, true} {
		t.Run(fmt.Sprintf("delayed-ledger-%v", delayedLedger), func(t *testing.T) {
			f, p := checkoutFixture(t)
			f.auth(t)
			ctx := context.Background()
			original := f.buy(t, "sandbox-sw-month-1", "initial-member")
			q, e := f.s.QuoteExternalUpgrade(ctx, f.actor, f.account, original.SubscriptionID, "sandbox-sw-month-2")
			if e != nil {
				t.Fatal(e)
			}
			x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{QuoteID: q.ID, Provider: "wechat", Key: "old-upgrade"})
			if e != nil {
				t.Fatal(e)
			}
			p.result = payments.Result{OrderID: x.ID, TransactionID: "old-upgrade-paid", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
			bad := &checkoutFailLedger{Ledger: f.l, fail: delayedLedger}
			f.s.ledger = bad
			if delayedLedger {
				if e = f.s.NotifyCheckout(ctx, "wechat", nil, nil); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET id='replacement',snapshot=jsonb_set(snapshot,'{id}','"replacement"') WHERE id=$1`, original.SubscriptionID); e != nil {
				t.Fatal(e)
			}
			bad.fail = false
			x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
			if e != nil || x.PaymentStatus != "REVIEW" || x.ErrorCode != "quote_invalidated" {
				t.Fatalf("verified payment lost on replaced upgrade %+v %v", x, e)
			}
			var receipts, batches int
			if e = f.s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM billing_payment_receipts WHERE checkout_id=$1),(SELECT count(*) FROM billing_batches WHERE order_id=$2)`, x.ID, x.OrderID).Scan(&receipts, &batches); e != nil || receipts != 1 || batches != 0 {
				t.Fatalf("receipt/grant %d/%d %v", receipts, batches, e)
			}
		})
	}
}
