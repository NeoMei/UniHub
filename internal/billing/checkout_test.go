package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"sync"
	"testing"
	"time"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/wallet"
)

type checkoutProvider struct {
	config        string
	onIO          func()
	refundQueries []payments.RefundResult
	mu            sync.Mutex
	requests      []payments.Request
	result        payments.Result
	queryErr      error
	createErr     error
	refunds       int
	refund        payments.RefundResult
}

func (p *checkoutProvider) ConfigurationID() string {
	if p.config != "" {
		return p.config
	}
	return "fixture-config-v1"
}
func (*checkoutProvider) ID() string         { return "wechat" }
func (*checkoutProvider) MerchantID() string { return "fixture-merchant" }
func (p *checkoutProvider) Create(_ context.Context, r payments.Request) (payments.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.onIO != nil {
		p.onIO()
	}
	p.requests = append(p.requests, r)
	return payments.Session{Kind: "qr", URL: "weixin://wxpay/bizpayurl?pr=fixture", ExpiresAt: r.ExpiresAt}, p.createErr
}
func (p *checkoutProvider) Query(context.Context, string) (payments.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.onIO != nil {
		p.onIO()
	}
	return p.result, p.queryErr
}
func (*checkoutProvider) Close(context.Context, string) error { return nil }
func (p *checkoutProvider) VerifyNotification(http.Header, []byte) (payments.Result, error) {
	return p.result, p.queryErr
}
func (p *checkoutProvider) Refund(_ context.Context, r payments.RefundRequest) (payments.RefundResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.onIO != nil {
		p.onIO()
	}
	p.refunds++
	p.refund = payments.RefundResult{RefundID: r.RefundID, TransactionID: r.TransactionID, Amount: r.Amount, Status: "SUCCEEDED"}
	return p.refund, nil
}
func (p *checkoutProvider) QueryRefund(context.Context, payments.RefundRequest) (payments.RefundResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.onIO != nil {
		p.onIO()
	}
	if len(p.refundQueries) > 0 {
		r := p.refundQueries[0]
		p.refundQueries = p.refundQueries[1:]
		return r, nil
	}
	if p.refund.Status == "" {
		return payments.RefundResult{Status: "NOT_FOUND"}, nil
	}
	return p.refund, nil
}
func checkoutFixture(t *testing.T) (fixture, *checkoutProvider) {
	t.Helper()
	f := setup(t)
	p := &checkoutProvider{}
	f.s.ConfigurePayments([]payments.Provider{p})
	return f, p
}
func TestExternalCheckoutReceiptReplayAndWalletPreservation(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	before, _ := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-sw-month-1", Provider: "wechat", Key: "external-first"})
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-sw-month-1", Provider: "wechat", Key: "external-first"})
	if e != nil || again.ID != x.ID || len(p.requests) != 1 {
		t.Fatalf("replay %+v %v creates=%d", again, e, len(p.requests))
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "provider-1", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.s.NotifyCheckout(ctx, "wechat", nil, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	x, e = f.s.GetCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("checkout %+v %v", x, e)
	}
	after, _ := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	if before != after {
		t.Fatalf("wallet changed %d -> %d", before, after)
	}
	var orders, batches, receipts int
	f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_orders`).Scan(&orders)
	f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_batches`).Scan(&batches)
	f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_payment_receipts`).Scan(&receipts)
	if orders != 1 || batches != 1 || receipts != 1 {
		t.Fatalf("counts %d/%d/%d", orders, batches, receipts)
	}
}
func TestExternalCheckoutUnknownPreservesIdentityAndCommercialLock(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	p.createErr = errors.New("lost create response")
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "lost"})
	if e != nil || x.PaymentStatus != "UNKNOWN" {
		t.Fatalf("%+v %v", x, e)
	}
	_, e = f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "second"})
	if !errors.Is(e, ErrPending) {
		t.Fatalf("new charge allowed: %v", e)
	}
	_, e = f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"sandbox-addon", "wallet"})
	if !errors.Is(e, ErrPending) {
		t.Fatalf("wallet purchase allowed %v", e)
	}
	var n int
	f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_commands`).Scan(&n)
	if n != 0 {
		t.Fatalf("unpaid checkout enqueued %d commands", n)
	}
	p.result = payments.Result{OrderID: x.ID, Status: "NOT_FOUND"}
	x, e = f.s.CloseCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.PaymentStatus == "CLOSED" {
		t.Fatalf("premature close %+v %v", x, e)
	}
	_, e = f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "lost"})
	if e != nil || len(p.requests) != 1 {
		t.Fatal("recreated uncertain payment", e)
	}
}
func TestExternalCheckoutLatePaidReviewAndOriginalRefund(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "late"})
	if e != nil {
		t.Fatal(e)
	}
	f.s.now = func() time.Time { return x.ExpiresAt.Add(time.Minute) }
	p.result = payments.Result{OrderID: x.ID, TransactionID: "late-paid", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.PaymentStatus != "REVIEW" || x.FulfillmentStatus != "REVIEW" {
		t.Fatalf("late %+v %v", x, e)
	}
	x, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "refund-late", "late payment")
	if e != nil || x.RefundStatus != "SUCCEEDED" {
		t.Fatalf("refund %+v %v", x, e)
	}
	x, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "refund-late", "late payment")
	if e != nil || p.refunds != 1 {
		t.Fatalf("duplicate refund count=%d %v", p.refunds, e)
	}
}

func TestExternalCheckoutReceiptBindingAndUniqueTransaction(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "one"})
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []payments.Result{{OrderID: x.ID, TransactionID: "txn", Amount: "1", Currency: "CNY", Status: "PAID"}, {OrderID: x.ID, TransactionID: "txn", Amount: x.Amount, Currency: "USD", Status: "PAID"}, {OrderID: x.ID, Amount: x.Amount, Currency: "CNY", Status: "PAID"}} {
		p.result = bad
		if e = f.s.NotifyCheckout(ctx, "wechat", nil, nil); e == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "same-txn", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	if e = f.s.NotifyCheckout(ctx, "wechat", nil, nil); e != nil {
		t.Fatal(e)
	}
	second, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "two"})
	if e != nil {
		t.Fatal(e)
	}
	p.result.OrderID = second.ID
	if e = f.s.NotifyCheckout(ctx, "wechat", nil, nil); e == nil {
		t.Fatal("same provider transaction applied to two orders")
	}
	var n int
	f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_payment_receipts`).Scan(&n)
	if n != 1 {
		t.Fatalf("receipts %d", n)
	}
}

func TestExternalCheckoutCentQuoteAndStaleUpgrade(t *testing.T) {
	f, p := checkoutFixture(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-month-1", "initial")
	f.s.now = func() time.Time { return dt("2024-02-03T12:00:01Z") }
	q, e := f.s.QuoteExternalUpgrade(ctx, f.actor, f.account, o.SubscriptionID, "sandbox-sw-month-2")
	if e != nil {
		t.Fatal(e)
	}
	amount, _ := integer(q.Amount, true)
	original, _ := integer(q.OriginalAmount, true)
	if q.RoundingRule != "cny_cent_ceil_v1" || amount%10000 != 0 || amount < original || amount-original >= 10000 {
		t.Fatalf("quote %+v", q)
	}
	if _, e = f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, "wallet-invalid"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("external quote accepted by wallet %v", e)
	}
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{QuoteID: q.ID, Provider: "wechat", Key: "upgrade"})
	if e != nil {
		t.Fatal(e)
	}
	// Simulate an external administrative subscription revision after checkout.
	if _, e = f.s.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{version}',to_jsonb((snapshot->>'version')::bigint+1)) WHERE id=$1`, o.SubscriptionID); e != nil {
		t.Fatal(e)
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "upgrade-txn", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.PaymentStatus != "REVIEW" {
		t.Fatalf("stale %+v %v", x, e)
	}
	var n int
	f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_batches WHERE order_id=$1`, x.OrderID).Scan(&n)
	if n != 0 {
		t.Fatal("stale quote granted credits")
	}
}

type checkoutFailLedger struct {
	ledger.Ledger
	fail bool
}

func (l *checkoutFailLedger) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	if l.fail {
		return ledger.Transaction{}, ledger.ErrUnknown
	}
	return l.Ledger.Post(ctx, c)
}
func TestExternalCheckoutRefundConfirmedBeforeLedgerRetry(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "refund"})
	if e != nil {
		t.Fatal(e)
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "refund-paid", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.RequestRefund(ctx, f.actor, f.account, x.OrderID, "legacy"); !errors.Is(e, ErrUnsupported) {
		t.Fatalf("legacy refund allowed %v", e)
	}
	bad := &checkoutFailLedger{Ledger: f.l, fail: true}
	f.s.ledger = bad
	x, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "refund-key", "unused")
	if !errors.Is(e, ErrPending) || x.RefundStatus != "PENDING" || p.refunds != 1 {
		t.Fatalf("premature refund %+v %v calls=%d", x, e, p.refunds)
	}
	saved, e := f.s.GetOrder(ctx, f.actor, f.account, x.OrderID)
	if e != nil || saved.RefundStatus == "REFUNDED" {
		t.Fatalf("premature REFUNDED %+v %v", saved, e)
	}
	bad.fail = false
	if e = f.s.RecoverCheckouts(ctx); e != nil {
		t.Fatal(e)
	}
	x, e = f.s.GetCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.RefundStatus != "SUCCEEDED" || p.refunds != 1 {
		t.Fatalf("recovery %+v %v calls=%d", x, e, p.refunds)
	}
}

func TestExternalCheckoutUnpaidDoesNotBlockUsageAndUsedRefundReview(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	f.auth(t)
	f.buy(t, "sandbox-sw-month-1", "wallet-credits")
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "pending"})
	if e != nil {
		t.Fatal(e)
	}
	r := Rate{ID: "checkout-rate", Model: "checkout-model", Semantics: "exclusive_v1", Input: Rational{"1", "1"}, Output: Rational{"0", "1"}, CacheRead: Rational{"0", "1"}, CacheWrite: Rational{"0", "1"}, Reasoning: Rational{"0", "1"}}
	if _, e = f.s.PublishRate(ctx, f.actor, r); e != nil {
		t.Fatal(e)
	}
	call, e := f.s.Reserve(ctx, "router", ReserveRequest{CallID: "while-checkout-pending", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "fixture", RateID: r.ID, Amount: "1"})
	if e != nil || call.Status != "RESERVED" {
		t.Fatalf("unpaid checkout blocked use %+v %v", call, e)
	}
	if _, e = f.s.Release(ctx, "router", call.CallID, "fixture"); e != nil {
		t.Fatal(e)
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "consumed", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil {
		t.Fatal(e)
	}
	bs, e := list[Batch](ctx, f.s.pool, `SELECT snapshot FROM billing_batches WHERE order_id=$1`, x.OrderID)
	if e != nil || len(bs) != 1 {
		t.Fatal(bs, e)
	}
	if _, e = f.l.Post(ctx, ledger.Command{Reference: "used-fixture", Postings: []ledger.Posting{{Source: BatchAddress(bs[0].ID, "available"), Destination: BatchAddress(bs[0].ID, "consumed"), Asset: CreditAsset, Amount: 1}}}); e != nil {
		t.Fatal(e)
	}
	x, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "used-refund", "unused requested")
	if e != nil || x.RefundStatus != "REVIEW" || p.refunds != 0 {
		t.Fatalf("used refunded %+v %v calls=%d", x, e, p.refunds)
	}
}

func TestExternalCheckoutClosedLateReceiptAndDelayedLedgerReview(t *testing.T) {
	for _, mode := range []string{"closed", "delayed-ledger"} {
		t.Run(mode, func(t *testing.T) {
			f, p := checkoutFixture(t)
			ctx := context.Background()
			x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: mode})
			if e != nil {
				t.Fatal(e)
			}
			if mode == "closed" {
				p.result = payments.Result{OrderID: x.ID, Status: "CLOSED"}
				x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
				if e != nil || x.PaymentStatus != "CLOSED" {
					t.Fatal(x, e)
				}
			}
			p.result = payments.Result{OrderID: x.ID, TransactionID: "late-" + mode, Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
			var bad *checkoutFailLedger
			if mode == "delayed-ledger" {
				bad = &checkoutFailLedger{Ledger: f.l, fail: true}
				f.s.ledger = bad
			}
			if e = f.s.NotifyCheckout(ctx, "wechat", nil, nil); e != nil {
				t.Fatal("verified durable receipt must ACK", e)
			}
			if bad != nil {
				bad.fail = false
				f.s.now = func() time.Time { return x.ExpiresAt.Add(time.Second) }
				if _, e = f.s.Recover(ctx); e != nil {
					t.Fatal(e)
				}
				if e = f.s.RecoverCheckouts(ctx); e != nil {
					t.Fatal(e)
				}
			}
			x, e = f.s.GetCheckout(ctx, f.actor, f.account, x.ID)
			if e != nil || x.PaymentStatus != "REVIEW" || x.FulfillmentStatus != "REVIEW" {
				t.Fatalf("late fulfilled %+v %v", x, e)
			}
			var count int
			f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_batches WHERE order_id=$1`, x.OrderID).Scan(&count)
			if count != 0 {
				t.Fatal("late receipt created benefits")
			}
		})
	}
}

func TestExternalCheckoutRefundAcceptanceAndTransactionBinding(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "refund-proof"})
	if e != nil {
		t.Fatal(e)
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "original-payment", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil {
		t.Fatal(e)
	}
	p.refundQueries = []payments.RefundResult{{Status: "NOT_FOUND"}, {Status: "PENDING"}}
	x, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "refund-proof-key", "unused")
	if e != nil || x.RefundStatus != "PENDING" {
		t.Fatalf("request acceptance was treated as success %+v %v", x, e)
	}
	wrong := p.refund
	wrong.TransactionID = "unrelated-payment"
	p.refundQueries = []payments.RefundResult{wrong}
	if _, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "refund-proof-key", "unused"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unbound refund proof accepted %v", e)
	}
	if e = f.s.RecoverCheckouts(ctx); e != nil {
		t.Fatal(e)
	}
	x, e = f.s.GetCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.RefundStatus != "SUCCEEDED" || p.refunds != 1 {
		t.Fatalf("refund recovery %+v %v", x, e)
	}
}
func TestExternalCheckoutMerchantApplicationBinding(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "config"})
	if e != nil {
		t.Fatal(e)
	}
	p.config = "different-application"
	p.result = payments.Result{OrderID: x.ID, TransactionID: "configuration-txn", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	if _, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID); !errors.Is(e, ErrPaymentUnavailable) {
		t.Fatalf("new app recovered old order %v", e)
	}
	if e = f.s.NotifyCheckout(ctx, "wechat", nil, nil); e == nil {
		t.Fatal("new app notification applied to old order")
	}
	p.config = "fixture-config-v1"
	if _, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID); e != nil {
		t.Fatal(e)
	}
}
func TestExternalCheckoutTaggedReadinessAndUnlockedProviderIO(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	o := f.offers[0]
	o.ID = "new-program"
	o.Sandbox = false
	o.Entitlements = map[string]string{"pricing_program": "superwagie-ai-points-v1"}
	if _, e := f.s.SaveOffer(ctx, f.actor, o); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.PublishOffer(ctx, f.actor, o.ID); e != nil {
		t.Fatal(e)
	}
	req := CheckoutRequest{OfferID: o.ID, Provider: "wechat", Key: "program"}
	if _, e := f.s.CreateCheckout(ctx, f.actor, f.account, req); !errors.Is(e, ErrCreditRatesUnavailable) {
		t.Fatalf("unconfigured policy allowed %v", e)
	}
	profile, _ := BillingPreset("superwagie", "hybrid")
	profile.NativeCreditConversion = &NativeConversion{Version: "checkout-test-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
	if _, e := f.s.SaveProfile(ctx, f.actor, "superwagie", profile); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.CreateCheckout(ctx, f.actor, f.account, req); !errors.Is(e, ErrCreditRatesUnavailable) {
		t.Fatalf("unverified runtime allowed %v", e)
	}
	verifyUnlocked := func() {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if e := f.s.locked(cctx, f.account, func(*pgxpool.Conn) error { return nil }); e != nil {
			t.Errorf("provider IO held account lock: %v", e)
		}
	}
	p.onIO = verifyUnlocked
	f.s.ConfigureCheckoutReadiness(func(context.Context, string, string, string) (NativeCreditReadiness, error) {
		verifyUnlocked()
		return NativeCreditReadiness{Ready: true}, nil
	})
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, req)
	if e != nil {
		t.Fatal(e)
	}
	f.s.ConfigureCheckoutReadiness(func(context.Context, string, string, string) (NativeCreditReadiness, error) {
		return NativeCreditReadiness{Ready: false, Reason: "runtime unavailable"}, nil
	})
	if _, e = f.s.CreateCheckout(ctx, f.actor, f.account, req); e != nil {
		t.Fatalf("accepted replay depends on new readiness %v", e)
	}
	p.result = payments.Result{OrderID: x.ID, TransactionID: "program-paid", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("accepted payment depends on new readiness %+v %v", x, e)
	}
	x, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "program-refund", "unused")
	if e != nil || x.RefundStatus != "SUCCEEDED" {
		t.Fatal(x, e)
	}
}
