package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/wallet"
)

// CreateCheckout persists the immutable contract and original merchant order
// before issuing a payment artifact. Replays never invoke Create again.
func (s *Service) CreateCheckout(ctx context.Context, actor, account string, r CheckoutRequest) (Checkout, error) {
	var x checkoutRecord
	fresh := false
	if !keyOK(r.Key) || (r.OfferID == "") == (r.QuoteID == "") {
		return x.Checkout, ErrInvalid
	}
	p, e := s.checkoutProvider(r.Provider, "")
	if e != nil {
		return x.Checkout, e
	}
	// Probe readiness without holding an account lock. Replays recover accepted
	// obligations even when new sales are disabled afterwards.
	if e = s.finance(ctx, s.pool, actor, account); e != nil {
		return x.Checkout, e
	}
	var replay bool
	if e = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_checkouts WHERE account_id=$1 AND request_key=$2)`, account, r.Key).Scan(&replay); e != nil {
		return x.Checkout, e
	}
	if !replay {
		var candidate Offer
		if r.OfferID != "" {
			candidate, e = load[Offer](ctx, s.pool, `SELECT snapshot FROM billing_offers WHERE id=$1`, r.OfferID)
		} else {
			var q Quote
			q, e = load[Quote](ctx, s.pool, `SELECT snapshot FROM billing_quotes WHERE id=$1 AND account_id=$2`, r.QuoteID, account)
			candidate = q.Offer
		}
		if e != nil {
			return x.Checkout, e
		}
		if e = s.externalCheckoutReady(ctx, actor, account, candidate); e != nil {
			return x.Checkout, e
		}
	}
	e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		digest := hash([]string{actor, r.Provider, r.OfferID, r.QuoteID})
		old, e := load[checkoutRecord](ctx, c, `SELECT snapshot FROM billing_checkouts WHERE account_id=$1 AND request_key=$2`, account, r.Key)
		if e == nil {
			if old.RequestDigest != digest {
				return ErrConflict
			}
			x = old
			return nil
		}
		if !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		if e = s.commercialClear(ctx, c, account); e != nil {
			return e
		}
		if e = s.clear(ctx, c, account); e != nil {
			return e
		}
		now := s.now().UTC()
		expires := now.Add(15 * time.Minute)
		var offer Offer
		var quote *Quote
		var sub Subscription
		if r.QuoteID != "" {
			q, e := load[Quote](ctx, c, `SELECT snapshot FROM billing_quotes WHERE account_id=$1 AND id=$2`, account, r.QuoteID)
			if e != nil {
				return e
			}
			if q.PaymentSource != "external" || q.RoundingRule != "cny_cent_ceil_v1" {
				return ErrInvalid
			}
			sub, e = load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND id=$2`, account, q.SubscriptionID)
			if e != nil {
				return e
			}
			start, _, ok := CurrentPeriod(sub.Anchor, sub.PaidThrough, now)
			if !ok || sub.Status != "active" || !now.Before(q.ExpiresAt) || sub.Version != q.SubscriptionVersion || !start.Equal(q.PeriodStart) {
				return ErrQuoteInvalidated
			}
			offer, e = s.buyable(ctx, c, q.Offer.ID, account)
			if e != nil {
				return e
			}
			if hash(offer) != hash(q.Offer) {
				return ErrQuoteInvalidated
			}
			quote = &q
			if q.ExpiresAt.Before(expires) {
				expires = q.ExpiresAt
			}
		} else {
			offer, e = s.buyable(ctx, c, r.OfferID, account)
			if e != nil {
				return e
			}
		}
		if e = s.requireAddonSubscriptionOn(ctx, c, offer, account, now); e != nil {
			return e
		}
		amount := offer.Price
		if quote != nil {
			amount = quote.Amount
		}
		n, e := integer(amount, true)
		if e != nil || n%10000 != 0 {
			return ErrInvalid
		}
		o := Order{ID: id("bo_"), AccountID: account, ActorID: actor, ProductID: offer.ProductID, Kind: offer.Kind, Offer: offer, Amount: amount, PaymentSource: "external", ProductFunding: "0", UnifiedFunding: "0", PaymentStatus: "PENDING", FulfillmentStatus: "PENDING", Start: now, CreatedAt: now, GrantAmount: offer.MonthlyCredit}
		if quote != nil {
			o.Kind = "upgrade"
			o.SubscriptionID = sub.ID
			o.End = sub.PaidThrough
			o.PeriodStart = quote.PeriodStart
			o.PeriodEnd = quote.PeriodEnd
			o.GrantAmount = quote.GrantAmount
		} else if offer.Kind == "membership" {
			active, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND product_id=$2`, account, offer.ProductID)
			if e == nil && active.Status == "active" && now.Before(active.PaidThrough) {
				return ErrConflict
			}
			if e != nil && !errors.Is(e, accounts.ErrNotFound) {
				return e
			}
			o.SubscriptionID = id("sub_")
			months := 1
			if offer.Period == "year" {
				months = 12
			}
			o.End = MonthAnchor(now, months)
			o.PeriodStart, o.PeriodEnd, _ = CurrentPeriod(now, o.End, now)
		} else {
			o.End = MonthAnchor(now, offer.ValidityMonths)
			o.PeriodStart = now
			o.PeriodEnd = o.End
		}
		checkoutID := id("")
		o.CheckoutID = checkoutID
		x = checkoutRecord{Checkout: Checkout{ID: checkoutID, AccountID: account, ProductID: offer.ProductID, Provider: r.Provider, OfferName: offer.Name, Amount: amount, Currency: "CNY", PaymentStatus: "UNKNOWN", FulfillmentStatus: "NOT_STARTED", ExpiresAt: expires, CreatedAt: now}, ActorID: actor, MerchantID: p.MerchantID(), MerchantVersion: paymentConfigurationID(p), RequestDigest: digest, Order: o, Quote: quote}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('unihub:billing-rate-catalog',0))`); e != nil {
			return e
		}
		if e = lockBillingProduct(ctx, tx, offer.ProductID); e != nil {
			return e
		}
		if e = s.offerForSaleOn(ctx, tx, offer); e != nil {
			return e
		}
		if e = s.requireSaleReadyOn(ctx, tx, offer); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO billing_checkouts(id,account_id,product_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,$4,$5,$6)`, x.ID, account, offer.ProductID, r.Key, digest, encode(x)); e != nil {
			return e
		}
		if e = event(ctx, tx, account, actor, x.ID, "checkout.created", map[string]string{"provider": r.Provider, "amount": amount}); e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		fresh = true
		return nil
	})
	if e != nil {
		return x.Checkout, e
	}
	if !fresh {
		return s.checkoutView(ctx, x)
	}
	session, createErr := p.Create(ctx, payments.Request{OrderID: x.ID, Amount: x.Amount, Description: x.OfferName, ExpiresAt: x.ExpiresAt})
	e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
		current, e := readCheckout(ctx, c, account, x.ID)
		if e != nil {
			return e
		}
		x = current
		if x.Receipt != nil || x.PaymentStatus == "CLOSED" {
			return nil
		}
		if createErr != nil {
			x.PaymentStatus = "UNKNOWN"
			x.ErrorCode = "provider_create_unknown"
		} else if (session.Kind != "qr" && session.Kind != "redirect") || session.URL == "" || session.ExpiresAt.After(x.ExpiresAt) || !session.ExpiresAt.After(x.CreatedAt) {
			x.PaymentStatus = "UNKNOWN"
			x.ErrorCode = "provider_session_invalid"
		} else {
			x.PaymentStatus = "PENDING"
			x.Session = &session
			x.ErrorCode = ""
		}
		return saveCheckout(ctx, c, x)
	})
	if e != nil {
		return x.Checkout, e
	}
	return s.checkoutView(ctx, x)
}

func (s *Service) ReconcileCheckout(ctx context.Context, actor, account, checkout string) (Checkout, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Checkout{}, e
	}
	return s.reconcileCheckout(ctx, account, checkout)
}
func (s *Service) reconcileCheckout(ctx context.Context, account, checkout string) (Checkout, error) {
	x, e := readCheckout(ctx, s.pool, account, checkout)
	if e != nil {
		return x.Checkout, e
	}
	if x.Receipt != nil {
		return s.settleCheckout(ctx, account, checkout)
	}
	p, e := s.checkoutBoundProvider(x)
	if e != nil {
		return x.Checkout, e
	}
	result, queryErr := p.Query(ctx, x.ID)
	if queryErr != nil {
		return s.checkoutUnknown(ctx, x, "provider_query_unknown")
	}
	if result.OrderID != x.ID {
		return x.Checkout, ErrInvalid
	}
	if result.Status == "PAID" {
		if e = s.acceptCheckoutReceipt(ctx, p, x, result); e != nil {
			return x.Checkout, e
		}
		return s.settleCheckout(ctx, account, checkout)
	}
	e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
		current, e := readCheckout(ctx, c, account, checkout)
		if e != nil {
			return e
		}
		x = current
		if x.Receipt != nil {
			return nil
		}
		switch result.Status {
		case "CLOSED":
			x.PaymentStatus = "CLOSED"
			x.Session = nil
		case "NOT_FOUND":
			if !s.now().Before(x.ExpiresAt) {
				x.PaymentStatus = "CLOSED"
				x.Session = nil
			} else {
				x.PaymentStatus = "UNKNOWN"
			}
		case "PENDING":
			if x.PaymentStatus != "CLOSED" {
				x.PaymentStatus = "PENDING"
			}
		default:
			return ErrInvalid
		}
		x.ErrorCode = ""
		return saveCheckout(ctx, c, x)
	})
	if e != nil {
		return x.Checkout, e
	}
	return s.checkoutView(ctx, x)
}
func (s *Service) checkoutUnknown(ctx context.Context, x checkoutRecord, code string) (Checkout, error) {
	e := s.locked(ctx, x.AccountID, func(c *pgxpool.Conn) error {
		current, e := readCheckout(ctx, c, x.AccountID, x.ID)
		if e != nil {
			return e
		}
		x = current
		if x.Receipt != nil || x.PaymentStatus == "CLOSED" {
			return nil
		}
		x.PaymentStatus = "UNKNOWN"
		x.ErrorCode = code
		return saveCheckout(ctx, c, x)
	})
	if e != nil {
		return x.Checkout, e
	}
	return s.checkoutView(ctx, x)
}
func (s *Service) CloseCheckout(ctx context.Context, actor, account, checkout string) (Checkout, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Checkout{}, e
	}
	// Query first. A close success without a subsequent verified terminal query is
	// never sufficient to release the commercial lock.
	v, e := s.reconcileCheckout(ctx, account, checkout)
	if e != nil {
		return v, e
	}
	if v.PaymentStatus == "PAID" || v.PaymentStatus == "REVIEW" || v.PaymentStatus == "CLOSED" {
		return v, nil
	}
	x, e := readCheckout(ctx, s.pool, account, checkout)
	if e != nil {
		return v, e
	}
	p, e := s.checkoutBoundProvider(x)
	if e != nil {
		return v, e
	}
	e = s.locked(ctx, account, func(c *pgxpool.Conn) error {
		current, e := readCheckout(ctx, c, account, checkout)
		if e != nil {
			return e
		}
		x = current
		x.CloseRequested = true
		return saveCheckout(ctx, c, x)
	})
	if e != nil {
		return v, e
	}
	if e = p.Close(ctx, checkout); e != nil {
		return s.checkoutUnknown(ctx, x, "provider_close_unknown")
	}
	return s.reconcileCheckout(ctx, account, checkout)
}

// NotifyCheckout acknowledges a verified receipt only after its durable unique
// binding. Fulfillment recovery is independent of callback delivery success.
func (s *Service) NotifyCheckout(ctx context.Context, provider string, headers http.Header, body []byte) error {
	for _, p := range s.notificationProviders(provider) {
		result, e := p.VerifyNotification(headers, body)
		if e != nil || result.Status != "PAID" {
			continue
		}
		x, e := load[checkoutRecord](ctx, s.pool, `SELECT snapshot FROM billing_checkouts WHERE id=$1`, result.OrderID)
		if e != nil {
			return e
		}
		if x.Provider != provider || x.MerchantID != p.MerchantID() || x.MerchantVersion != paymentConfigurationID(p) {
			continue
		}
		if e = s.acceptCheckoutReceipt(ctx, p, x, result); e != nil {
			return e
		}
		_, _ = s.settleCheckout(ctx, x.AccountID, x.ID)
		return nil
	}
	return payments.ErrVerification
}

func (s *Service) acceptCheckoutReceipt(ctx context.Context, p payments.Provider, x checkoutRecord, r payments.Result) error {
	if r.OrderID != x.ID || r.Amount != x.Amount || r.Currency != "CNY" || r.Status != "PAID" || r.TransactionID == "" || len(r.TransactionID) > 256 || x.Provider != p.ID() || x.MerchantID != p.MerchantID() || x.MerchantVersion != paymentConfigurationID(p) {
		return ErrInvalid
	}
	return s.locked(ctx, x.AccountID, func(c *pgxpool.Conn) error {
		current, e := readCheckout(ctx, c, x.AccountID, x.ID)
		if e != nil {
			return e
		}
		x = current
		if x.Receipt != nil {
			if x.Receipt.TransactionID != r.TransactionID || x.Receipt.Amount != r.Amount {
				return ErrConflict
			}
			return nil
		}
		wasClosed := x.PaymentStatus == "CLOSED"
		x.Receipt = &r
		x.PaymentStatus = "PAID"
		x.FulfillmentStatus = "PENDING"
		x.Session = nil
		x.ErrorCode = ""
		// Both the observation and provider timestamp must fit the frozen contract.
		if wasClosed || !s.now().Before(x.ExpiresAt) || !r.PaidAt.IsZero() && (!r.PaidAt.Before(x.ExpiresAt) || r.PaidAt.Before(x.CreatedAt.Add(-time.Minute))) {
			x.PaymentStatus = "REVIEW"
			x.FulfillmentStatus = "REVIEW"
			x.ErrorCode = "late_payment"
		}
		if x.Quote != nil {
			sub, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1 AND account_id=$2`, x.Quote.SubscriptionID, x.AccountID)
			if e != nil && !errors.Is(e, accounts.ErrNotFound) {
				return e
			}
			if errors.Is(e, accounts.ErrNotFound) || sub.Status != "active" || sub.Version != x.Quote.SubscriptionVersion {
				x.PaymentStatus = "REVIEW"
				x.FulfillmentStatus = "REVIEW"
				x.ErrorCode = "quote_invalidated"
			}
		}
		o := x.Order
		o.PaymentStatus = "PAID"
		o.FulfillmentStatus = x.FulfillmentStatus
		o.ProviderTransactionID = r.TransactionID
		x.OrderID = o.ID
		x.Order = o
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if _, e = tx.Exec(ctx, `INSERT INTO billing_payment_receipts(checkout_id,provider,merchant_id,transaction_id,snapshot) VALUES($1,$2,$3,$4,$5)`, x.ID, x.Provider, x.MerchantID, r.TransactionID, encode(r)); e != nil {
			return e
		}
		// Receipt and original external payment command are committed together. The
		// command posts to received-but-unfulfilled cash, never either user wallet.
		pending := o
		pending.PaymentStatus = "PENDING"
		if _, e = tx.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,$4,$5,$6)`, o.ID, o.AccountID, o.ActorID, "external:"+x.ID, x.RequestDigest, encode(pending)); e != nil {
			return e
		}
		n, _ := integer(x.Amount, true)
		if _, e = s.createCommand(ctx, tx, o.AccountID, o.ActorID, "external_payment", x.ID, []ledger.Posting{{Source: "world", Destination: externalReceived(x.ID), Asset: wallet.Asset, Amount: n}}, effect{Order: &o}); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE billing_checkouts SET snapshot=$2 WHERE id=$1`, x.ID, encode(x)); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
}
func externalReceived(checkout string) string { return "commercial:external:unfulfilled:" + checkout }
func (s *Service) settleCheckout(ctx context.Context, account, checkout string) (Checkout, error) {
	var x checkoutRecord
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		var e error
		x, e = readCheckout(ctx, c, account, checkout)
		if e != nil {
			return e
		}
		if x.Receipt == nil {
			return nil
		}
		var commandID string
		if e = c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind='external_payment' AND request_key=$2`, account, checkout).Scan(&commandID); e != nil {
			return e
		}
		if e = s.runLocked(ctx, c, commandID); e != nil {
			return e
		}
		if x.RefundStatus == "" && x.PaymentStatus == "PAID" {
			if e = s.fulfillLocked(ctx, c, x.OrderID, s.now().UTC()); e != nil {
				return e
			}
		}
		x, e = readCheckout(ctx, c, account, checkout)
		if e != nil {
			return e
		}
		o, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, x.OrderID)
		if e != nil {
			return e
		}
		x.FulfillmentStatus = o.FulfillmentStatus
		if x.FulfillmentStatus == "CANCELED" {
			x.FulfillmentStatus = "NOT_STARTED"
		}
		if o.RefundStatus == "REFUNDED" {
			x.RefundStatus = "SUCCEEDED"
		}
		return saveCheckout(ctx, c, x)
	})
	if e != nil {
		return x.Checkout, e
	}
	return s.checkoutView(ctx, x)
}

// RecoverCheckouts performs gateway IO outside account locks. The host invokes
// it for worker recovery independently of ordinary command/usage maintenance.
func (s *Service) RecoverCheckouts(ctx context.Context) error {
	xs, e := list[checkoutRecord](ctx, s.pool, `SELECT x.snapshot FROM billing_checkouts x
		WHERE x.snapshot->>'payment_status' IN ('CREATED','PENDING','UNKNOWN')
		OR (x.snapshot->>'payment_status' IN ('PAID','REVIEW')
			AND x.snapshot->>'fulfillment_status' IN ('PENDING','REVIEW')
			AND COALESCE(x.snapshot->>'refund_status','') NOT IN ('SUCCEEDED','REVIEW')
			AND ((x.snapshot->>'payment_status'='PAID' AND x.snapshot->>'fulfillment_status'='PENDING')
				OR EXISTS(SELECT 1 FROM billing_commands c WHERE c.account_id=x.account_id AND c.kind='external_payment' AND c.request_key=x.id AND c.status NOT IN ('APPLIED','REJECTED','CANCELED'))))
		OR x.snapshot->>'refund_status'='PENDING'
		ORDER BY x.last_recovery_attempt NULLS FIRST,(x.snapshot->>'created_at')::timestamptz,x.id LIMIT 200`)
	if e != nil {
		return e
	}
	var failures error
	for _, x := range xs {
		if e = ctx.Err(); e != nil {
			return errors.Join(failures, e)
		}
		// Record each attempt before provider IO. Even a timed-out gateway then
		// rotates behind untouched work on the next bounded worker cycle.
		if _, e = s.pool.Exec(ctx, `UPDATE billing_checkouts SET last_recovery_attempt=clock_timestamp() WHERE id=$1`, x.ID); e != nil {
			return errors.Join(failures, e)
		}
		if x.RefundStatus == "PENDING" {
			_, e = s.recoverCheckoutRefund(ctx, x.AccountID, x.ID)
		} else {
			var v Checkout
			v, e = s.reconcileCheckout(ctx, x.AccountID, x.ID)
			if e == nil && x.CloseRequested && v.PaymentStatus != "CLOSED" && v.PaymentStatus != "PAID" && v.PaymentStatus != "REVIEW" {
				p, pe := s.checkoutBoundProvider(x)
				if pe == nil {
					pe = p.Close(ctx, x.ID)
				}
				if pe == nil {
					_, e = s.reconcileCheckout(ctx, x.AccountID, x.ID)
				}
			}
		}
		if e != nil {
			failures = errors.Join(failures, e)
		}
	}
	return failures
}

// A receipt may outlive its ledger outage. Before creating a new fulfillment
// command recheck the frozen deadline and subscription revision; once a command
// exists its immutable postings must be recovered instead of changed.
func (s *Service) externalFulfillmentReviewLocked(ctx context.Context, c *pgxpool.Conn, o Order, now time.Time) (bool, error) {
	x, e := readCheckout(ctx, c, o.AccountID, o.CheckoutID)
	if e != nil {
		return false, e
	}
	reason := ""
	if !now.Before(x.ExpiresAt) {
		reason = "fulfillment_deadline_elapsed"
	}
	if x.Quote != nil {
		sub, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1 AND account_id=$2`, x.Quote.SubscriptionID, x.AccountID)
		if e != nil && !errors.Is(e, accounts.ErrNotFound) {
			return false, e
		}
		if errors.Is(e, accounts.ErrNotFound) || sub.Status != "active" || sub.Version != x.Quote.SubscriptionVersion {
			reason = "quote_invalidated"
		}
	}
	if o.Kind == "membership" {
		sub, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND product_id=$2`, o.AccountID, o.ProductID)
		if e == nil && sub.Status == "active" && now.Before(sub.PaidThrough) {
			reason = "subscription_changed"
		}
		if e != nil && !errors.Is(e, accounts.ErrNotFound) {
			return false, e
		}
	}
	if reason == "" {
		return false, nil
	}
	x.PaymentStatus = "REVIEW"
	x.FulfillmentStatus = "REVIEW"
	x.ErrorCode = reason
	o.FulfillmentStatus = "REVIEW"
	tx, e := c.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer database.Rollback(tx)
	if e = saveOrder(ctx, tx, o); e != nil {
		return false, e
	}
	if _, e = tx.Exec(ctx, `UPDATE billing_checkouts SET snapshot=$2 WHERE id=$1`, x.ID, encode(x)); e != nil {
		return false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	return true, nil
}
