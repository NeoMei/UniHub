package cashtopup

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/payments"
)

// paymentConfigurationID mirrors billing's merchant configuration version so
// a receipt verified against a rotated merchant key is not misapplied.
func paymentConfigurationID(p payments.Provider) string {
	if c, ok := p.(interface{ ConfigurationID() string }); ok && c.ConfigurationID() != "" {
		return c.ConfigurationID()
	}
	return p.MerchantID()
}

// Notify authenticates a provider notification and credits the matching
// checkout. It is dispatched by the shared /payments/{provider}/notify route;
// the route calls this method with the raw headers and body for every provider.
func (s *Service) Notify(ctx context.Context, provider string, headers http.Header, body []byte) error {
	providers, e := s.notificationProviders(ctx, provider)
	if e != nil {
		return e
	}
	for _, p := range providers {
		result, e := p.VerifyNotification(headers, body)
		if e != nil || result.Status != "PAID" {
			continue
		}
		if len(result.OrderID) < 3 || result.OrderID[:3] != CheckoutIDPrefix {
			continue // not a cash-topup checkout
		}
		rec, e := loadAny(ctx, s.pool, result.OrderID)
		if e != nil {
			if errors.Is(e, accounts.ErrNotFound) {
				continue
			}
			return e
		}
		if rec.Provider != provider || rec.MerchantID != p.MerchantID() || rec.MerchantVersion != paymentConfigurationID(p) {
			continue
		}
		if e = s.accept(ctx, p, rec, result); e != nil {
			return e
		}
		return nil
	}
	return payments.ErrVerification
}

// accept persists the receipt and the wallet credit command atomically.
func (s *Service) accept(ctx context.Context, p payments.Provider, rec record, r payments.Result) error {
	if r.OrderID != rec.ID || r.Amount != rec.Amount || r.Currency != "CNY" || r.Status != "PAID" || r.TransactionID == "" || len(r.TransactionID) > 256 || rec.Provider != p.ID() || rec.MerchantID != p.MerchantID() || rec.MerchantVersion != paymentConfigurationID(p) {
		return ErrInvalid
	}
	return s.locked(ctx, rec.AccountID, func(c *pgxpool.Conn) error {
		cur, e := loadByID(ctx, c, rec.AccountID, rec.ID)
		if e != nil {
			return e
		}
		rec = cur
		newReceipt := rec.Receipt == nil
		if !newReceipt {
			if rec.Receipt.TransactionID != r.TransactionID || rec.Receipt.Amount != r.Amount {
				return ErrConflict
			}
			if rec.PaymentStatus != "PAID" || rec.OperationID != "" {
				return nil
			}
		} else {
			late := rec.PaymentStatus == "CLOSED" || !s.now().Before(rec.ExpiresAt) ||
				(!r.PaidAt.IsZero() && (!r.PaidAt.Before(rec.ExpiresAt) || r.PaidAt.Before(rec.CreatedAt.Add(-time.Minute))))
			rec.Receipt = &r
			rec.PaymentStatus = "PAID"
			rec.Session = nil
			rec.ErrorCode = ""
			if late {
				rec.PaymentStatus = "REVIEW"
				rec.ErrorCode = "late_payment"
			}
		}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if newReceipt {
			if _, e = tx.Exec(ctx, "INSERT INTO cash_payment_receipts(checkout_id,provider,merchant_id,transaction_id,snapshot) VALUES($1,$2,$3,$4,$5)", rec.ID, rec.Provider, rec.MerchantID, r.TransactionID, encode(r)); e != nil {
				return dbErr(e)
			}
		}
		if rec.PaymentStatus == "PAID" {
			destination := rec.ProductID
			if rec.WalletTarget == "unified" {
				destination = ""
			}
			op, err := s.wallet.CreditCashTopupTx(ctx, tx, rec.AccountID, rec.ActorID, destination, rec.Amount, rec.ID, r.TransactionID)
			if err != nil {
				return err
			}
			rec.OperationID = op.ID
		}
		if _, e = tx.Exec(ctx, "UPDATE cash_checkouts SET snapshot=$2 WHERE id=$1", rec.ID, encode(rec)); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
}

// Reconcile queries the provider for a checkout that the payer claims to have
// paid but for which no notification arrived. On a paid result it follows the
// same accept path.
func (s *Service) Reconcile(ctx context.Context, actor, account, id string) (Checkout, error) {
	rec, e := loadByID(ctx, s.pool, account, id)
	if e != nil {
		return Checkout{}, e
	}
	if e = s.finance(ctx, s.pool, actor, account); e != nil {
		return Checkout{}, e
	}
	if rec.Receipt != nil {
		return rec.Checkout, nil
	}
	providers, e := s.notificationProviders(ctx, rec.Provider)
	if e != nil {
		return Checkout{}, e
	}
	var p payments.Provider
	for _, cand := range providers {
		if cand.ID() == rec.Provider && cand.MerchantID() == rec.MerchantID && paymentConfigurationID(cand) == rec.MerchantVersion {
			p = cand
			break
		}
	}
	if p == nil {
		return Checkout{}, ErrPaymentUnavailable
	}
	result, e := p.Query(ctx, rec.ID)
	if e != nil {
		return Checkout{}, e
	}
	if result.Status == "PAID" {
		if e = s.accept(ctx, p, rec, result); e != nil {
			return Checkout{}, e
		}
	} else if result.Status == "CLOSED" || result.Status == "FAILED" {
		return s.recordClosed(ctx, account, id)
	}
	cur, e := loadByID(ctx, s.pool, account, id)
	if e != nil {
		return Checkout{}, e
	}
	return cur.Checkout, nil
}

// Close requests provider closure of an unpaid checkout.
func (s *Service) Close(ctx context.Context, actor, account, id string) (Checkout, error) {
	rec, e := loadByID(ctx, s.pool, account, id)
	if e != nil {
		return Checkout{}, e
	}
	if e = s.finance(ctx, s.pool, actor, account); e != nil {
		return Checkout{}, e
	}
	if rec.PaymentStatus == "PAID" || rec.Receipt != nil {
		return Checkout{}, fmt.Errorf("cannot close a paid checkout")
	}
	providers, e := s.notificationProviders(ctx, rec.Provider)
	if e != nil {
		return Checkout{}, e
	}
	var p payments.Provider
	for _, cand := range providers {
		if cand.ID() == rec.Provider && cand.MerchantID() == rec.MerchantID {
			p = cand
			break
		}
	}
	if p == nil {
		return Checkout{}, ErrPaymentUnavailable
	}
	if e = p.Close(ctx, rec.ID); e != nil {
		return Checkout{}, e
	}
	return s.recordClosed(ctx, account, id)
}

// A verified receipt can arrive while the close or query request is in
// flight. Reload under the same account lock as accept before changing state.
func (s *Service) recordClosed(ctx context.Context, account, id string) (Checkout, error) {
	var out Checkout
	err := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		rec, err := loadByID(ctx, c, account, id)
		if err != nil {
			return err
		}
		if rec.Receipt == nil && rec.PaymentStatus != "PAID" {
			rec.PaymentStatus = "CLOSED"
			rec.Session = nil
			if _, err = c.Exec(ctx, "UPDATE cash_checkouts SET snapshot=$2 WHERE id=$1", id, encode(rec)); err != nil {
				return err
			}
		}
		out = rec.Checkout
		return nil
	})
	return out, err
}
