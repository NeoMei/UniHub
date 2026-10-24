package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

// RequestRefund freezes the source before examining ledger quantities. Any usage
// or in-flight hold requires an explicit manual decision, never a full auto-refund.
func (s *Service) RequestRefund(ctx context.Context, actor, account, orderID, key string) (Order, error) {
	var out Order
	if !keyOK(key) {
		return out, ErrInvalid
	}
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		o, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND id=$2`, account, orderID)
		if e != nil {
			return e
		}
		out = o
		if o.RefundStatus == "REJECTED" {
			return nil
		}
		if o.RefundStatus == "MANUAL_REVIEW" {
			return ErrManualReview
		}
		if o.RefundStatus == "REFUNDED" {
			return nil
		}
		var old string
		e = c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind='refund' AND request_key=$2`, account, orderID).Scan(&old)
		if e == nil {
			if e = s.runLocked(ctx, c, old); e != nil {
				return e
			}
			out, e = load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, orderID)
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e = s.clearExceptOrder(ctx, c, account, o.ID); e != nil {
			return e
		}
		if o.PaymentStatus != "PAID" {
			return ErrInvalid
		}
		bs, e := list[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND order_id=$2 ORDER BY id`, account, orderID)
		if e != nil {
			return e
		}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		o.RefundActorID = actor
		o.RefundKey = key
		o.RefundStatus = "FROZEN"
		o.RefundReason = "requested"
		if e = saveOrder(ctx, tx, o); e != nil {
			return e
		}
		for i := range bs {
			bs[i].Frozen = true
			if e = saveBatch(ctx, tx, bs[i]); e != nil {
				return e
			}
		}
		if e = event(ctx, tx, account, actor, orderID, "refund.requested", map[string]string{"key": key}); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		manual := ""
		if o.Offer.RefundRule != "unused_only_v1" {
			manual = "refund_policy_not_configured"
		}
		posts := []ledger.Posting{}
		for _, b := range bs {
			if e = s.quantities(ctx, &b); e != nil {
				return e
			}
			reserved, _ := integer(b.Reserved, false)
			consumed, _ := integer(b.Consumed, false)
			if reserved > 0 {
				manual = "credits_reserved"
			}
			if consumed > 0 {
				manual = "credits_consumed"
			}
			available, _ := integer(b.Available, false)
			if available > 0 {
				posts = append(posts, ledger.Posting{Source: BatchAddress(b.ID, "available"), Destination: BatchAddress(b.ID, "withdrawn"), Asset: CreditAsset, Amount: available})
			}
		}
		var sub *Subscription
		if o.SubscriptionID != "" {
			x, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1`, o.SubscriptionID)
			if e == nil {
				if x.Offer.ID != o.Offer.ID || o.Kind == "upgrade" || !x.PaidThrough.Equal(o.End) {
					manual = "subscription_changed"
				} else {
					x.Status = "refunded"
					x.Version++
					sub = &x
				}
			} else if !errors.Is(e, accounts.ErrNotFound) {
				return e
			}
		}
		if manual != "" {
			o.RefundStatus = "MANUAL_REVIEW"
			o.RefundReason = manual
			tx, e := c.Begin(ctx)
			if e != nil {
				return e
			}
			defer database.Rollback(tx)
			if e = saveOrder(ctx, tx, o); e != nil {
				return e
			}
			if e = event(ctx, tx, account, actor, orderID, "refund.manual_review", map[string]string{"reason": manual}); e != nil {
				return e
			}
			if e = tx.Commit(ctx); e != nil {
				return e
			}
			out = o
			return ErrManualReview
		}
		productAmount, _ := integer(o.ProductFunding, false)
		unifiedAmount, _ := integer(o.UnifiedFunding, false)
		if productAmount > 0 {
			posts = append(posts, ledger.Posting{Source: "commercial:revenue:" + o.ProductID, Destination: wallet.Address(account, o.ProductID), Asset: wallet.Asset, Amount: productAmount})
		}
		if unifiedAmount > 0 {
			posts = append(posts, ledger.Posting{Source: "commercial:revenue:" + o.ProductID, Destination: wallet.Address(account, ""), Asset: wallet.Asset, Amount: unifiedAmount})
		}
		o.RefundStatus = "REFUNDED"
		o.RefundReason = "unused_credits_revoked"
		o.FulfillmentStatus = "CANCELED"
		tx, e = c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		cmd, e := s.createCommand(ctx, tx, account, actor, "refund", orderID, posts, effect{Order: &o, Subscription: sub, Batches: bs, Finance: true})
		if e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		if e = s.runLocked(ctx, c, cmd.ID); e != nil {
			return e
		}
		out, e = load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, orderID)
		return e
	})
	return out, e
}

// ResolveRefundReview currently supports a documented rejection only. Used or
// reserved credit approval requires a separately reviewed commercial policy.
func (s *Service) ResolveRefundReview(ctx context.Context, operator, account, orderID, decision, reason string) (Order, error) {
	var out Order
	if e := s.operator(operator); e != nil {
		return out, e
	}
	if decision != "reject" {
		return out, ErrUnsupported
	}
	if !keyOK(reason) {
		return out, ErrInvalid
	}
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		o, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1 AND account_id=$2`, orderID, account)
		if e != nil {
			return e
		}
		if o.RefundStatus == "REJECTED" {
			out = o
			return nil
		}
		if o.RefundStatus != "MANUAL_REVIEW" {
			return ErrConflict
		}
		if e = s.clearExceptOrder(ctx, c, account, o.ID); e != nil {
			return e
		}
		bs, e := list[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE order_id=$1 AND account_id=$2`, orderID, account)
		if e != nil {
			return e
		}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		o.RefundStatus = "REJECTED"
		o.RefundReason = reason
		if e = saveOrder(ctx, tx, o); e != nil {
			return e
		}
		for _, b := range bs {
			b.Frozen = false
			if e = saveBatch(ctx, tx, b); e != nil {
				return e
			}
		}
		if e = event(ctx, tx, account, operator, orderID, "refund.review_rejected", map[string]string{"reason": reason}); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		out = o
		return nil
	})
	return out, e
}
