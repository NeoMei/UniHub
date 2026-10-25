package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
)

func (s *Service) QuoteUpgrade(ctx context.Context, actor, account, subID, offerID string) (Quote, error) {
	var out Quote
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		if e := s.clear(ctx, c, account); e != nil {
			return e
		}
		sub, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1 AND account_id=$2`, subID, account)
		if e != nil {
			return e
		}
		offer, e := s.buyable(ctx, c, offerID, account)
		if e != nil {
			return e
		}
		if offer.Kind != "membership" || offer.ProductID != sub.ProductID || sub.Status != "active" || offer.Rank <= sub.Offer.Rank {
			return ErrInvalid
		}
		if offer.Period != sub.Offer.Period {
			return ErrUnsupported
		}
		if offer.UpgradeRule != "remaining_term_prorata_v1" {
			return ErrUnsupported
		}
		now := s.now().UTC()
		start, end, ok := CurrentPeriod(sub.Anchor, sub.PaidThrough, now)
		if !ok {
			return ErrInvalid
		}
		if e = s.monthlyLocked(ctx, c, sub, now); e != nil {
			return e
		}
		base, _ := integer(sub.Offer.Price, true)
		next, _ := integer(offer.Price, true)
		if next <= base {
			return ErrInvalid
		}
		termStart := sub.Anchor
		if offer.Period == "month" {
			termStart = start
		} else {
			m := (sub.PaidThrough.Year()-sub.Anchor.Year())*12 + int(sub.PaidThrough.Month()-sub.Anchor.Month())
			termStart = MonthAnchor(sub.Anchor, m-12)
		}
		amount, e := prorate(next-base, sub.PaidThrough.Sub(now), sub.PaidThrough.Sub(termStart))
		if e != nil {
			return e
		}
		rows, e := list[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND snapshot->>'subscription_id'=$2 AND (snapshot->>'starts_at')::timestamptz=$3`, account, subID, start)
		if e != nil {
			return e
		}
		newCredit, _ := integer(offer.MonthlyCredit, false)
		issued := int64(0)
		for _, b := range rows {
			n, _ := integer(b.Original, true)
			if n > newCredit-issued {
				return ErrInvalid
			}
			issued += n
		}
		out = Quote{ID: id("quote_"), AccountID: account, SubscriptionID: subID, SubscriptionVersion: sub.Version, Offer: offer, Amount: amount, GrantAmount: str(newCredit - issued), PeriodStart: start, ExpiresAt: end, CreatedAt: now}
		_, e = c.Exec(ctx, `INSERT INTO billing_quotes(id,account_id,snapshot) VALUES($1,$2,$3)`, out.ID, account, encode(out))
		return e
	})
	return out, e
}
func (s *Service) AcceptUpgrade(ctx context.Context, actor, account, quoteID, key string) (Order, error) {
	var out Order
	if !keyOK(key) {
		return out, ErrInvalid
	}
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		h := hash([]string{"upgrade", actor, quoteID})
		old, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND request_key=$2`, account, key)
		if e == nil {
			var oldh string
			if e = c.QueryRow(ctx, `SELECT request_digest FROM billing_orders WHERE id=$1`, old.ID).Scan(&oldh); e != nil {
				return e
			}
			if oldh != h {
				return ErrConflict
			}
			out = old
			return s.resumeOrderLocked(ctx, c, &out)
		}
		if !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		if e = s.clear(ctx, c, account); e != nil {
			return e
		}
		q, e := load[Quote](ctx, c, `SELECT snapshot FROM billing_quotes WHERE account_id=$1 AND id=$2`, account, quoteID)
		if e != nil {
			return e
		}
		sub, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND id=$2`, account, q.SubscriptionID)
		if e != nil {
			return e
		}
		now := s.now().UTC()
		start, _, ok := CurrentPeriod(sub.Anchor, sub.PaidThrough, now)
		if !ok || !now.Before(q.ExpiresAt) || !start.Equal(q.PeriodStart) || sub.Version != q.SubscriptionVersion {
			return ErrConflict
		}
		offer, e := s.buyable(ctx, c, q.Offer.ID, account)
		if e != nil {
			return e
		}
		if hash(offer) != hash(q.Offer) {
			return ErrConflict
		}
		o := Order{ID: id("bo_"), AccountID: account, ActorID: actor, ProductID: offer.ProductID, Kind: "upgrade", Offer: offer, Amount: q.Amount, PaymentStatus: "PENDING", FulfillmentStatus: "PENDING", SubscriptionID: sub.ID, Start: now, End: sub.PaidThrough, PeriodStart: q.PeriodStart, PeriodEnd: q.ExpiresAt, GrantAmount: q.GrantAmount, CreatedAt: now}
		out, e = s.payLocked(ctx, c, o, key, h, nil)
		if e != nil {
			return e
		}
		return s.fulfillAndReadLocked(ctx, c, &out, now)
	})
	return out, e
}
