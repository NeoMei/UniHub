package billing

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"unihub/internal/payments"

	"github.com/jackc/pgx/v5/pgxpool"
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

type checkoutCloseProvider struct {
	*checkoutProvider
	closeErr error
}

func (p *checkoutCloseProvider) Close(context.Context, string) error { return p.closeErr }

func TestAlipayCloseRequiresVerifiedTerminalEvidence(t *testing.T) {
	merchant, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		closeBody   string
		unsigned    bool
		queryFailed bool
		paid        bool
		want        string
	}{
		{name: "signed close with missing query", closeBody: `{"code":"10000","out_trade_no":"switchcheckout"}`, want: "CLOSED"},
		{name: "signed close with failed query", closeBody: `{"code":"10000","out_trade_no":"switchcheckout"}`, queryFailed: true, want: "CLOSED"},
		{name: "merchant did not confirm close", closeBody: `{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}`, want: "UNKNOWN"},
		{name: "unverified close", closeBody: `{"code":"10000","out_trade_no":"switchcheckout"}`, unsigned: true, want: "UNKNOWN"},
		{name: "verified payment wins", closeBody: `{"code":"10000","out_trade_no":"switchcheckout"}`, paid: true, want: "PAID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := nativeFixture(t)
			ctx := context.Background()
			now := time.Date(2030, 4, 12, 11, 30, 0, 0, time.UTC)
			f.s.now = func() time.Time { return now }
			var queries, closes int
			provider, err := payments.NewAlipay(payments.AlipayConfig{
				AppID: "fixture-app", SellerID: "fixture-seller", NotifyURL: "https://merchant.example/notify",
				ReturnURL:  "https://merchant.example/return",
				PrivateKey: merchant, PublicKey: &platform.PublicKey,
				HTTPClient: &http.Client{Transport: commerceRoundTrip(func(r *http.Request) (*http.Response, error) {
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					method := r.Form.Get("method")
					var body string
					signatureOK := true
					switch method {
					case "alipay.trade.query":
						queries++
						if queries > 1 && tc.queryFailed {
							return nil, errors.New("fixture follow-up transport failure")
						}
						body = `{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}`
						if queries > 1 && tc.paid {
							body = `{"code":"10000","out_trade_no":"switchcheckout","trade_no":"fixture-paid","trade_status":"TRADE_SUCCESS","total_amount":"1.00","send_pay_date":"2030-04-12 19:30:00"}`
						}
					case "alipay.trade.close":
						closes++
						body, signatureOK = tc.closeBody, !tc.unsigned
					default:
						t.Fatalf("unexpected merchant operation: %s", method)
					}
					envelope := map[string]any{strings.ReplaceAll(method, ".", "_") + "_response": json.RawMessage(body)}
					if signatureOK {
						digest := sha256.Sum256([]byte(body))
						signature, err := rsa.SignPKCS1v15(rand.Reader, platform, crypto.SHA256, digest[:])
						if err != nil {
							t.Fatal(err)
						}
						envelope["sign"] = base64.StdEncoding.EncodeToString(signature)
					}
					payload, err := json.Marshal(envelope)
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			f.s.ConfigurePayments([]payments.Provider{provider})
			x := checkoutRecord{
				Checkout: Checkout{ID: "switchcheckout", AccountID: f.account, ProductID: "superwagie", Provider: "alipay",
					OfferName: "Fixture add-on", Amount: "1000000", Currency: "CNY", PaymentStatus: "PENDING", FulfillmentStatus: "NOT_STARTED",
					CreatedAt: now, ExpiresAt: now.Add(time.Minute)},
				ActorID: f.actor, MerchantID: provider.MerchantID(), MerchantVersion: paymentConfigurationID(provider), RequestDigest: "switch-fixture",
				Order: Order{ID: "fixture-order", AccountID: f.account, ActorID: f.actor, ProductID: "superwagie", Kind: "addon",
					Offer: Offer{ID: "fixture-addon", ProductID: "superwagie", Kind: "addon", ValidityMonths: 1}, Amount: "1000000",
					PaymentSource: "external", CheckoutID: "switchcheckout", ProductFunding: "0", UnifiedFunding: "0",
					GrantAmount: "100000000", CreatedAt: now, Start: now, End: now.AddDate(0, 1, 0), PeriodStart: now, PeriodEnd: now.AddDate(0, 1, 0)},
			}
			x.setPaymentSession(&payments.Session{Kind: "qr", URL: "https://qr.alipay.com/fixture", ExpiresAt: x.ExpiresAt})
			if _, err := f.s.pool.Exec(ctx, `INSERT INTO billing_checkouts(id,account_id,product_id,request_key,request_digest,snapshot) VALUES($1,$2,'superwagie',$1,$1,$3)`, x.ID, f.account, encode(x)); err != nil {
				t.Fatal(err)
			}
			got, err := f.s.CloseCheckout(ctx, f.actor, f.account, x.ID)
			if err != nil || got.PaymentStatus != tc.want || (got.Session != nil) != (tc.want == "UNKNOWN") {
				t.Fatalf("merchant close = %s with session=%t, error %v; want %s", got.PaymentStatus, got.Session != nil, err, tc.want)
			}
			if queries != 2 || closes != 1 {
				t.Fatalf("unexpected merchant reads and closes: %d, %d", queries, closes)
			}
			var hasReceipt bool
			if err := f.s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_payment_receipts WHERE checkout_id=$1)`, x.ID).Scan(&hasReceipt); err != nil || hasReceipt != tc.paid {
				t.Fatalf("durable receipt = %t, error %v", hasReceipt, err)
			}
			hold := f.s.locked(ctx, f.account, func(c *pgxpool.Conn) error { return f.s.commercialClear(ctx, c, f.account) })
			if (tc.want == "UNKNOWN" && !errors.Is(hold, ErrPending)) || (tc.want != "UNKNOWN" && hold != nil) {
				t.Fatalf("unexpected commercial hold for %s: %v", tc.want, hold)
			}
		})
	}
}

func TestExpiredCheckoutRecoveryRequiresMerchantEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expiresIn time.Duration
		closeErr  error
		status    string
	}{
		{name: "confirmed close of expired order", expiresIn: -time.Hour, status: "CLOSED"},
		{name: "lost close response", expiresIn: -time.Hour, closeErr: errors.New("close unavailable"), status: "UNKNOWN"},
		{name: "confirmed close of active order", expiresIn: time.Hour, status: "CLOSED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := nativeFixture(t)
			ctx := context.Background()
			p := &checkoutCloseProvider{checkoutProvider: &checkoutProvider{queryErr: errors.New("query unavailable")}, closeErr: tc.closeErr}
			f.s.ConfigurePayments([]payments.Provider{p})
			x := checkoutRecord{Checkout: Checkout{ID: "restorecheckout", AccountID: f.account, ProductID: "superwagie", Provider: "wechat", PaymentStatus: "UNKNOWN", FulfillmentStatus: "NOT_STARTED", CreatedAt: f.s.now().Add(-8 * 24 * time.Hour), ExpiresAt: f.s.now().Add(tc.expiresIn)}, MerchantID: p.MerchantID(), MerchantVersion: p.ConfigurationID(), CloseRequested: true}
			if _, err := f.s.pool.Exec(ctx, `INSERT INTO billing_checkouts(id,account_id,product_id,request_key,request_digest,snapshot) VALUES($1,$2,'superwagie',$1,$1,$3)`, x.ID, f.account, encode(x)); err != nil {
				t.Fatal(err)
			}
			if err := f.s.RecoverCheckouts(ctx); err != nil {
				t.Fatal(err)
			}
			rows, err := f.s.ListCheckouts(ctx, f.actor, f.account)
			if err != nil || len(rows) != 1 || rows[0].PaymentStatus != tc.status {
				t.Fatalf("listed checkouts: %+v, %v; want %s", rows, err, tc.status)
			}
			err = f.s.locked(ctx, f.account, func(c *pgxpool.Conn) error { return f.s.commercialClear(ctx, c, f.account) })
			if (tc.status == "CLOSED" && err != nil) || (tc.status != "CLOSED" && !errors.Is(err, ErrPending)) {
				t.Fatalf("commercial hold for %s: %v", tc.status, err)
			}
		})
	}
}
