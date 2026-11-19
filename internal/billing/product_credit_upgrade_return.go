package billing

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/wallet"
)

func upgradeReturnCash(o Order) string {
	return "commercial:external:upgrade_return_payable:" + o.ID
}

func (s *Service) RecoverProductUpgradeReturns(ctx context.Context) error {
	orders, e := list[Order](ctx, s.pool, `SELECT snapshot FROM billing_orders
		WHERE snapshot->>'refund_status' IN ('UPGRADE_PENDING','UPGRADE_RELEASING')
		ORDER BY upgrade_return_attempted_at NULLS FIRST,id LIMIT 200`)
	if e != nil {
		return e
	}
	var failures error
	for _, old := range orders {
		if e = ctx.Err(); e != nil {
			return errors.Join(failures, e)
		}
		if _, e = s.pool.Exec(ctx, `UPDATE billing_orders SET upgrade_return_attempted_at=clock_timestamp() WHERE id=$1`, old.ID); e != nil {
			return errors.Join(failures, e)
		}
		if old.RefundStatus == "UPGRADE_PENDING" {
			old, e = s.prepareUpgradeReturn(ctx, old)
			if e != nil {
				if !errors.Is(e, ErrPending) {
					failures = errors.Join(failures, e)
				}
				continue
			}
		}
		if old.RefundStatus == "UPGRADE_RELEASING" {
			if e = s.finishExternalUpgradeReturn(ctx, old); e != nil && !errors.Is(e, ErrPending) {
				failures = errors.Join(failures, e)
			}
		}
	}
	return failures
}

func (s *Service) prepareUpgradeReturn(ctx context.Context, old Order) (Order, error) {
	e := s.locked(ctx, old.AccountID, func(c *pgxpool.Conn) error {
		var e error
		old, e = load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1 AND account_id=$2`, old.ID, old.AccountID)
		if e != nil || old.RefundStatus != "UPGRADE_PENDING" {
			return e
		}
		if old.UpgradeReturn == nil || old.UpgradeReturn.RefundID != "" {
			return ErrConflict
		}
		if e = s.clear(ctx, c, old.AccountID); e != nil {
			return e
		}
		value := old.UpgradeReturn
		batches, e := s.productUpgradeBatches(ctx, c, old)
		if e != nil {
			return e
		}
		credits, ce := integer(value.CreditCeiling, true)
		service, se := integer(value.ServiceAmount, false)
		if ce != nil || se != nil {
			return ErrConflict
		}
		service, compute, total, e := upgradeReturnAmounts(old, batches, value.Cutover, credits, service, false)
		if e != nil {
			return e
		}
		value.Amount, value.ServiceAmount, value.ComputeAmount = str(total), str(service), str(compute)
		old.RefundStatus = "UPGRADE_RETURNED"
		destination := wallet.Address(old.AccountID, "")
		if old.PaymentSource == "external" && total > 0 {
			if _, e = originalUpgradeCheckout(ctx, c, old); e != nil {
				return e
			}
			old.RefundStatus = "UPGRADE_RELEASING"
			value.RefundID = id("")
			destination = upgradeReturnCash(old)
		} else if old.PaymentSource == "wallet" && (old.ProductFunding != "0" || old.UnifiedFunding != old.Amount) {
			return ErrConflict
		}
		posts := []ledger.Posting{}
		for _, b := range batches {
			if !b.Frozen {
				return ErrConflict
			}
			if b.ExpiresAt.After(value.Cutover) {
				posts = append(posts, withdrawUpgradeBatch(b)...)
			}
		}
		if service > 0 {
			posts = append(posts, ledger.Posting{Source: "commercial:revenue:" + old.ProductID, Destination: destination, Asset: CashAsset, Amount: service})
		}
		if compute > 0 {
			posts = append(posts, ledger.Posting{Source: productComputePrepay(old), Destination: destination, Asset: CashAsset, Amount: compute})
		}
		return s.postUpgradeReturn(ctx, c, old, "upgrade_return_prepare", posts)
	})
	return old, e
}

func originalUpgradeCheckout(ctx context.Context, q dbq, old Order) (checkoutRecord, error) {
	x, e := readCheckout(ctx, q, old.AccountID, old.CheckoutID)
	if e != nil {
		return x, e
	}
	if x.OrderID != old.ID || x.ProductID != old.ProductID || x.Amount != old.Amount || x.Receipt == nil ||
		x.Receipt.Amount != old.Amount || x.Receipt.TransactionID != old.ProviderTransactionID ||
		(x.RefundID != "" && (x.RefundStatus != "REJECTED" || x.RefundReceipt != nil)) ||
		x.PaymentStatus != "PAID" || x.FulfillmentStatus != "FULFILLED" {
		return x, ErrConflict
	}
	return x, nil
}

func (s *Service) finishExternalUpgradeReturn(ctx context.Context, old Order) error {
	value := old.UpgradeReturn
	if value == nil || value.RefundID == "" {
		return ErrConflict
	}
	amount, e := integer(value.Amount, true)
	if e != nil || amount%10000 != 0 {
		return ErrConflict
	}
	x, e := originalUpgradeCheckout(ctx, s.pool, old)
	if e != nil {
		return e
	}
	p, e := s.checkoutBoundProvider(ctx, x)
	if e != nil {
		return e
	}
	refund := payments.RefundRequest{OrderID: x.ID, TransactionID: x.Receipt.TransactionID, RefundID: value.RefundID,
		Amount: value.Amount, TotalAmount: x.Amount, Reason: "subscription_upgrade"}
	r, e := p.QueryRefund(ctx, refund)
	if e != nil {
		return e
	}
	if r.Status == "NOT_FOUND" {
		if _, e = p.Refund(ctx, refund); e != nil {
			return e
		}
		r, e = p.QueryRefund(ctx, refund)
		if e != nil {
			return e
		}
	}
	if r.Status != "SUCCEEDED" {
		if r.Status == "CLOSED" {
			return s.markExternalUpgradeReturnForReview(ctx, old)
		}
		return ErrPending
	}
	if r.RefundID != value.RefundID || r.Amount != value.Amount || r.TransactionID != x.Receipt.TransactionID {
		return ErrConflict
	}
	return s.locked(ctx, old.AccountID, func(c *pgxpool.Conn) error {
		current, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1 AND account_id=$2`, old.ID, old.AccountID)
		if e != nil || current.RefundStatus == "UPGRADE_RETURNED" {
			return e
		}
		if current.RefundStatus != "UPGRADE_RELEASING" || current.UpgradeReturn == nil ||
			current.UpgradeReturn.RefundID != value.RefundID || current.UpgradeReturn.Amount != value.Amount {
			return ErrConflict
		}
		current.RefundStatus = "UPGRADE_RETURNED"
		posts := []ledger.Posting{{Source: upgradeReturnCash(old), Destination: "commercial:external:refunded:" + x.ID, Asset: CashAsset, Amount: amount}}
		return s.postUpgradeReturn(ctx, c, current, "upgrade_return_confirm", posts)
	})
}

func (s *Service) markExternalUpgradeReturnForReview(ctx context.Context, old Order) error {
	return s.locked(ctx, old.AccountID, func(c *pgxpool.Conn) error {
		current, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1 AND account_id=$2`, old.ID, old.AccountID)
		if e != nil || current.RefundStatus != "UPGRADE_RELEASING" {
			return e
		}
		if current.UpgradeReturn == nil || current.UpgradeReturn.RefundID != old.UpgradeReturn.RefundID {
			return ErrConflict
		}
		current.RefundStatus = "UPGRADE_REVIEW"
		current.RefundReason = "provider_upgrade_refund_closed"
		return s.postUpgradeReturn(ctx, c, current, "upgrade_return_review", nil)
	})
}

func (s *Service) postUpgradeReturn(ctx context.Context, c *pgxpool.Conn, old Order, kind string, posts []ledger.Posting) error {
	if e := s.clear(ctx, c, old.AccountID); e != nil {
		return e
	}
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	cmd, e := s.createCommand(ctx, tx, old.AccountID, old.ActorID, kind, old.ID, posts, effect{Order: &old})
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return s.runLocked(ctx, c, cmd.ID)
}
