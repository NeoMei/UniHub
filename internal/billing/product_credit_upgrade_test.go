package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"unihub/internal/payments"
	"unihub/internal/wallet"
)

func publishUpgradeOffer(t *testing.T, f fixture, previous Offer) Offer {
	t.Helper()
	next := previous
	next.ID, next.Name, next.Rank = previous.ID+"-higher", "Higher membership", previous.Rank+1
	next.UpgradeRule = "remaining_term_prorata_v1"
	next.Price, next.ServiceAmount, next.ComputeAmount, next.MonthlyCredit = "6000000", "3000000", "3000000", "300000000"
	if previous.Period == "year" {
		next.Price, next.ServiceAmount, next.ComputeAmount = "72000000", "36000000", "36000000"
	}
	_, e := f.s.SaveOffer(t.Context(), f.actor, next)
	require.NoError(t, e)
	next, e = f.s.PublishOffer(t.Context(), f.actor, next.ID)
	require.NoError(t, e)
	return next
}

func TestProductCreditUpgradeStartsNewCycleAndReturnsOriginalWallet(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	base, addon := publishProductCreditOffers(t, f, "month")
	next := publishUpgradeOffer(t, f, base)
	f.s.now = func() time.Time { return dt("2024-01-31T12:00:00.123456789Z") }
	original := f.buy(t, base.ID, "upgrade-original")
	pack := f.buy(t, addon.ID, "upgrade-add-on")
	cutover := dt("2024-02-15T00:00:00Z")
	f.s.now = func() time.Time { return cutover }
	q, e := f.s.QuoteUpgrade(ctx, f.actor, f.account, original.SubscriptionID, next.ID)
	require.NoError(t, e)
	require.Equal(t, next.Price, q.Amount)
	require.Equal(t, next.MonthlyCredit, q.GrantAmount)
	require.Equal(t, "2000000", q.ProductCreditUpgrade.EstimatedRefundAmount)
	require.Equal(t, "wallet", q.ProductCreditUpgrade.RefundPaymentSource)
	upgraded, e := f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, "wallet-upgrade")
	require.NoError(t, e)
	require.NotEqual(t, original.SubscriptionID, upgraded.SubscriptionID)
	require.True(t, upgraded.Start.Equal(cutover))
	require.True(t, upgraded.End.Equal(dt("2024-03-15T00:00:00Z")))
	again, e := f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, "wallet-upgrade")
	require.NoError(t, e)
	require.Equal(t, upgraded.ID, again.ID)
	_, e = f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, "different-wallet-upgrade")
	require.Error(t, e)

	for range 2 {
		require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
	}
	old, e := f.s.GetOrder(ctx, f.actor, f.account, original.ID)
	require.NoError(t, e)
	require.Equal(t, "UPGRADE_RETURNED", old.RefundStatus)
	require.Equal(t, upgraded.ID, old.UpgradeReturn.ReplacementOrderID)
	require.Equal(t, "2000000", old.UpgradeReturn.Amount)
	cashBalance(t, f, wallet.Address(f.account, ""), 991000000)
	cashBalance(t, f, productComputePrepay(original), 0)
	cashBalance(t, f, productComputePrepay(upgraded), 3000000)
	w, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	require.NoError(t, e)
	require.Len(t, w.Batches, 3)
	require.Equal(t, "500000000", w.Available)
	for _, b := range w.Batches {
		if b.OrderID == pack.ID {
			require.True(t, b.ExpiresAt.Equal(pack.End))
			require.False(t, b.Frozen)
		}
		if b.OrderID == original.ID {
			require.Equal(t, base.MonthlyCredit, b.Withdrawn)
			require.True(t, b.Frozen)
		}
	}
	_, e = f.s.RequestRefund(ctx, f.actor, f.account, original.ID, "attempt-second-refund")
	require.ErrorIs(t, e, ErrConflict)
}

func TestProductCreditUpgradeReturnsAnnualBalanceViaOriginalMerchant(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := t.Context()
	base, _ := publishProductCreditOffers(t, f, "year")
	next := publishUpgradeOffer(t, f, base)
	original := payUpgradeTestCheckout(t, f, p, CheckoutRequest{OfferID: base.ID, Provider: "wechat", Key: "original-annual"}, "fixture-old-receipt")
	cutover := dt("2024-02-15T00:00:00Z")
	f.s.now = func() time.Time { return cutover }
	q, e := f.s.QuoteExternalUpgrade(ctx, f.actor, f.account, original.SubscriptionID, next.ID)
	require.NoError(t, e)
	require.Equal(t, next.Price, q.Amount)
	upgraded := payUpgradeTestCheckout(t, f, p, CheckoutRequest{QuoteID: q.ID, Provider: "wechat", Key: "external-upgrade"}, "fixture-new-receipt")
	require.True(t, upgraded.End.Equal(dt("2025-02-15T00:00:00Z")))
	require.True(t, upgraded.PeriodEnd.Equal(dt("2024-03-15T00:00:00Z")))
	for range 2 {
		require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
	}
	old, e := f.s.GetOrder(ctx, f.actor, f.account, original.ID)
	require.NoError(t, e)
	require.Equal(t, "UPGRADE_RETURNED", old.RefundStatus)
	require.Equal(t, 1, p.refunds)
	require.Equal(t, "fixture-old-receipt", p.refund.TransactionID)
	require.Equal(t, q.ProductCreditUpgrade.EstimatedRefundAmount, p.refund.Amount)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000000000)
	nextPeriod := MonthAnchor(cutover, 1)
	f.s.now = func() time.Time { return nextPeriod }
	_, e = f.s.Maintain(ctx, nextPeriod)
	require.NoError(t, e)
	view, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	require.NoError(t, e)
	require.Equal(t, next.MonthlyCredit, view.Available)
	count := 0
	for _, b := range view.Batches {
		if b.OrderID == upgraded.ID {
			count++
			if b.StartsAt.Before(nextPeriod) {
				require.Equal(t, next.MonthlyCredit, b.Expired)
			}
		}
	}
	require.Equal(t, 2, count)
}

func TestProductCreditUpgradeHonorsTimelyAnnualPaymentObservedAfterMonthlyBoundary(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := t.Context()
	base, _ := publishProductCreditOffers(t, f, "year")
	next := publishUpgradeOffer(t, f, base)
	previous := payUpgradeTestCheckout(t, f, p, CheckoutRequest{OfferID: base.ID, Provider: "wechat", Key: "prior-annual"}, "old-annual-paid")
	boundary := dt("2024-02-29T12:00:00Z")
	f.s.now = func() time.Time { return boundary.Add(-5 * time.Minute) }
	q, e := f.s.QuoteExternalUpgrade(ctx, f.actor, f.account, previous.SubscriptionID, next.ID)
	require.NoError(t, e)
	checkout, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{QuoteID: q.ID, Provider: "wechat", Key: "annual-timely"})
	require.NoError(t, e)
	paidAt := boundary.Add(-time.Minute)
	f.s.now = func() time.Time { return boundary.Add(time.Minute) }
	_, e = f.s.Maintain(ctx, f.s.now())
	require.NoError(t, e)
	p.result = payments.Result{OrderID: checkout.ID, TransactionID: "new-annual-timely", Amount: checkout.Amount, Currency: "CNY", Status: "PAID", PaidAt: paidAt}
	checkout, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, checkout.ID)
	require.NoError(t, e)
	require.Equal(t, "FULFILLED", checkout.FulfillmentStatus)
	upgrade, e := f.s.GetOrder(ctx, f.actor, f.account, checkout.OrderID)
	require.NoError(t, e)
	require.True(t, upgrade.Start.Equal(paidAt))
	require.True(t, upgrade.End.Equal(dt("2025-02-28T11:59:00Z")))
	v, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	require.NoError(t, e)
	require.Equal(t, previous.ID, v.Batches[0].OrderID)
	require.Equal(t, "100000000", v.Batches[0].Withdrawn)
	require.Equal(t, "0", v.Batches[0].Expired)
}

func TestProductCreditUpgradeAcceptsMerchantPaymentTimeWithSecondPrecision(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := t.Context()
	base, _ := publishProductCreditOffers(t, f, "month")
	next := publishUpgradeOffer(t, f, base)
	now := dt("2024-01-31T12:00:00.123456Z")
	f.s.now = func() time.Time { return now }
	previous := f.buy(t, base.ID, "original-before-merchant-rounding")
	now = dt("2024-01-31T12:00:00.567890Z")
	q, e := f.s.QuoteExternalUpgrade(ctx, f.actor, f.account, previous.SubscriptionID, next.ID)
	require.NoError(t, e)
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{QuoteID: q.ID, Provider: "wechat", Key: "scoped-upgrade-second-precision"})
	require.NoError(t, e)
	paidAt := dt("2024-01-31T12:00:00Z")
	p.result = payments.Result{OrderID: x.ID, TransactionID: "external-upgrade-second-precision", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: paidAt}
	now = dt("2024-01-31T12:00:01Z")
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	require.NoError(t, e)
	require.Equal(t, "FULFILLED", x.FulfillmentStatus)
	upgraded, e := f.s.GetOrder(ctx, f.actor, f.account, x.OrderID)
	require.NoError(t, e)
	require.True(t, upgraded.Start.Equal(dt("2024-01-31T12:00:00.567890Z")))
	record, e := readCheckout(ctx, f.s.pool, f.account, x.ID)
	require.NoError(t, e)
	require.NotNil(t, record.Receipt)
	require.True(t, record.Receipt.PaidAt.Equal(paidAt))
}

func TestProductUpgradeRefundWaitsForHoldsInAnAlreadyExpiredGrant(t *testing.T) {
	upgrade := dt("2024-03-15T00:00:00Z")
	expired := Batch{ExpiresAt: dt("2024-02-29T12:00:00Z"), Reserved: "5000000", Consumed: "0"}
	order := Order{Amount: "36000000", PaymentSource: "wallet"}
	_, _, _, e := upgradeReturnAmounts(order, []Batch{expired}, upgrade, 100000000, 1000000, false)
	require.ErrorIs(t, e, ErrPending)
	expired.Reserved = "0"
	expired.Consumed = "5000000"
	_, _, remaining, e := upgradeReturnAmounts(order, []Batch{expired}, upgrade, 100000000, 1000000, false)
	require.NoError(t, e)
	require.EqualValues(t, 2000000, remaining)
}

func TestProductCreditUpgradeWaitsForAnOriginalHoldBeforeRefund(t *testing.T) {
	f, credentialID, _ := nativeCreditFixture(t)
	ctx := t.Context()
	base, addon := publishProductCreditOffers(t, f, "month")
	next := publishUpgradeOffer(t, f, base)
	_, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET credit_scope='product_v1',max_per_call='1000000000',max_total='1000000000' WHERE id=$1`, credentialID)
	require.NoError(t, e)
	_, e = f.s.pool.Exec(ctx, `DELETE FROM billing_subscriptions WHERE account_id=$1`, f.account)
	require.NoError(t, e)
	f.s.now = func() time.Time { return dt("2024-01-31T12:00:00Z") }
	fundCash(t, f, "", 12000000)
	original := f.buy(t, base.ID, "retained-original")
	f.s.now = func() time.Time { return dt("2024-02-02T12:00:00Z") }
	f.buy(t, addon.ID, "retained-pack")
	r := creditNativeRequest(f, credentialID, "in-flight-upgrade", "5000000", exactProductCreditTestConversion())
	r.CreditScope, r.SourceVerified = ProductCreditScopeV1, true
	before, e := f.s.ReserveNative(ctx, "llmrouter", r)
	require.NoError(t, e)
	require.Len(t, before.Allocations, 1)
	cutover := dt("2024-02-15T00:00:00Z")
	f.s.now = func() time.Time { return cutover }
	q, e := f.s.QuoteUpgrade(ctx, f.actor, f.account, original.SubscriptionID, next.ID)
	require.NoError(t, e)
	require.Equal(t, "1950000", q.ProductCreditUpgrade.EstimatedRefundAmount)
	_, e = f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, "switch-in-flight")
	require.NoError(t, e)
	require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
	old, e := f.s.GetOrder(ctx, f.actor, f.account, original.ID)
	require.NoError(t, e)
	require.Equal(t, "UPGRADE_PENDING", old.RefundStatus)
	view, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	require.NoError(t, e)
	require.Equal(t, "500000000", view.Available)
	require.Equal(t, "5000000", view.Reserved)
	_, e = f.s.TopUp(ctx, "llmrouter", r.CallID, "301000000", "above-new-subscription-grant")
	require.ErrorIs(t, e, ErrInsufficientCredits)
	held, e := f.s.TopUp(ctx, "llmrouter", r.CallID, "1000000", "difference-on-new-subscription")
	require.NoError(t, e)
	require.Len(t, held.Allocations, 2)
	require.Equal(t, before.Allocations[0], held.Allocations[0])
	newBatch, e := load[Batch](ctx, f.s.pool, `SELECT snapshot FROM billing_batches WHERE id=$1`, held.Allocations[1].BatchID)
	require.NoError(t, e)
	require.NotEqual(t, original.SubscriptionID, newBatch.SubscriptionID)
	require.Equal(t, "1000000", held.Allocations[1].Amount)
	require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
	old, e = f.s.GetOrder(ctx, f.actor, f.account, original.ID)
	require.NoError(t, e)
	require.Equal(t, "UPGRADE_PENDING", old.RefundStatus)
	cutover = dt("2024-03-01T00:00:00Z")
	after, e := f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{
		CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "6000000", Amount: "6000000",
	})
	require.NoError(t, e)
	require.Equal(t, held.Allocations, after.Allocations)
	require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
	old, e = f.s.GetOrder(ctx, f.actor, f.account, original.ID)
	require.NoError(t, e)
	require.Equal(t, "UPGRADE_RETURNED", old.RefundStatus)
	require.Equal(t, "1950000", old.UpgradeReturn.Amount)
	view, e = f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	require.NoError(t, e)
	require.Equal(t, "499000000", view.Available)
	cashBalance(t, f, productComputePrepay(original), 0)
}

func TestProductAddonExpiresFromPaymentInBeijingCalendar(t *testing.T) {
	f, p := checkoutFixture(t)
	ctx := t.Context()
	base, addon := publishProductCreditOffers(t, f, "year")
	f.buy(t, base.ID, "addon-eligible-member")
	f.s.now = func() time.Time { return dt("2024-01-31T15:59:00Z") }
	x, e := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{OfferID: addon.ID, Provider: "wechat", Key: "addon-at-local-midnight"})
	require.NoError(t, e)
	paidAt := dt("2024-01-31T16:01:00Z") // Feb 1 at 00:01 in Beijing.
	f.s.now = func() time.Time { return paidAt }
	p.result = payments.Result{OrderID: x.ID, TransactionID: "scope-addon-payment", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: paidAt}
	x, e = f.s.ReconcileCheckout(ctx, f.actor, f.account, x.ID)
	require.NoError(t, e)
	require.Equal(t, "FULFILLED", x.FulfillmentStatus)
	o, e := f.s.GetOrder(ctx, f.actor, f.account, x.OrderID)
	require.NoError(t, e)
	require.True(t, o.Start.Equal(paidAt))
	require.True(t, o.End.Equal(dt("2024-02-29T16:01:00Z")))
}

func TestProductCreditUpgradeWithdrawsUnusedHoldAfterOriginalExpiry(t *testing.T) {
	f, credentialID, _ := nativeCreditFixture(t)
	ctx := t.Context()
	base, _ := publishProductCreditOffers(t, f, "month")
	next := publishUpgradeOffer(t, f, base)
	_, e := f.s.pool.Exec(ctx, `UPDATE model_credentials SET credit_scope='product_v1',max_per_call='1000000000',max_total='1000000000' WHERE id=$1`, credentialID)
	require.NoError(t, e)
	_, e = f.s.pool.Exec(ctx, `DELETE FROM billing_subscriptions WHERE account_id=$1`, f.account)
	require.NoError(t, e)
	f.s.now = func() time.Time { return dt("2024-01-31T12:00:00Z") }
	fundCash(t, f, "", 9000000)
	old := f.buy(t, base.ID, "original-for-late-release")
	r := creditNativeRequest(f, credentialID, "late-released-hold", "5000000", exactProductCreditTestConversion())
	r.CreditScope, r.SourceVerified = ProductCreditScopeV1, true
	held, e := f.s.ReserveNative(ctx, "llmrouter", r)
	require.NoError(t, e)
	require.Len(t, held.Allocations, 1)
	f.s.now = func() time.Time { return dt("2024-02-15T00:00:00Z") }
	q, e := f.s.QuoteUpgrade(ctx, f.actor, f.account, old.SubscriptionID, next.ID)
	require.NoError(t, e)
	_, e = f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, "switch-before-late-release")
	require.NoError(t, e)

	f.s.now = func() time.Time { return dt("2024-03-01T00:00:00Z") }
	_, e = f.s.SettleNative(ctx, "llmrouter", NativeSettlementRequest{
		CallID: r.CallID, PricingDigest: r.Quote.PricingDigest, NativeQuota: "3000000", Amount: "3000000",
	})
	require.NoError(t, e)
	require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
	w, e := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	require.NoError(t, e)
	require.Len(t, w.Batches, 2)
	retired := w.Batches[0]
	require.Equal(t, held.Allocations[0].BatchID, retired.ID)
	require.Equal(t, "3000000", retired.Consumed)
	require.Equal(t, "97000000", retired.Withdrawn)
	require.Equal(t, "0", retired.Reserved)
	require.Equal(t, "0", retired.Expired)
	old, e = f.s.GetOrder(ctx, f.actor, f.account, old.ID)
	require.NoError(t, e)
	require.Equal(t, "UPGRADE_RETURNED", old.RefundStatus)
	require.Equal(t, "970000", old.UpgradeReturn.ComputeAmount)
}

func payUpgradeTestCheckout(t *testing.T, f fixture, p *checkoutProvider, request CheckoutRequest, tx string) Order {
	t.Helper()
	ctx := context.Background()
	checkout, e := f.s.CreateCheckout(ctx, f.actor, f.account, request)
	require.NoError(t, e)
	p.result = payments.Result{OrderID: checkout.ID, TransactionID: tx, Amount: checkout.Amount, Currency: "CNY", Status: "PAID", PaidAt: f.s.now()}
	paid, e := f.s.ReconcileCheckout(ctx, f.actor, f.account, checkout.ID)
	require.NoError(t, e)
	require.Equal(t, "FULFILLED", paid.FulfillmentStatus)
	o, e := f.s.GetOrder(ctx, f.actor, f.account, paid.OrderID)
	require.NoError(t, e)
	return o
}
