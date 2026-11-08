package billing

import (
	"context"
	"errors"
	"testing"
	"time"
	"unihub/internal/payments"
)

func TestDynamicProviderReadFailureAndOriginalBoundOperations(t *testing.T) {
	errManagedRead := errors.New("configuration unavailable")
	f, p := checkoutFixture(t)
	ctx := context.Background()
	r := CheckoutRequest{OfferID: "sandbox-sw-month-1", Provider: "wechat", Key: "managed-original"}
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, r)
	if e != nil {
		t.Fatal(e)
	}
	f.s.ConfigurePaymentSource(func(context.Context) ([]payments.Provider, []payments.Provider, error) {
		return nil, []payments.Provider{p}, nil
	})
	p.result = payments.Result{OrderID: x.ID, Status: "PENDING", Amount: x.Amount, Currency: "CNY"}
	if _, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID); e != nil {
		t.Fatal("disabled original query", e)
	}
	if _, e = f.s.CloseCheckout(ctx, f.actor, f.account, x.ID); e != nil {
		t.Fatal("disabled original close", e)
	}
	f.s.ConfigurePaymentSource(func(context.Context) ([]payments.Provider, []payments.Provider, error) {
		return nil, nil, errManagedRead
	})
	if _, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID); !errors.Is(e, errManagedRead) {
		t.Fatal("stale provider used", e)
	}
	if _, e = f.s.PaymentProvidersContext(ctx); !errors.Is(e, errManagedRead) {
		t.Fatal("empty success", e)
	}
}

func TestDynamicProviderDisabledOriginalRefund(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := context.Background()
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: "sandbox-addon", Provider: "wechat", Key: "original-refund"})
	if e != nil {
		t.Fatal(e)
	}
	f.s.ConfigurePaymentSource(func(context.Context) ([]payments.Provider, []payments.Provider, error) {
		return nil, []payments.Provider{p}, nil
	})
	f.s.now = func() time.Time { return x.ExpiresAt.Add(time.Minute) }
	p.result = payments.Result{OrderID: x.ID, TransactionID: "original-late-paid", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	if e != nil || x.PaymentStatus != "REVIEW" {
		t.Fatal(x, e)
	}
	x, e = f.s.RefundCheckout(ctx, f.actor, f.account, x.ID, "original-refund-request", "late payment")
	if e != nil || x.RefundStatus != "SUCCEEDED" || p.refunds != 1 {
		t.Fatal("original disabled refund", x, e)
	}
}
