package billing

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

type AutoAddonPolicy struct {
	ProductID string `json:"product_id"`
	Enabled   bool   `json:"enabled"`
	Version   int64  `json:"version"`
	OfferID   string `json:"offer_id"`
	Trigger   string `json:"trigger"`
	Threshold string `json:"threshold"`
	ActorID   string `json:"actor_id"`
}

type AutoAddonPolicyRequest struct {
	Version   int64  `json:"expected_version"`
	Enabled   bool   `json:"enabled"`
	OfferID   string `json:"offer_id"`
	Trigger   string `json:"trigger"`
	Threshold string `json:"threshold"`
}

func (s *Service) ProductCreditAutoAddonPolicies(ctx context.Context, actor, account string) ([]AutoAddonPolicy, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	return list[AutoAddonPolicy](ctx, s.pool, `SELECT snapshot FROM billing_product_credit_auto_addon_policies WHERE account_id=$1 ORDER BY product_id`, account)
}

func (s *Service) SetProductCreditAutoAddon(ctx context.Context, actor, account, productID string, r AutoAddonPolicyRequest) (AutoAddonPolicy, error) {
	out := AutoAddonPolicy{ProductID: productID, ActorID: actor, Enabled: r.Enabled, OfferID: r.OfferID, Trigger: r.Trigger, Threshold: r.Threshold}
	if r.Version < 0 || r.Version == math.MaxInt64 || !billingProductID.MatchString(productID) {
		return out, ErrInvalid
	}
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		old, e := loadAutoAddonPolicy(ctx, c, account, productID)
		if e != nil && !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		if r.Version != old.Version {
			return ErrConflict
		}
		if r.Enabled {
			if e = validateAutoAddonPolicy(out); e != nil {
				return e
			}
			offer, e := s.buyable(ctx, c, r.OfferID, account)
			if e != nil {
				return e
			}
			if offer.ProductID != productID || offer.Kind != "addon" || offer.CreditScope != ProductCreditScopeV1 {
				return ErrInvalid
			}
		} else {
			out.OfferID, out.Trigger, out.Threshold = old.OfferID, old.Trigger, old.Threshold
			if e = product(ctx, c, productID); e != nil {
				return e
			}
		}
		out.Version = old.Version + 1
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		if _, e = tx.Exec(ctx, `INSERT INTO billing_product_credit_auto_addon_policies(account_id,product_id,snapshot)
			VALUES($1,$2,$3) ON CONFLICT(account_id,product_id) DO UPDATE SET snapshot=excluded.snapshot,armed=true`, account, productID, encode(out)); e != nil {
			return e
		}
		if e = event(ctx, tx, account, actor, productID, "product_credit.auto_addon.policy", out); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return out, e
}

func validateAutoAddonPolicy(p AutoAddonPolicy) error {
	if !keyOK(p.OfferID) {
		return ErrInvalid
	}
	if p.Trigger == "exhausted" && (p.Threshold == "" || p.Threshold == "0") {
		return nil
	}
	if p.Trigger == "threshold" {
		_, e := integer(p.Threshold, true)
		return e
	}
	return ErrInvalid
}

func loadAutoAddonPolicy(ctx context.Context, q accounts.Querier, account, product string) (AutoAddonPolicy, error) {
	return load[AutoAddonPolicy](ctx, q, `SELECT snapshot FROM billing_product_credit_auto_addon_policies WHERE account_id=$1 AND product_id=$2`, account, product)
}

type autoAddonTrigger struct {
	ID            int64  `json:"id"`
	AccountID     string `json:"account_id"`
	ProductID     string `json:"product_id"`
	PolicyVersion int64  `json:"policy_version"`
	SourceID      string `json:"source_id"`
	Kind          string `json:"trigger_kind"`
	MinimumCredit int64  `json:"minimum_credit"`
	Status        string `json:"status"`
}

func (t autoAddonTrigger) orderKey() string {
	return "auto-addon:" + hash([]string{t.ProductID, str(t.PolicyVersion), t.Kind, t.SourceID})
}

func queueProductCreditAutoAddon(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, account, product, source, kind string, minimum int64) error {
	_, e := q.Exec(ctx, `INSERT INTO billing_product_credit_auto_addon_triggers(account_id,product_id,policy_version,source_id,trigger_kind,minimum_credit)
		SELECT account_id,product_id,(snapshot->>'version')::bigint,$3,$4,$5 FROM billing_product_credit_auto_addon_policies
		WHERE account_id=$1 AND product_id=$2 AND (snapshot->>'enabled')::boolean ON CONFLICT DO NOTHING`, account, product, source, kind, minimum)
	return e
}

func (s *Service) maintainProductCreditAutoAddons(ctx context.Context, now time.Time) (MaintenanceResult, error) {
	var out MaintenanceResult
	var failures error
	triggers, e := list[autoAddonTrigger](ctx, s.pool, `SELECT to_jsonb(t) FROM billing_product_credit_auto_addon_triggers t
		WHERE status='PENDING' ORDER BY attempted_at NULLS FIRST,id LIMIT 100`)
	if e != nil {
		return out, e
	}
	readiness := map[string]NativeCreditReadiness{}
	for _, trigger := range triggers {
		r, seen := readiness[trigger.ProductID]
		if !seen {
			r, _ = s.NativeCheckoutReadiness(ctx, "", "", trigger.ProductID)
			readiness[trigger.ProductID] = r
		}
		e = s.locked(ctx, trigger.AccountID, func(c *pgxpool.Conn) error {
			return s.processAutoAddon(ctx, c, trigger, r, now)
		})
		switch {
		case e == nil:
			out.Processed++
		case errors.Is(e, ErrPending):
			out.Pending++
		default:
			out.Pending++
			failures = errors.Join(failures, e)
		}
	}
	return out, failures
}

func (s *Service) processAutoAddon(ctx context.Context, c *pgxpool.Conn, t autoAddonTrigger, ready NativeCreditReadiness, now time.Time) error {
	tag, e := c.Exec(ctx, `UPDATE billing_product_credit_auto_addon_triggers SET attempted_at=now() WHERE id=$1 AND status='PENDING'`, t.ID)
	if e != nil || tag.RowsAffected() == 0 {
		return e
	}
	key := t.orderKey()
	order, e := load[Order](ctx, c, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND request_key=$2`, t.AccountID, key)
	if e == nil {
		if order.ProductID != t.ProductID || order.CreditScope != ProductCreditScopeV1 || order.Kind != "addon" || order.AutoAddonPolicyVersion != t.PolicyVersion {
			return ErrConflict
		}
		if order.PaymentStatus == "REJECTED" {
			return finishAutoAddon(ctx, c, t, "FAILED", true)
		}
		if e = s.resumeOrderLocked(ctx, c, &order); e != nil {
			return e
		}
		return s.finishPurchasedAutoAddon(ctx, c, t, order, now)
	}
	if !errors.Is(e, accounts.ErrNotFound) {
		return e
	}
	p, e := loadAutoAddonPolicy(ctx, c, t.AccountID, t.ProductID)
	if e != nil {
		return e
	}
	if !p.Enabled || p.Version != t.PolicyVersion {
		return finishAutoAddon(ctx, c, t, "IGNORED", false)
	}
	if e = s.finance(ctx, c, p.ActorID, t.AccountID); e != nil {
		if errors.Is(e, accounts.ErrForbidden) {
			return finishAutoAddon(ctx, c, t, "IGNORED", false)
		}
		return e
	}
	if _, e = activeSubscriptionOn(ctx, c, t.AccountID, t.ProductID, now); e != nil {
		if errors.Is(e, ErrSubscriptionRequired) {
			return finishAutoAddon(ctx, c, t, "IGNORED", true)
		}
		return e
	}
	if e = s.clear(ctx, c, t.AccountID); e != nil {
		return e
	}
	balance, e := s.productAutoAddonBalance(ctx, c, t, now)
	if e != nil {
		return e
	}
	var armed bool
	if e = c.QueryRow(ctx, `SELECT armed FROM billing_product_credit_auto_addon_policies WHERE account_id=$1 AND product_id=$2`, t.AccountID, t.ProductID).Scan(&armed); e != nil {
		return e
	}
	low := autoAddonBalanceLow(p, balance)
	if t.Kind == "funded" || !armed || !low {
		return finishAutoAddon(ctx, c, t, "IGNORED", armed || !low)
	}
	if t.Kind == "insufficient" && balance > 0 && (t.MinimumCredit == 0 || balance >= t.MinimumCredit) {
		return finishAutoAddon(ctx, c, t, "IGNORED", true)
	}
	if !ready.Ready || ready.CreditScope != ProductCreditScopeV1 {
		return ErrPending
	}
	if e = s.commercialClear(ctx, c, t.AccountID); e != nil {
		return e
	}
	offer, e := s.buyable(ctx, c, p.OfferID, t.AccountID)
	if e != nil {
		if errors.Is(e, accounts.ErrNotFound) || errors.Is(e, accounts.ErrForbidden) || errors.Is(e, ErrCreditRatesUnavailable) {
			return finishAutoAddon(ctx, c, t, "IGNORED", false)
		}
		return e
	}
	if offer.CreditScope != ProductCreditScopeV1 || offer.Kind != "addon" || offer.ProductID != t.ProductID {
		return ErrConflict
	}
	price, _ := integer(offer.Price, true)
	unifiedCash, e := s.ledger.Balance(ctx, wallet.Address(t.AccountID, ""), CashAsset)
	if e != nil {
		return e
	}
	if unifiedCash < price {
		// Recheck the original trigger after the shared wallet is funded.
		return ErrPending
	}
	end := offer.monthAnchor(now, offer.ValidityMonths)
	order = Order{ID: id("bo_"), AccountID: t.AccountID, ActorID: p.ActorID, ProductID: t.ProductID, Kind: "addon", Offer: offer, Amount: offer.Price, PaymentStatus: "PENDING", FulfillmentStatus: "PENDING", Start: now, End: end, PeriodStart: now, PeriodEnd: end, GrantAmount: offer.MonthlyCredit, CreatedAt: now, AutoAddonPolicyVersion: p.Version}
	order, e = s.payLocked(ctx, c, order, key, hash(key), nil)
	if errors.Is(e, ledger.ErrInsufficientFunds) {
		return finishAutoAddon(ctx, c, t, "FAILED", true)
	}
	if e != nil {
		return e
	}
	if e = s.fulfillAndReadLocked(ctx, c, &order, now); e != nil {
		return e
	}
	return s.finishPurchasedAutoAddon(ctx, c, t, order, now)
}

func (s *Service) finishPurchasedAutoAddon(ctx context.Context, c *pgxpool.Conn, t autoAddonTrigger, o Order, now time.Time) error {
	if o.PaymentStatus == "REJECTED" {
		return finishAutoAddon(ctx, c, t, "FAILED", true)
	}
	if o.PaymentStatus != "PAID" || o.FulfillmentStatus != "FULFILLED" {
		return ErrPending
	}
	p, e := loadAutoAddonPolicy(ctx, c, t.AccountID, t.ProductID)
	if e != nil {
		return e
	}
	balance, e := s.productAutoAddonBalance(ctx, c, t, now)
	if e != nil {
		return e
	}
	return finishAutoAddon(ctx, c, t, "PURCHASED", !autoAddonBalanceLow(p, balance))
}

func autoAddonBalanceLow(p AutoAddonPolicy, balance int64) bool {
	if p.Trigger == "threshold" {
		threshold, _ := integer(p.Threshold, true)
		return balance < threshold
	}
	return balance == 0
}

func (s *Service) productAutoAddonBalance(ctx context.Context, c *pgxpool.Conn, t autoAddonTrigger, now time.Time) (int64, error) {
	bs, e := productCreditBatchesOn(ctx, c, t.AccountID, t.ProductID)
	if e != nil {
		return 0, e
	}
	var total int64
	for _, b := range bs {
		if b.Frozen || now.Before(b.StartsAt) || !now.Before(b.ExpiresAt) {
			continue
		}
		for _, bucket := range []string{"available", "reserved"} {
			n, e := s.ledger.Balance(ctx, BatchAddress(b.ID, bucket), CreditAsset)
			if e != nil || n < 0 {
				return 0, errors.Join(e, ErrConflict)
			}
			if n > math.MaxInt64-total {
				return math.MaxInt64, nil
			}
			total += n
		}
	}
	return total, nil
}

func finishAutoAddon(ctx context.Context, c *pgxpool.Conn, t autoAddonTrigger, status string, arm bool) error {
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `UPDATE billing_product_credit_auto_addon_triggers SET status=$2 WHERE id=$1 AND status='PENDING'`, t.ID, status); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE billing_product_credit_auto_addon_policies SET armed=$4 WHERE account_id=$1 AND product_id=$2 AND (snapshot->>'version')::bigint=$3`, t.AccountID, t.ProductID, t.PolicyVersion, arm); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Service) validateAutoAddonPayment(ctx context.Context, q accounts.Querier, o Order) error {
	p, e := loadAutoAddonPolicy(ctx, q, o.AccountID, o.ProductID)
	if e != nil {
		return e
	}
	if !p.Enabled || p.Version != o.AutoAddonPolicyVersion || p.ActorID != o.ActorID || p.OfferID != o.Offer.ID || o.Kind != "addon" || o.CreditScope != ProductCreditScopeV1 {
		return accounts.ErrForbidden
	}
	return s.RequireProductCreditMembershipOn(ctx, q, o.AccountID, o.ProductID)
}
