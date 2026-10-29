package billing

// The platform adapter is a distinct authorization entry point. It never changes
// the legacy operator allowlist, payer membership, or the actor in the ledger.
import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

type AdminAuthority interface {
	RequireAdminOn(context.Context, accounts.Querier, string, string) error
	WriteAudit(context.Context, pgx.Tx, string, string, string, string, any) error
}
type AdminOperation struct {
	Action      string
	ID          string
	AccountID   string
	ProductID   string
	InstanceID  string
	FundingMode string
	Reason      string
	Version     string
	Offer       Offer
	Rate        Rate
	Profile     BillingProfile
	Budget      UsageBudgetRequest
	Decision    string
}

func (s *Service) Administer(ctx context.Context, authority AdminAuthority, actor string, r AdminOperation) (any, error) {
	if authority == nil || !keyOK(r.Reason) {
		return nil, ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`); e != nil {
		return nil, e
	}
	if e = authority.RequireAdminOn(ctx, tx, actor, "finance:write"); e != nil {
		return nil, e
	}
	var out any
	var before any
	switch r.Action {
	case "offer.save", "offer.publish", "offer.retire":
		var raw []byte
		var version, status string
		e = tx.QueryRow(ctx, `SELECT snapshot,md5(snapshot::text),status FROM billing_offers WHERE id=$1 FOR UPDATE`, r.ID).Scan(&raw, &version, &status)
		exists := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		var o Offer
		if exists {
			if json.Unmarshal(raw, &o) != nil {
				return nil, ErrInvalid
			}
			before = o
			if r.Version != version {
				return nil, ErrConflict
			}
		} else if r.Version != "" && r.Version != "0" {
			return nil, ErrConflict
		}
		if r.Action == "offer.save" {
			if exists && status != "draft" {
				return nil, ErrConflict
			}
			o = r.Offer
			if o.ID != r.ID {
				return nil, ErrInvalid
			}
			o.Status = "draft"
			if e = validateOffer(o); e != nil {
				return nil, e
			}
			if o.Sandbox && !s.isSandbox() {
				return nil, accounts.ErrForbidden
			}
			if e = lockBillingProduct(ctx, tx, o.ProductID); e != nil {
				return nil, e
			}
			if e = s.offerForSaleOn(ctx, tx, o); e != nil {
				return nil, e
			}
			_, e = tx.Exec(ctx, `INSERT INTO billing_offers(id,product_id,status,snapshot) VALUES($1,$2,'draft',$3) ON CONFLICT(id) DO UPDATE SET product_id=excluded.product_id,status='draft',snapshot=excluded.snapshot`, o.ID, o.ProductID, encode(o))
		} else {
			if !exists {
				return nil, accounts.ErrNotFound
			}
			if r.Action == "offer.publish" {
				if status != "draft" {
					return nil, ErrConflict
				}
				if e = validateOffer(o); e != nil {
					return nil, e
				}
				if o.Sandbox && !s.isSandbox() {
					return nil, accounts.ErrForbidden
				}
				if e = lockBillingProduct(ctx, tx, o.ProductID); e != nil {
					return nil, e
				}
				if e = s.offerForSaleOn(ctx, tx, o); e != nil {
					return nil, e
				}
				o.Status = "published"
			} else {
				if status != "published" {
					return nil, ErrConflict
				}
				o.Status = "retired"
			}
			_, e = tx.Exec(ctx, `UPDATE billing_offers SET status=$2,snapshot=$3 WHERE id=$1`, o.ID, o.Status, encode(o))
		}
		out = o
	case "rate.publish":
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:billing-rate-catalog',0))`); e != nil {
			return nil, e
		}
		rate := r.Rate
		if r.Version != "" && r.Version != "0" {
			return nil, ErrConflict
		}
		if !keyOK(rate.ID) || rate.Model == "" {
			return nil, ErrInvalid
		}
		rate.Asset = normalizedAsset(rate.Asset)
		if e = validateRate(rate); e != nil {
			return nil, e
		}
		rate.Status = "published"
		result, err := tx.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2) ON CONFLICT(id) DO NOTHING`, rate.ID, encode(rate))
		e = err
		if e == nil && result.RowsAffected() != 1 {
			return nil, ErrConflict
		}
		out = rate
	case "rate.retire":
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:billing-rate-catalog',0))`); e != nil {
			return nil, e
		}
		var raw []byte
		var version, status string
		if e = tx.QueryRow(ctx, `SELECT snapshot,md5(snapshot::text),status FROM billing_rates WHERE id=$1 FOR UPDATE`, r.ID).Scan(&raw, &version, &status); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return nil, accounts.ErrNotFound
			}
			return nil, e
		}
		if version != r.Version || status != "published" {
			return nil, ErrConflict
		}
		var rate Rate
		if json.Unmarshal(raw, &rate) != nil {
			return nil, ErrInvalid
		}
		before = rate
		rate.Status = "retired"
		_, e = tx.Exec(ctx, `UPDATE billing_rates SET status='retired' WHERE id=$1`, r.ID)
		out = rate
	case "profile.save":
		profile := canonicalProfile(r.Profile)
		v, err := strconv.ParseInt(r.Version, 10, 64)
		if err != nil || v != profile.Version || profile.ProductID != r.ProductID || profile.Version == math.MaxInt64 {
			return nil, ErrInvalid
		}
		if e = profile.Validate(); e != nil {
			return nil, e
		}
		if e = lockBillingProduct(ctx, tx, r.ProductID); e != nil {
			return nil, e
		}
		old, err := s.ProfileOn(ctx, tx, r.ProductID)
		if err != nil {
			return nil, err
		}
		before = old
		if old.Version != profile.Version {
			return nil, ErrConflict
		}
		profile.Version++
		e = insertBillingProfile(ctx, tx, actor, profile)
		out = profile
	case "budget.save":
		if r.Budget.Version < 0 || ValidateUsageLimits(r.Budget.MaxTotal, r.Budget.MaxPerCall) != nil {
			return nil, ErrInvalid
		}
		if e = database.LockAccountTx(ctx, tx, r.AccountID); e != nil {
			return nil, e
		}
		if e = product(ctx, tx, r.ProductID); e != nil {
			return nil, e
		}
		old, err := s.usageBudgetOn(ctx, tx, r.AccountID, r.ProductID, r.InstanceID, r.FundingMode)
		if err != nil {
			return nil, err
		}
		before = old
		if old.Version != r.Budget.Version {
			return nil, ErrConflict
		}
		old.Version++
		old.MaxTotal = r.Budget.MaxTotal
		old.MaxPerCall = r.Budget.MaxPerCall
		old.Remaining = UsageRemaining(old.MaxTotal, old.Held, old.Used)
		_, e = tx.Exec(ctx, `INSERT INTO usage_budgets(account_id,origin_product_id,instance_id,asset,version,max_total,max_per_call,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account_id,origin_product_id,instance_id,asset) DO UPDATE SET version=excluded.version,max_total=excluded.max_total,max_per_call=excluded.max_per_call,updated_by=excluded.updated_by,updated_at=now()`, r.AccountID, r.ProductID, r.InstanceID, old.Asset, old.Version, old.MaxTotal, old.MaxPerCall, actor)
		if e == nil {
			e = event(ctx, tx, r.AccountID, actor, r.ProductID, "usage_budget.updated", old)
		}
		out = old
	case "refund.review":
		if r.Decision != "reject" {
			return nil, ErrUnsupported
		}
		if e = database.LockAccountTx(ctx, tx, r.AccountID); e != nil {
			return nil, e
		}
		var version string
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT snapshot,md5(snapshot::text) FROM billing_orders WHERE id=$1 AND account_id=$2 FOR UPDATE`, r.ID, r.AccountID).Scan(&raw, &version)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, accounts.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		if version != r.Version {
			return nil, ErrConflict
		}
		var order Order
		if json.Unmarshal(raw, &order) != nil {
			return nil, ErrInvalid
		}
		before = order
		if order.RefundStatus != "MANUAL_REVIEW" {
			return nil, ErrConflict
		}
		var pending bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_commands WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN')) OR EXISTS(SELECT 1 FROM wallet_operations WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN')) OR EXISTS(SELECT 1 FROM billing_orders WHERE account_id=$1 AND id<>$2 AND ((snapshot->>'payment_status'='PAID' AND snapshot->>'fulfillment_status'='PENDING' AND COALESCE(snapshot->>'refund_status','') IN ('','REJECTED')) OR snapshot->>'refund_status'='FROZEN'))`, r.AccountID, r.ID).Scan(&pending); e != nil {
			return nil, e
		}
		if pending {
			return nil, ErrPending
		}
		batches, err := list[Batch](ctx, tx, `SELECT snapshot FROM billing_batches WHERE order_id=$1 AND account_id=$2`, r.ID, r.AccountID)
		if err != nil {
			return nil, err
		}
		order.RefundStatus = "REJECTED"
		order.RefundReason = r.Reason
		e = saveOrder(ctx, tx, order)
		for _, b := range batches {
			if e != nil {
				break
			}
			b.Frozen = false
			e = saveBatch(ctx, tx, b)
		}
		if e == nil {
			e = event(ctx, tx, r.AccountID, actor, r.ID, "refund.review_rejected", map[string]string{"reason": r.Reason})
		}
		out = order
	default:
		return nil, ErrUnsupported
	}
	if e != nil {
		return nil, e
	}
	if e = authority.WriteAudit(ctx, tx, actor, r.Action, r.ID+r.AccountID+r.ProductID, r.Reason, map[string]any{"before": before, "after": out, "account_id": r.AccountID, "product_id": r.ProductID, "expected_version": r.Version}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

// AdminCashBalance reads one exact scope; callers must not fan out across all
// accounts. Missing ledger configuration and failures remain failures.
func (s *Service) AdminCashBalance(ctx context.Context, authority AdminAuthority, actor, account, productID string) (map[string]any, error) {
	if e := authority.RequireAdminOn(ctx, s.pool, actor, "finance:read"); e != nil {
		return nil, e
	}
	if s.ledger == nil {
		return nil, ErrUnsupported
	}
	var exists bool
	if e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)`, account).Scan(&exists); e != nil {
		return nil, e
	}
	if !exists {
		return nil, accounts.ErrNotFound
	}
	if productID != "" {
		if e := product(ctx, s.pool, productID); e != nil {
			return nil, e
		}
	}
	address := "cash:" + account + ":unified"
	if productID != "" {
		address = "cash:" + account + ":product:" + productID
	}
	n, e := s.ledger.Balance(ctx, address, CashAsset)
	if e != nil {
		return nil, e
	}
	return map[string]any{"account_id": account, "product_id": productID, "asset": CashAsset, "amount": strconv.FormatInt(n, 10), "source": "formance", "status": "available"}, nil
}

func (s *Service) AdminBatchQuantities(ctx context.Context, authority AdminAuthority, actor string, b *Batch) error {
	if e := authority.RequireAdminOn(ctx, s.pool, actor, "finance:read"); e != nil {
		return e
	}
	if s.ledger == nil {
		return ErrUnsupported
	}
	return s.quantities(ctx, b)
}

func (s *Service) AdminUsageBudget(ctx context.Context, authority AdminAuthority, actor, account, productID, instance, mode string) (UsageBudget, error) {
	if e := authority.RequireAdminOn(ctx, s.pool, actor, "finance:read"); e != nil {
		return UsageBudget{}, e
	}
	if e := product(ctx, s.pool, productID); e != nil {
		return UsageBudget{}, e
	}
	var exists bool
	if e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)`, account).Scan(&exists); e != nil {
		return UsageBudget{}, e
	}
	if !exists {
		return UsageBudget{}, accounts.ErrNotFound
	}
	return s.usageBudgetOn(ctx, s.pool, account, productID, instance, mode)
}
