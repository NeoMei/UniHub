package billing

import (
	"context"
	"math"
	"time"
)

// QuoteExternalUpgrade is a separate immutable contract. Wallet quotes retain
// their exact prorated CNY/6 value; external quotes explicitly round up to fen.
func (s *Service) QuoteExternalUpgrade(ctx context.Context, actor, account, subID, offerID string) (Quote, error) {
	return s.quoteExternalUpgrade(ctx, actor, account, subID, offerID, false)
}

func (s *Service) quoteExternalUpgrade(ctx context.Context, actor, account, subID, offerID string, allowLegacyNet bool) (Quote, error) {
	legacyNetPaymentSource := ""
	if allowLegacyNet {
		legacyNetPaymentSource = "external"
	}
	q, e := s.quoteUpgrade(ctx, actor, account, subID, offerID, legacyNetPaymentSource)
	if e != nil || q.PaymentSource == "external" {
		return q, e
	}
	q, e = externalUpgradeQuote(q)
	if e != nil {
		return q, e
	}
	_, e = s.pool.Exec(ctx, `INSERT INTO billing_quotes(id,account_id,snapshot) VALUES($1,$2,$3)`, q.ID, account, encode(q))
	return q, e
}

func externalUpgradeQuote(q Quote) (Quote, error) {
	n, e := integer(q.Amount, true)
	if e != nil || n > math.MaxInt64-9999 {
		return Quote{}, ErrInvalid
	}
	legacyNet := hasLegacyNetUpgrade(q.ProductCreditUpgrade)
	if q.ProductCreditUpgrade != nil && !legacyNet && n%10000 != 0 {
		return Quote{}, ErrInvalid
	}
	rounded := (n + 9999) / 10000 * 10000
	if legacyNet {
		gross, grossErr := integer(q.Offer.Price, true)
		if grossErr != nil || rounded > gross {
			return Quote{}, ErrUnsupported
		}
		upgrade := *q.ProductCreditUpgrade
		upgrade.AppliedCreditAmount = str(gross - rounded)
		q.ProductCreditUpgrade = &upgrade
	}
	q.ID = id("quote_")
	q.PaymentSource = "external"
	q.RoundingRule = "cny_cent_ceil_v1"
	q.OriginalAmount = q.Amount
	q.Amount = str(rounded)
	if !legacyNet {
		q.PeriodEnd = q.ExpiresAt
	}
	expiry := q.CreatedAt.Add(15 * time.Minute)
	if expiry.Before(q.ExpiresAt) {
		q.ExpiresAt = expiry
	}
	return q, nil
}
