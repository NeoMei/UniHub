package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

func (s *Service) Subscriptions(ctx context.Context, actor, account string) ([]Subscription, error) {
	if e := s.member(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	return list[Subscription](ctx, s.pool, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 ORDER BY product_id`, account)
}
func (s *Service) Orders(ctx context.Context, actor, account string) ([]Order, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	return list[Order](ctx, s.pool, `SELECT snapshot FROM billing_orders WHERE account_id=$1 ORDER BY (snapshot->>'created_at')::timestamptz DESC,id LIMIT 200`, account)
}

// Explicit product policies govern new add-on purchases. The implicit version
// zero profile preserves historical shared-pool sales; paid order recovery never
// re-enters this check. A membership in another product cannot satisfy it.
func (s *Service) requireAddonSubscriptionOn(ctx context.Context, q accounts.Querier, offer Offer, account string, now time.Time) error {
	if offer.Kind != "addon" {
		return nil
	}
	profile, err := s.ProfileOn(ctx, q, offer.ProductID)
	if err != nil {
		return err
	}
	if offer.CreditScope == ProductCreditScopeV1 {
		_, err = activeSubscriptionOn(ctx, q, account, offer.ProductID, now)
		return err
	}
	if profile.Version == 0 || (!profile.Usage.RequireSubscription && !profile.Usage.RequireSubscriptionForCredits) {
		return nil
	}
	sub, err := load[Subscription](ctx, q, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND product_id=$2`, account, offer.ProductID)
	if errors.Is(err, accounts.ErrNotFound) {
		return ErrSubscriptionRequired
	}
	if err != nil {
		return err
	}
	if sub.Status != "active" || !now.Before(sub.PaidThrough) {
		return ErrSubscriptionRequired
	}
	return nil
}

func (s *Service) Purchase(ctx context.Context, actor, account string, r PurchaseRequest) (Order, error) {
	var out Order
	if !keyOK(r.Key) {
		return out, ErrInvalid
	}
	if e := s.newPurchaseReady(ctx, actor, account, r.Key, r.OfferID, ""); e != nil {
		return out, e
	}
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		h := hash([]string{"purchase", actor, r.OfferID})
		old, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND request_key=$2`, account, r.Key)
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
		if e = s.commercialClear(ctx, c, account); e != nil {
			return e
		}
		if e = s.clear(ctx, c, account); e != nil {
			return e
		}
		offer, e := s.buyable(ctx, c, r.OfferID, account)
		if e != nil {
			return e
		}
		now := s.now().UTC()
		if e = s.requireAddonSubscriptionOn(ctx, c, offer, account, now); e != nil {
			return e
		}
		o := Order{ID: id("bo_"), AccountID: account, ActorID: actor, ProductID: offer.ProductID, Kind: offer.Kind, Offer: offer, Amount: offer.Price, PaymentStatus: "PENDING", FulfillmentStatus: "PENDING", Start: now, CreatedAt: now, GrantAmount: offer.MonthlyCredit}
		if offer.Kind == "membership" {
			sub, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND product_id=$2`, account, offer.ProductID)
			if e == nil && sub.Status == "active" && now.Before(sub.PaidThrough) {
				return ErrConflict
			}
			if e != nil && !errors.Is(e, accounts.ErrNotFound) {
				return e
			}
			o.SubscriptionID = id("sub_")
			o.End = o.Offer.monthAnchor(now, o.Offer.membershipMonths())
			o.PeriodStart, o.PeriodEnd, _ = o.Offer.currentPeriod(now, o.End, now)
		} else {
			o.End = offer.monthAnchor(now, offer.ValidityMonths)
			o.PeriodStart = now
			o.PeriodEnd = o.End
		}
		out, e = s.payLocked(ctx, c, o, r.Key, h, nil)
		if e != nil {
			return e
		}
		return s.fulfillAndReadLocked(ctx, c, &out, now)
	})
	return out, e
}
func (s *Service) payLocked(ctx context.Context, c *pgxpool.Conn, o Order, key, digest string, renewal *Authorization) (Order, error) {
	o.PaymentSource = "wallet"
	if e := freezeProductCreditOrder(&o); e != nil {
		return o, e
	}
	amount, _ := integer(o.Amount, true)
	var fromProduct int64
	if o.CreditScope == "" {
		balance, e := s.ledger.Balance(ctx, wallet.Address(o.AccountID, o.ProductID), wallet.Asset)
		if e != nil {
			return o, e
		}
		if balance < 0 {
			return o, ErrConflict
		}
		fromProduct = min(amount, balance)
	}
	shortfall := amount - fromProduct
	var funding *Authorization
	if shortfall > 0 && o.CreditScope == "" {
		a, e := s.budget(ctx, c, o.ActorID, o.AccountID, o.ProductID, "funding", shortfall, s.now().UTC())
		if e != nil {
			if errors.Is(e, accounts.ErrForbidden) {
				return o, ledger.ErrInsufficientFunds
			}
			return o, e
		}
		funding = &a
		o.FundingAuthorizationVersion = a.Version
	}
	o.ProductFunding = str(fromProduct)
	o.UnifiedFunding = str(shortfall)
	posts := []ledger.Posting{}
	source := wallet.Address(o.AccountID, o.ProductID)
	if o.CreditScope == ProductCreditScopeV1 {
		source = wallet.Address(o.AccountID, "")
	} else if shortfall > 0 {
		posts = append(posts, ledger.Posting{Source: wallet.Address(o.AccountID, ""), Destination: source, Asset: wallet.Asset, Amount: shortfall})
	}
	orderPosts, e := orderPaymentPostings(o, source)
	if e != nil {
		return o, e
	}
	posts = append(posts, orderPosts...)
	tx, e := c.Begin(ctx)
	if e != nil {
		return o, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('unihub:billing-rate-catalog',0))`); e != nil {
		return o, e
	}
	if e = lockBillingProduct(ctx, tx, o.ProductID); e != nil {
		return o, e
	}
	if e = s.offerForSaleOn(ctx, tx, o.Offer); e != nil {
		return o, e
	}
	if e = s.requireSaleReadyOn(ctx, tx, o.Offer); e != nil {
		return o, e
	}
	if e = s.requireAddonSubscriptionOn(ctx, tx, o.Offer, o.AccountID, s.now().UTC()); e != nil {
		return o, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,$4,$5,$6)`, o.ID, o.AccountID, o.ActorID, key, digest, encode(o))
	if e != nil {
		return o, e
	}
	if funding != nil {
		if e = updateAuth(ctx, tx, o.AccountID, *funding); e != nil {
			return o, e
		}
	}
	if renewal != nil {
		if e = updateAuth(ctx, tx, o.AccountID, *renewal); e != nil {
			return o, e
		}
	}
	paid := o
	paid.PaymentStatus = "PAID"
	cmd, e := s.createCommand(ctx, tx, o.AccountID, o.ActorID, "payment", o.ID, posts, effect{Order: &paid, Finance: true})
	if e != nil {
		return o, e
	}
	if e = tx.Commit(ctx); e != nil {
		return o, e
	}
	e = s.runLocked(ctx, c, cmd.ID)
	saved, readErr := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, o.ID)
	if readErr != nil {
		return o, readErr
	}
	return saved, e
}
func (s *Service) resumeOrderLocked(ctx context.Context, c *pgxpool.Conn, o *Order) error {
	var cmdID string
	e := c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind='payment' AND request_key=$2`, o.AccountID, o.ID).Scan(&cmdID)
	if e != nil {
		return e
	}
	if e = s.runLocked(ctx, c, cmdID); e != nil {
		return e
	}
	return s.fulfillAndReadLocked(ctx, c, o, s.now().UTC())
}
func (s *Service) fulfillAndReadLocked(ctx context.Context, c *pgxpool.Conn, o *Order, now time.Time) error {
	e := s.fulfillLocked(ctx, c, o.ID, now)
	saved, re := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, o.ID)
	if re == nil {
		*o = saved
	}
	if e != nil {
		return e
	}
	return re
}
func (s *Service) fulfillLocked(ctx context.Context, c *pgxpool.Conn, orderID string, now time.Time) error {
	o, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE id=$1`, orderID)
	if e != nil {
		return e
	}
	if o.PaymentStatus != "PAID" || o.FulfillmentStatus != "PENDING" || refundBlocks(o.RefundStatus) {
		return nil
	}
	var existing string
	e = c.QueryRow(ctx, `SELECT id FROM billing_commands WHERE account_id=$1 AND kind='fulfillment' AND request_key=$2`, o.AccountID, o.ID).Scan(&existing)
	if e == nil {
		return s.runLocked(ctx, c, existing)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if o.PaymentSource == "external" {
		reviewed, e := s.externalFulfillmentReviewLocked(ctx, c, o, now)
		if e != nil || reviewed {
			return e
		}
	}
	if e = s.clearExceptOrder(ctx, c, o.AccountID, o.ID); e != nil {
		return e
	}
	var sub *Subscription
	start, end := o.PeriodStart, o.PeriodEnd
	grant := o.GrantAmount
	var replacedOrder *Order
	var replacedBatches []Batch
	posts := []ledger.Posting{}
	if o.Kind == "membership" || o.Kind == "renewal" {
		old, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1`, o.SubscriptionID)
		if e != nil && !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		anchor := o.Start
		if o.Kind == "renewal" && (o.CreditScope != ProductCreditScopeV1 || !o.Start.After(old.PaidThrough)) {
			anchor = old.Anchor
		}
		x := Subscription{ID: o.SubscriptionID, AccountID: o.AccountID, ProductID: o.ProductID, Offer: o.Offer, Version: old.Version + 1, Anchor: anchor, PaidThrough: o.End, Status: "active"}
		sub = &x
		var ok bool
		start, end, ok = o.Offer.currentPeriod(anchor, o.End, now)
		if !ok {
			grant = "0"
		}
	}
	if o.ProductCreditUpgrade != nil {
		old, batches, withdrawalPosts, e := s.prepareProductCreditUpgrade(ctx, c, &o, o.Start)
		if e != nil {
			return e
		}
		replacedOrder, replacedBatches = &old, batches
		posts = append(posts, withdrawalPosts...)
		sub = &Subscription{ID: o.SubscriptionID, AccountID: o.AccountID, ProductID: o.ProductID,
			Offer: o.Offer, Version: 1, Anchor: o.Start, PaidThrough: o.End, Status: "active"}
		var active bool
		start, end, active = o.Offer.currentPeriod(o.Start, o.End, now)
		if !active {
			grant = "0"
		}
	} else if o.Kind == "upgrade" {
		old, e := load[Subscription](ctx, c, `SELECT snapshot FROM billing_subscriptions WHERE id=$1`, o.SubscriptionID)
		if e != nil {
			return e
		}
		old.Offer = o.Offer
		old.Version++
		sub = &old
		if !now.Before(end) {
			grant = "0"
		}
	}
	if o.Kind == "addon" && !now.Before(end) {
		grant = "0"
	}
	o.FulfillmentStatus = "FULFILLED"
	eff := effect{Order: &o, ReplacedOrder: replacedOrder, Subscription: sub, Batches: replacedBatches}
	if o.PaymentSource == "external" {
		cashPosts, e := orderPaymentPostings(o, externalReceived(o.CheckoutID))
		if e != nil {
			return e
		}
		posts = append(posts, cashPosts...)
	}
	n, _ := integer(grant, false)
	if n > 0 {
		kind := "base"
		source := o.SubscriptionID + ":" + start.Format(time.RFC3339Nano) + ":base"
		if o.Kind == "upgrade" && o.ProductCreditUpgrade == nil {
			kind = "upgrade"
			source = o.ID + ":" + start.Format(time.RFC3339Nano) + ":upgrade"
		}
		if o.Kind == "addon" {
			kind = "addon"
			source = o.ID + ":addon"
		}
		b := Batch{ID: id("batch_"), CreditScope: o.CreditScope, AccountID: o.AccountID, ProductID: o.ProductID, OrderID: o.ID, SubscriptionID: o.SubscriptionID, SourceKey: source, Kind: kind, Original: grant, StartsAt: start, ExpiresAt: end, CreatedAt: now}
		eff.Batches = append(eff.Batches, b)
		posts = append(posts, ledger.Posting{Source: "world", Destination: BatchAddress(b.ID, "available"), Asset: CreditAsset, Amount: n})
	}
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	cmd, e := s.createCommand(ctx, tx, o.AccountID, o.ActorID, "fulfillment", o.ID, posts, eff)
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return s.runLocked(ctx, c, cmd.ID)
}
func (s *Service) monthlyLocked(ctx context.Context, c *pgxpool.Conn, sub Subscription, now time.Time) error {
	n, e := integer(sub.Offer.MonthlyCredit, false)
	if e != nil {
		return e
	}
	if n == 0 {
		return nil
	}
	start, end, ok := sub.Offer.currentPeriod(sub.Anchor, sub.PaidThrough, now)
	if !ok {
		return nil
	}
	source := sub.ID + ":" + start.Format(time.RFC3339Nano) + ":base"
	var exists bool
	if e := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_batches WHERE source_key=$1)`, source).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return nil
	}
	var orderID string
	e = c.QueryRow(ctx, `SELECT id FROM billing_orders WHERE account_id=$1 AND snapshot->>'subscription_id'=$2 AND snapshot->>'payment_status'='PAID' AND COALESCE(snapshot->>'refund_status','') IN ('','REJECTED') AND (snapshot->>'kind'<>'upgrade' OR snapshot ? 'product_credit_upgrade') AND (snapshot->>'start')::timestamptz <= $3 AND (snapshot->>'end')::timestamptz > $3 ORDER BY (snapshot->>'created_at')::timestamptz DESC LIMIT 1`, sub.AccountID, sub.ID, start).Scan(&orderID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	b := Batch{ID: id("batch_"), CreditScope: sub.Offer.CreditScope, AccountID: sub.AccountID, ProductID: sub.ProductID, OrderID: orderID, SubscriptionID: sub.ID, SourceKey: source, Kind: "base", Original: sub.Offer.MonthlyCredit, StartsAt: start, ExpiresAt: end, CreatedAt: now}
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	cmd, e := s.createCommand(ctx, tx, sub.AccountID, "worker", "monthly", source, []ledger.Posting{{Source: "world", Destination: BatchAddress(b.ID, "available"), Asset: CreditAsset, Amount: n}}, effect{Batches: []Batch{b}})
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return s.runLocked(ctx, c, cmd.ID)
}
func (s *Service) renewLocked(ctx context.Context, c *pgxpool.Conn, sub Subscription, now time.Time, runtimeReady bool) error {
	if e := s.commercialClear(ctx, c, sub.AccountID); e != nil {
		return e
	}
	if sub.Status != "active" {
		return nil
	}
	a, e := load[Authorization](ctx, c, `SELECT snapshot FROM billing_authorizations WHERE account_id=$1 AND product_id=$2 AND purpose='renewal'`, sub.AccountID, sub.ProductID)
	if errors.Is(e, accounts.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	n, _ := integer(sub.Offer.Price, true)
	a, e = s.budget(ctx, c, a.ActorID, sub.AccountID, sub.ProductID, "renewal", n, now)
	if e != nil {
		return e
	}
	months := sub.Offer.membershipMonths()
	loc := sub.Offer.billingLocation()
	m := monthsBetween(sub.Anchor.In(loc), sub.PaidThrough.In(loc))
	end := sub.Offer.monthAnchor(sub.Anchor, m+months)
	scoped := sub.Offer.CreditScope == ProductCreditScopeV1
	if !scoped && !now.Before(end) {
		return nil
	}
	key := "renew:" + sub.ID + ":" + sub.PaidThrough.Format(time.RFC3339Nano)
	retrying := false
	for {
		old, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND request_key=$2`, sub.AccountID, key)
		if errors.Is(e, accounts.ErrNotFound) {
			break
		}
		if e != nil {
			return e
		}
		if old.PaymentStatus != "REJECTED" {
			return s.resumeOrderLocked(ctx, c, &old)
		}
		if !scoped {
			return nil
		}
		// A definitively rejected attempt can be retried, preserving its record.
		retrying = true
		key = "renew-retry:" + old.ID
	}
	if scoped && !runtimeReady {
		return ErrPending
	}
	if retrying {
		balance, e := s.ledger.Balance(ctx, wallet.Address(sub.AccountID, ""), CashAsset)
		if e != nil {
			return e
		}
		if balance < n {
			return ledger.ErrInsufficientFunds
		}
	}
	if _, e = s.buyable(ctx, c, sub.Offer.ID, sub.AccountID); e != nil {
		return e
	}
	start, anchor := sub.PaidThrough, sub.Anchor
	if scoped && (retrying || !now.Before(sub.Offer.monthAnchor(sub.Anchor, m+1))) {
		// Start a full term after a rejected renewal or a missed grant window.
		start, anchor = now, now
		end = sub.Offer.monthAnchor(anchor, months)
	}
	periodStart, periodEnd, _ := sub.Offer.currentPeriod(anchor, end, now)
	o := Order{ID: id("bo_"), AccountID: sub.AccountID, ActorID: a.ActorID, ProductID: sub.ProductID, Kind: "renewal", Offer: sub.Offer, Amount: sub.Offer.Price, PaymentStatus: "PENDING", FulfillmentStatus: "PENDING", SubscriptionID: sub.ID, Start: start, End: end, PeriodStart: periodStart, PeriodEnd: periodEnd, GrantAmount: sub.Offer.MonthlyCredit, CreatedAt: now, RenewalAuthorizationVersion: a.Version}
	paid, e := s.payLocked(ctx, c, o, key, hash(key), &a)
	if e != nil {
		return e
	}
	return s.fulfillAndReadLocked(ctx, c, &paid, now)
}
