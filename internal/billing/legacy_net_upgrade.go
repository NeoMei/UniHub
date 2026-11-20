package billing

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
)

const legacyNetUpgradeProduct = "superwagie"

// QuoteUpgradeWithLegacyNet is used by the negotiated SuperWagie checkout
// contract. Other callers retain the existing scope-change restriction.
func (s *Service) QuoteUpgradeWithLegacyNet(ctx context.Context, actor, account, subID, offerID string, external bool) (Quote, error) {
	if external {
		return s.quoteExternalUpgrade(ctx, actor, account, subID, offerID, true)
	}
	return s.quoteUpgrade(ctx, actor, account, subID, offerID, "wallet")
}

func previousLegacyNetUpgradeOrder(ctx context.Context, q dbq, sub Subscription, expectedID string) (Order, error) {
	if sub.ProductID != legacyNetUpgradeProduct || sub.Offer.CreditScope != "" || sub.Status != "active" {
		return Order{}, ErrQuoteInvalidated
	}
	orders, err := list[Order](ctx, q, `SELECT snapshot FROM billing_orders
		WHERE account_id=$1 AND id=snapshot->>'id' AND snapshot->>'subscription_id'=$2
		AND (snapshot->>'end')::timestamptz=$3::text::timestamptz
		AND snapshot->>'payment_status'='PAID' AND snapshot->>'fulfillment_status'='FULFILLED'
		LIMIT 2`, sub.AccountID, sub.ID, sub.PaidThrough.Format(time.RFC3339Nano))
	if err != nil {
		return Order{}, err
	}
	// Previous legacy upgrades either add a second charge for the same term or
	// obscure the original full-term purchase. Neither gives a unique basis.
	if len(orders) != 1 {
		return Order{}, ErrQuoteInvalidated
	}
	old := orders[0]
	months := monthsBetween(sub.Anchor.UTC(), sub.PaidThrough.UTC())
	start := sub.Offer.monthAnchor(sub.Anchor, months-sub.Offer.membershipMonths())
	if old.ID == "" || (expectedID != "" && old.ID != expectedID) || old.AccountID != sub.AccountID ||
		old.SubscriptionID != sub.ID || old.ProductID != sub.ProductID || (old.Kind != "membership" && old.Kind != "renewal") ||
		old.CreditScope != "" || old.ServiceAmount != "" || old.ComputeAmount != "" || old.UpgradeReturn != nil || old.ProductCreditUpgrade != nil ||
		refundBlocks(old.RefundStatus) || old.Start.Before(sub.Anchor) || !old.Start.Equal(start) || !old.End.Equal(sub.PaidThrough) ||
		hash(old.Offer) != hash(sub.Offer) || old.GrantAmount != sub.Offer.MonthlyCredit {
		return Order{}, ErrQuoteInvalidated
	}
	if old.PaymentSource == "external" {
		if _, err := originalUpgradeCheckout(ctx, q, old); err != nil {
			if errors.Is(err, accounts.ErrNotFound) || errors.Is(err, ErrConflict) {
				return Order{}, ErrQuoteInvalidated
			}
			return Order{}, err
		}
	} else if old.CheckoutID != "" || old.ProviderTransactionID != "" {
		return Order{}, ErrQuoteInvalidated
	}
	if err := validateLegacyNetUpgradePayment(ctx, q, old); err != nil {
		return Order{}, err
	}
	return old, nil
}

// Applied commands bind the amount to the original payment and fulfillment.
// Their recorded transaction IDs remain available after a ledger cutover.
func validateLegacyNetUpgradePayment(ctx context.Context, q dbq, old Order) error {
	loadApplied := func(kind, key string) (command, string, error) {
		var id string
		var transaction string
		err := q.QueryRow(ctx, `SELECT id,COALESCE(ledger_transaction_id,'') FROM billing_commands
			WHERE account_id=$1 AND kind=$2 AND request_key=$3 AND status='APPLIED'`, old.AccountID, kind, key).Scan(&id, &transaction)
		if errors.Is(err, pgx.ErrNoRows) {
			return command{}, "", ErrQuoteInvalidated
		}
		if err != nil {
			return command{}, "", err
		}
		out, err := readCommand(ctx, q, id)
		if err != nil {
			return command{}, "", err
		}
		if out.Account != old.AccountID || out.Kind != kind || out.Key != key || out.Status != "APPLIED" || out.Ledger.Reference != "unihub:billing:"+id {
			return command{}, "", ErrQuoteInvalidated
		}
		return out, transaction, nil
	}
	fulfilled, fulfillmentTransaction, err := loadApplied("fulfillment", old.ID)
	if err != nil {
		return err
	}
	if fulfilled.Effect.Order == nil || fulfilled.Effect.Order.PaymentStatus != "PAID" || fulfilled.Effect.Order.FulfillmentStatus != "FULFILLED" ||
		!sameLegacyOrderTerms(old, *fulfilled.Effect.Order) || (len(fulfilled.Ledger.Postings) != 0 && fulfillmentTransaction == "") {
		return ErrQuoteInvalidated
	}
	kind, key, postings, err := legacyPaymentProof(ctx, q, old, fulfilled)
	if err != nil {
		var invalid legacyCreditProofError
		if errors.As(err, &invalid) {
			return ErrQuoteInvalidated
		}
		return err
	}
	paid, transaction, err := loadApplied(kind, key)
	if err != nil {
		return err
	}
	if transaction == "" || paid.Actor != old.ActorID || paid.Effect.Order == nil || paid.Effect.Order.PaymentStatus != "PAID" ||
		!sameLegacyOrderTerms(old, *paid.Effect.Order) || !slices.Equal(paid.Ledger.Postings, postings) {
		return ErrQuoteInvalidated
	}
	return nil
}

func quoteLegacyNetUpgrade(ctx context.Context, q dbq, sub Subscription, offer Offer, now, start, end time.Time) (Quote, error) {
	old, err := previousLegacyNetUpgradeOrder(ctx, q, sub, "")
	if err != nil {
		return Quote{}, err
	}
	net, credit, err := legacyNetUpgradePrice(old.Amount, offer.Price, old.Start, old.End, now)
	if err != nil {
		return Quote{}, err
	}
	source := old.PaymentSource
	if source == "" {
		source = "wallet"
	}
	expiresAt := now.Add(15 * time.Minute)
	if end.Before(expiresAt) {
		expiresAt = end
	}
	return Quote{
		ID: id("quote_"), AccountID: sub.AccountID, SubscriptionID: sub.ID, SubscriptionVersion: sub.Version,
		Offer: offer, Amount: net, GrantAmount: offer.MonthlyCredit, PeriodStart: start,
		PeriodEnd: end, ExpiresAt: expiresAt, CreatedAt: now,
		ProductCreditUpgrade: &ProductCreditUpgrade{
			PreviousOrderID: old.ID, PreviousSubscriptionID: sub.ID, PreviousVersion: sub.Version,
			EstimatedRefundAmount: "0", RefundPaymentSource: source, AppliedCreditAmount: credit,
		},
	}, nil
}

func legacyNetUpgradePrice(oldPaid, gross string, start, end, now time.Time) (net, credit string, err error) {
	paid, err := integer(oldPaid, true)
	if err != nil || now.Before(start) || !now.Before(end) {
		return "", "", ErrQuoteInvalidated
	}
	price, err := integer(gross, true)
	if err != nil {
		return "", "", ErrUnsupported
	}
	remainingValue := new(big.Int).Mul(big.NewInt(paid), big.NewInt(end.Sub(now).Nanoseconds()))
	remainingValue.Quo(remainingValue, big.NewInt(end.Sub(start).Nanoseconds()))
	applied := remainingValue.Int64()
	if applied >= price {
		return "", "", ErrUnsupported
	}
	return str(price - applied), str(applied), nil
}
