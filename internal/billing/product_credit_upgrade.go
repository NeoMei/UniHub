package billing

import (
	"context"
	"errors"
	"math"
	"math/big"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/ledger"
)

func upgradeQuoteCurrent(q Quote, sub Subscription, now time.Time) bool {
	start, _, active := sub.Offer.currentPeriod(sub.Anchor, sub.PaidThrough, now)
	return sub.Status == "active" && active && now.Before(q.ExpiresAt) &&
		start.Equal(q.PeriodStart) && sub.Version == q.SubscriptionVersion
}

func setUpgradeOrderTerm(o *Order, quote Quote, sub Subscription) {
	if quote.ProductCreditUpgrade != nil {
		o.ProductCreditUpgrade = quote.ProductCreditUpgrade
		o.SubscriptionID = id("sub_")
		// Before fulfillment, these boundaries protect the original quote.
		o.End = sub.PaidThrough
		o.PeriodEnd = quote.PeriodEnd
	}
}

// A post-upgrade top-up may draw only the difference from the active replacement.
func replacementFundingSubscription(ctx context.Context, q dbq, call Call, now time.Time) (string, error) {
	sub, e := activeSubscriptionOn(ctx, q, call.AccountID, call.OriginProductID, now)
	if e != nil || sub.ID == call.FundingSubscriptionID {
		return "", e
	}
	var replaced bool
	e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_orders
		WHERE account_id=$1 AND snapshot->>'product_id'=$2 AND snapshot->>'subscription_id'=$3
		AND snapshot ? 'upgrade_return')`, call.AccountID, call.OriginProductID, call.FundingSubscriptionID).Scan(&replaced)
	if e != nil || !replaced {
		return "", e
	}
	return sub.ID, nil
}

func previousProductUpgradeOrder(ctx context.Context, q dbq, sub Subscription, expectedID string) (Order, error) {
	out, e := load[Order](ctx, q, `SELECT snapshot FROM billing_orders
		WHERE account_id=$1 AND snapshot->>'subscription_id'=$2 AND (snapshot->>'end')::timestamptz=$3::text::timestamptz
		AND snapshot->>'payment_status'='PAID' AND snapshot->>'fulfillment_status'='FULFILLED'
		ORDER BY (snapshot->>'created_at')::timestamptz DESC,id LIMIT 1`,
		sub.AccountID, sub.ID, sub.PaidThrough.Format(time.RFC3339Nano))
	if errors.Is(e, accounts.ErrNotFound) {
		return out, ErrQuoteInvalidated
	}
	if e != nil {
		return out, e
	}
	if (expectedID != "" && expectedID != out.ID) || !out.End.Equal(sub.PaidThrough) || out.ProductID != sub.ProductID || out.Offer.ID != sub.Offer.ID ||
		out.CreditScope != ProductCreditScopeV1 || out.UpgradeReturn != nil || refundBlocks(out.RefundStatus) ||
		(out.PaymentSource != "wallet" && out.PaymentSource != "external") ||
		(out.Kind != "membership" && out.Kind != "renewal" && (out.Kind != "upgrade" || out.ProductCreditUpgrade == nil)) {
		return out, ErrQuoteInvalidated
	}
	return out, nil
}

func (s *Service) quoteProductCreditUpgrade(ctx context.Context, q dbq, sub Subscription, offer Offer, now, start, end time.Time) (Quote, error) {
	old, e := previousProductUpgradeOrder(ctx, q, sub, "")
	if e != nil {
		return Quote{}, e
	}
	if old.PaymentSource == "external" {
		if _, e = originalUpgradeCheckout(ctx, q, old); e != nil {
			return Quote{}, e
		}
	}
	batches, e := s.productUpgradeBatches(ctx, q, old)
	if e != nil {
		return Quote{}, e
	}
	credit, service, e := upgradeReturnCeiling(old, sub, now)
	if e != nil {
		return Quote{}, e
	}
	_, _, refund, e := upgradeReturnAmounts(old, batches, now, credit, service, true)
	if e != nil {
		return Quote{}, e
	}
	return Quote{
		ID: id("quote_"), AccountID: sub.AccountID, SubscriptionID: sub.ID, SubscriptionVersion: sub.Version,
		Offer: offer, Amount: offer.Price, GrantAmount: offer.MonthlyCredit, PeriodStart: start,
		PeriodEnd: end, ExpiresAt: end, CreatedAt: now,
		ProductCreditUpgrade: &ProductCreditUpgrade{
			PreviousOrderID: old.ID, PreviousSubscriptionID: sub.ID, PreviousVersion: sub.Version,
			EstimatedRefundAmount: str(refund), RefundPaymentSource: old.PaymentSource,
		},
	}, nil
}

// Monthly grants already expired at cutover have no refundable value. The
// current grant (even if the worker has not issued it yet) and each future
// period derive from the immutable order and original subscription anchor.
func upgradeReturnCeiling(o Order, sub Subscription, cutover time.Time) (credit, service int64, e error) {
	start, _, active := sub.Offer.currentPeriod(sub.Anchor, o.End, cutover)
	if !active || o.Start.After(cutover) {
		return 0, 0, ErrQuoteInvalidated
	}
	loc := sub.Offer.billingLocation()
	periods := monthsBetween(start.In(loc), o.End.In(loc))
	compute, e := integer(o.ComputeAmount, true)
	if e != nil || compute > math.MaxInt64/productCreditsPerCNY || periods < 1 || periods > 12 {
		return 0, 0, ErrConflict
	}
	monthly, e := integer(o.Offer.MonthlyCredit, true)
	if e != nil || monthly > compute*productCreditsPerCNY/int64(periods) {
		return 0, 0, ErrConflict
	}
	originalService, e := integer(o.ServiceAmount, false)
	if e != nil {
		return 0, 0, e
	}
	term := o.End.Sub(o.Start)
	if term <= 0 {
		return 0, 0, ErrConflict
	}
	serviceN := new(big.Int).Mul(big.NewInt(originalService), big.NewInt(int64(o.End.Sub(cutover))))
	serviceN.Quo(serviceN, big.NewInt(int64(term)))
	return monthly * int64(periods), serviceN.Int64(), nil
}

func (s *Service) productUpgradeBatches(ctx context.Context, q dbq, old Order) ([]Batch, error) {
	batches, e := list[Batch](ctx, q, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND order_id=$2 ORDER BY id`, old.AccountID, old.ID)
	if e != nil {
		return nil, e
	}
	for i := range batches {
		b := &batches[i]
		if b.ProductID != old.ProductID || b.CreditScope != old.CreditScope || b.SubscriptionID != old.SubscriptionID ||
			b.Kind != "base" || b.Frozen != (old.UpgradeReturn != nil) {
			return nil, ErrConflict
		}
		if e = s.quantities(ctx, b); e != nil {
			return nil, e
		}
		original, e := integer(b.Original, true)
		if e != nil || b.Original != old.Offer.MonthlyCredit {
			return nil, ErrConflict
		}
		for _, raw := range []string{b.Available, b.Reserved, b.Consumed, b.Expired, b.Withdrawn} {
			n, e := integer(raw, false)
			if e != nil || n > original {
				return nil, ErrConflict
			}
			original -= n
		}
		if original != 0 {
			return nil, ErrConflict
		}
	}
	return batches, nil
}

func upgradeReturnAmounts(old Order, batches []Batch, cutover time.Time, credit, service int64, estimate bool) (serviceCash, computeCash, total int64, e error) {
	for _, b := range batches {
		held, he := integer(b.Reserved, false)
		if he != nil {
			return 0, 0, 0, ErrConflict
		}
		if !estimate && held > 0 {
			return 0, 0, 0, ErrPending
		}
		if !b.ExpiresAt.After(cutover) {
			continue
		}
		used, ue := integer(b.Consumed, false)
		if ue != nil || used > credit || held > credit-used {
			return 0, 0, 0, ErrConflict
		}
		credit -= used + held
	}
	computeCash = credit / productCreditsPerCNY
	original, e := integer(old.Amount, true)
	if e != nil || service > original || computeCash > original-service {
		return 0, 0, 0, ErrConflict
	}
	total = service + computeCash
	if old.PaymentSource == "external" {
		total -= total % 10000
	}
	computeCash = min(computeCash, total)
	return total - computeCash, computeCash, total, nil
}

func (s *Service) prepareProductCreditUpgrade(ctx context.Context, q dbq, o *Order, cutover time.Time) (Order, []Batch, []ledger.Posting, error) {
	upgrade := o.ProductCreditUpgrade
	sub, e := load[Subscription](ctx, q, `SELECT snapshot FROM billing_subscriptions WHERE id=$1 AND account_id=$2`, upgrade.PreviousSubscriptionID, o.AccountID)
	if e != nil {
		return Order{}, nil, nil, e
	}
	if sub.Status != "active" || sub.Version != upgrade.PreviousVersion || !cutover.Before(o.PeriodEnd) {
		return Order{}, nil, nil, ErrQuoteInvalidated
	}
	old, e := previousProductUpgradeOrder(ctx, q, sub, upgrade.PreviousOrderID)
	if e != nil {
		return Order{}, nil, nil, e
	}
	credit, service, e := upgradeReturnCeiling(old, sub, cutover)
	if e != nil {
		return Order{}, nil, nil, e
	}
	batches, e := s.productUpgradeBatches(ctx, q, old)
	if e != nil {
		return Order{}, nil, nil, e
	}
	var posts []ledger.Posting
	for i := range batches {
		b := &batches[i]
		b.Frozen = true
		if b.ExpiresAt.After(cutover) {
			posts = append(posts, withdrawUpgradeBatch(*b)...)
		}
	}
	old.RefundStatus = "UPGRADE_PENDING"
	old.UpgradeReturn = &ProductCreditUpgradeReturn{ReplacementOrderID: o.ID, Cutover: cutover, CreditCeiling: str(credit), ServiceAmount: str(service)}
	o.Start, o.End = cutover, o.Offer.monthAnchor(cutover, o.Offer.membershipMonths())
	o.PeriodStart, o.PeriodEnd, _ = o.Offer.currentPeriod(cutover, o.End, cutover)
	return old, batches, posts, nil
}

// The payment can be observed after a worker has posted an expiry that fell after cutover.
func withdrawUpgradeBatch(b Batch) []ledger.Posting {
	var posts []ledger.Posting
	for _, source := range []struct{ bucket, amount string }{{"available", b.Available}, {"expired", b.Expired}} {
		amount, _ := integer(source.amount, false)
		if amount > 0 {
			posts = append(posts, ledger.Posting{Source: BatchAddress(b.ID, source.bucket), Destination: BatchAddress(b.ID, "withdrawn"), Asset: CreditAsset, Amount: amount})
		}
	}
	return posts
}
