package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/wallet"
)

func (s *Service) RefundCheckout(ctx context.Context, actor, account, checkout, key, reason string) (Checkout, error) {
	var x checkoutRecord
	if !keyOK(key) || !keyOK(reason) || len(reason) > 80 {
		return x.Checkout, ErrInvalid
	}
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return x.Checkout, e
	}
	// Finish the receipt command first; provider cash cannot be refunded without
	// its durable receipt liability. This never calls a provider create method.
	if _, e := s.settleCheckout(ctx, account, checkout); e != nil {
		return x.Checkout, e
	}
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		var e error
		x, e = readCheckout(ctx, c, account, checkout)
		if e != nil {
			return e
		}
		if x.RefundID != "" {
			if x.RefundKey != key || x.RefundReason != reason {
				return ErrConflict
			}
			return nil
		}
		if x.Receipt == nil {
			return ErrInvalid
		}
		o, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, x.OrderID)
		if e != nil {
			return e
		}
		if o.PaymentStatus != "PAID" {
			return ErrPending
		}
		if e = s.clearExceptOrder(ctx, c, account, o.ID); e != nil {
			return e
		}
		bs, e := list[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND order_id=$2 ORDER BY id`, account, o.ID)
		if e != nil {
			return e
		}
		x.RefundID = id("")
		x.RefundKey = key
		x.RefundReason = reason
		x.RefundStatus = "PENDING"
		o.RefundActorID = actor
		o.RefundKey = key
		o.RefundReason = reason
		o.RefundStatus = "FROZEN"
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		for i := range bs {
			bs[i].Frozen = true
			if e = saveBatch(ctx, tx, bs[i]); e != nil {
				return e
			}
		}
		if e = saveOrder(ctx, tx, o); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE billing_checkouts SET snapshot=$2 WHERE id=$1`, x.ID, encode(x)); e != nil {
			return e
		}
		if e = event(ctx, tx, account, actor, x.ID, "external_refund.requested", map[string]string{"key": key, "reason": reason}); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	if e != nil {
		return x.Checkout, e
	}
	if x.RefundStatus == "SUCCEEDED" || x.RefundStatus == "REVIEW" {
		return s.checkoutView(ctx, x)
	}
	return s.recoverCheckoutRefund(ctx, account, checkout)
}

// refundableExternalLocked checks unused/unreserved grants after freezing them.
// FROZEN order state prevents expiration/maintenance from racing with a
// confirmed refund's final postings until its ledger command is applied.
func (s *Service) prepareCheckoutRefund(ctx context.Context, account, checkout string) (checkoutRecord, error) {
	var x checkoutRecord
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		var e error
		x, e = readCheckout(ctx, c, account, checkout)
		if e != nil {
			return e
		}
		if x.RefundStatus != "PENDING" {
			return nil
		}
		var exists bool
		if e = c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_commands WHERE account_id=$1 AND kind='external_refund' AND request_key=$2)`, account, checkout).Scan(&exists); e != nil {
			return e
		}
		if exists || x.RefundReceipt != nil {
			return nil
		}
		o, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, x.OrderID)
		if e != nil {
			return e
		}
		bs, e := list[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND order_id=$2`, account, o.ID)
		if e != nil {
			return e
		}
		manual := ""
		// Received-but-unfulfilled late payments have no service or points to revoke.
		if o.FulfillmentStatus == "FULFILLED" {
			if o.Offer.RefundRule != "unused_only_v1" {
				manual = "refund_policy_not_configured"
			}
			if o.Offer.Kind == "membership" && o.Offer.MonthlyCredit == "0" {
				manual = "service_entitlements_require_review"
			}
		}
		for i := range bs {
			if e = s.quantities(ctx, &bs[i]); e != nil {
				return e
			}
			if bs[i].Consumed != "0" || bs[i].Reserved != "0" {
				manual = "credits_consumed_or_reserved"
			}
		}
		if o.SubscriptionID != "" && o.FulfillmentStatus == "FULFILLED" {
			sub, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1`, o.SubscriptionID)
			if errors.Is(e, accounts.ErrNotFound) {
				manual = "subscription_changed"
			} else if e != nil {
				return e
			} else if sub.Offer.ID != o.Offer.ID || o.Kind == "upgrade" || !sub.PaidThrough.Equal(o.End) {
				manual = "subscription_changed"
			}
		}
		if manual != "" {
			x.RefundStatus = "REVIEW"
			x.ErrorCode = manual
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
			if _, e = tx.Exec(ctx, `UPDATE billing_checkouts SET snapshot=$2 WHERE id=$1`, x.ID, encode(x)); e != nil {
				return e
			}
			return tx.Commit(ctx)
		}
		return nil
	})
	return x, e
}
func (s *Service) recoverCheckoutRefund(ctx context.Context, account, checkout string) (Checkout, error) {
	x, e := s.prepareCheckoutRefund(ctx, account, checkout)
	if e != nil {
		return x.Checkout, e
	}
	if x.RefundStatus != "PENDING" {
		return s.checkoutView(ctx, x)
	}
	if x.RefundReceipt == nil {
		p, e := s.checkoutBoundProvider(ctx, x)
		if e != nil {
			return x.Checkout, e
		}
		req := payments.RefundRequest{OrderID: x.ID, TransactionID: x.Receipt.TransactionID, RefundID: x.RefundID, Amount: x.Amount, TotalAmount: x.Amount, Reason: x.RefundReason}
		result, e := p.QueryRefund(ctx, req)
		if e != nil {
			return x.Checkout, nil
		}
		if result.Status == "NOT_FOUND" {
			_, e = p.Refund(ctx, req)
			if e != nil {
				return x.Checkout, nil
			}
			// An accepted refund request is never completion evidence; independently
			// query the original refund before recording provider success.
			result, e = p.QueryRefund(ctx, req)
			if e != nil {
				return x.Checkout, nil
			}
		}
		if result.Status != "SUCCEEDED" {
			if result.Status == "CLOSED" {
				e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
					current, e := readCheckout(ctx, c, account, checkout)
					if e != nil {
						return e
					}
					x = current
					if x.RefundReceipt == nil {
						x.RefundStatus = "REVIEW"
						x.ErrorCode = "provider_refund_closed"
						o, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, x.OrderID)
						if e != nil {
							return e
						}
						o.RefundStatus = "MANUAL_REVIEW"
						o.RefundReason = x.ErrorCode
						tx, e := c.Begin(ctx)
						if e != nil {
							return e
						}
						defer database.Rollback(tx)
						if e = saveOrder(ctx, tx, o); e != nil {
							return e
						}
						if _, e = tx.Exec(ctx, `UPDATE billing_checkouts SET snapshot=$2 WHERE id=$1`, x.ID, encode(x)); e != nil {
							return e
						}
						return tx.Commit(ctx)
					}
					return nil
				})
			}
			return x.Checkout, e
		}
		if result.RefundID != x.RefundID || result.Amount != x.Amount || result.TransactionID != x.Receipt.TransactionID {
			return x.Checkout, ErrInvalid
		}
		e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
			current, e := readCheckout(ctx, c, account, checkout)
			if e != nil {
				return e
			}
			x = current
			if x.RefundReceipt != nil {
				if x.RefundReceipt.TransactionID != result.TransactionID {
					return ErrConflict
				}
				return nil
			}
			x.RefundReceipt = &result
			return saveCheckout(ctx, c, x)
		})
		if e != nil {
			return x.Checkout, e
		}
	}
	e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
		current, e := readCheckout(ctx, c, account, checkout)
		if e != nil {
			return e
		}
		x = current
		if x.RefundStatus == "SUCCEEDED" {
			return nil
		}
		if x.RefundReceipt == nil {
			return ErrPending
		}
		var commandID string
		e = c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind='external_refund' AND request_key=$2`, account, checkout).Scan(&commandID)
		if errors.Is(e, pgx.ErrNoRows) {
			o, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, x.OrderID)
			if e != nil {
				return e
			}
			bs, e := list[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND order_id=$2`, account, o.ID)
			if e != nil {
				return e
			}
			posts := []ledger.Posting{}
			for _, b := range bs {
				n, e := s.ledger.Balance(ctx, BatchAddress(b.ID, "available"), CreditAsset)
				if e != nil {
					return e
				}
				if n > 0 {
					posts = append(posts, ledger.Posting{Source: BatchAddress(b.ID, "available"), Destination: BatchAddress(b.ID, "withdrawn"), Asset: CreditAsset, Amount: n})
				}
			}
			source := externalReceived(x.ID)
			var sub *Subscription
			if o.FulfillmentStatus == "FULFILLED" {
				source = "commercial:revenue:" + o.ProductID
				if o.SubscriptionID != "" {
					value, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1`, o.SubscriptionID)
					if e != nil {
						return e
					}
					value.Status = "refunded"
					value.Version++
					sub = &value
				}
			}
			amount, _ := integer(x.Amount, true)
			posts = append(posts, ledger.Posting{Source: source, Destination: "commercial:external:refunded:" + x.ID, Asset: wallet.Asset, Amount: amount})
			o.RefundStatus = "REFUNDED"
			o.FulfillmentStatus = "CANCELED"
			tx, e := c.Begin(ctx)
			if e != nil {
				return e
			}
			defer database.Rollback(tx)
			cmd, e := s.createCommand(ctx, tx, account, o.RefundActorID, "external_refund", checkout, posts, effect{Order: &o, Subscription: sub, Batches: bs})
			if e != nil {
				return e
			}
			if e = tx.Commit(ctx); e != nil {
				return e
			}
			commandID = cmd.ID
		} else if e != nil {
			return e
		}
		if e = s.runLocked(ctx, c, commandID); e != nil {
			return e
		}
		cmd, e := readCommand(ctx, c, commandID)
		if e != nil {
			return e
		}
		if cmd.Status != "APPLIED" {
			return ErrPending
		}
		x.RefundStatus = "SUCCEEDED"
		x.FulfillmentStatus = "NOT_STARTED"
		x.ErrorCode = ""
		return saveCheckout(ctx, c, x)
	})
	if e != nil {
		return x.Checkout, e
	}
	return s.checkoutView(ctx, x)
}
