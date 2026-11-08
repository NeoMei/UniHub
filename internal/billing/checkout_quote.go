package billing

import (
	"context"
	"math"
	"time"
)

// QuoteExternalUpgrade is a separate immutable contract. Wallet quotes retain
// their exact prorated CNY/6 value; external quotes explicitly round up to fen.
func (s *Service) QuoteExternalUpgrade(ctx context.Context, actor, account, subID, offerID string) (Quote, error) {
	q, e := s.QuoteUpgrade(ctx, actor, account, subID, offerID)
	if e != nil {
		return q, e
	}
	n, e := integer(q.Amount, true)
	if e != nil || n > math.MaxInt64-9999 {
		return Quote{}, ErrInvalid
	}
	q.ID = id("quote_")
	q.PaymentSource = "external"
	q.RoundingRule = "cny_cent_ceil_v1"
	q.OriginalAmount = q.Amount
	q.Amount = str((n + 9999) / 10000 * 10000)
	q.PeriodEnd = q.ExpiresAt
	expiry := q.CreatedAt.Add(15 * time.Minute)
	if expiry.Before(q.ExpiresAt) {
		q.ExpiresAt = expiry
	}
	_, e = s.pool.Exec(ctx, `INSERT INTO billing_quotes(id,account_id,snapshot) VALUES($1,$2,$3)`, q.ID, account, encode(q))
	return q, e
}
