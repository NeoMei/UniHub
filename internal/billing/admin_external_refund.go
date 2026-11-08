package billing

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/payments"
)

// approveExternalRefund holds the same platform/account lock order as Administer.
// Session locks keep frozen grants and authority stable while reading the ledger
// outside a SQL transaction. The decision, fresh authority check and audit then
// commit together on this connection. Provider recovery belongs to the worker.
func (s *Service) approveExternalRefund(ctx context.Context, authority AdminAuthority, actor string, r AdminOperation) (any, error) {
	c, e := s.pool.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	defer c.Release()
	unlockPlatform, e := database.LockAccount(ctx, c, "unihub:platform-administration")
	if e != nil {
		return nil, e
	}
	defer unlockPlatform(context.Background())
	if e = authority.RequireAdminOn(ctx, c, actor, "finance:write"); e != nil {
		return nil, e
	}
	unlockAccount, e := database.LockAccount(ctx, c, r.AccountID)
	if e != nil {
		return nil, e
	}
	defer unlockAccount(context.Background())
	var raw []byte
	var version string
	if e = c.QueryRow(ctx, `SELECT snapshot,md5(snapshot::text) FROM billing_orders WHERE id=$1 AND account_id=$2`, r.ID, r.AccountID).Scan(&raw, &version); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, accounts.ErrNotFound
		}
		return nil, e
	}
	if version != r.Version {
		return nil, ErrConflict
	}
	var o Order
	if e = json.Unmarshal(raw, &o); e != nil {
		return nil, e
	}
	if o.RefundStatus != "MANUAL_REVIEW" {
		return nil, ErrConflict
	}
	if o.PaymentSource != "external" {
		return nil, ErrUnsupported
	}
	if s.ledger == nil {
		return nil, ErrUnsupported
	}
	if e = s.clearExceptOrder(ctx, c, r.AccountID, o.ID); e != nil {
		return nil, e
	}
	x, e := readCheckout(ctx, c, r.AccountID, o.CheckoutID)
	if e != nil {
		return nil, e
	}
	if x.OrderID != o.ID || x.RefundStatus != "REVIEW" || x.RefundID == "" || x.RefundReceipt != nil || x.Receipt == nil || o.PaymentStatus != "PAID" || x.Receipt.Status != "PAID" || x.Receipt.OrderID != x.ID || x.Receipt.TransactionID != o.ProviderTransactionID || x.Receipt.Amount != x.Amount || x.Amount != o.Amount || x.Currency != "CNY" || x.Receipt.Currency != "CNY" {
		return nil, ErrConflict
	}
	receipt, e := load[payments.Result](ctx, c, `SELECT snapshot FROM billing_payment_receipts WHERE checkout_id=$1 AND provider=$2 AND merchant_id=$3 AND transaction_id=$4`, x.ID, x.Provider, x.MerchantID, x.Receipt.TransactionID)
	if e != nil {
		return nil, e
	}
	if hash(receipt) != hash(*x.Receipt) {
		return nil, ErrConflict
	}
	var commanded bool
	if e = c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_commands WHERE account_id=$1 AND kind='external_refund' AND request_key=$2)`, r.AccountID, x.ID).Scan(&commanded); e != nil {
		return nil, e
	}
	if commanded {
		return nil, ErrConflict
	}
	bs, e := list[Batch](ctx, c, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND order_id=$2`, r.AccountID, o.ID)
	if e != nil {
		return nil, e
	}
	if o.FulfillmentStatus == "FULFILLED" {
		// Service-only and changed-subscription policies remain outside this action.
		if len(bs) == 0 || o.Offer.MonthlyCredit == "0" {
			return nil, ErrManualReview
		}
		if o.SubscriptionID != "" {
			sub, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND id=$2`, r.AccountID, o.SubscriptionID)
			if errors.Is(e, accounts.ErrNotFound) {
				return nil, ErrManualReview
			}
			if e != nil {
				return nil, e
			}
			if sub.Status != "active" || sub.Offer.ID != o.Offer.ID || o.Kind == "upgrade" || !sub.PaidThrough.Equal(o.End) {
				return nil, ErrManualReview
			}
		}
	}
	for i := range bs {
		if !bs[i].Frozen {
			return nil, ErrConflict
		}
		if e = s.quantities(ctx, &bs[i]); e != nil {
			return nil, e
		}
		if bs[i].Consumed != "0" || bs[i].Reserved != "0" {
			return nil, ErrManualReview
		}
	}
	return s.administer(ctx, authority, actor, r, c.Begin)
}
