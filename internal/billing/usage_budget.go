package billing

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

type UsageBudget struct {
	AccountID       string  `json:"account_id"`
	OriginProductID string  `json:"origin_product_id"`
	InstanceID      string  `json:"instance_id"`
	Asset           string  `json:"asset"`
	FundingMode     string  `json:"funding_mode"`
	Version         int64   `json:"version"`
	MaxTotal        string  `json:"max_total"`
	MaxPerCall      string  `json:"max_per_call"`
	Held            string  `json:"held"`
	Used            string  `json:"used"`
	Remaining       *string `json:"remaining"`
}

type UsageBudgetRequest struct {
	Version    int64  `json:"version"`
	MaxTotal   string `json:"max_total"`
	MaxPerCall string `json:"max_per_call"`
}

var usageInstanceID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var exactLimit = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)

func ValidateUsageLimits(total, perCall string) error {
	for _, n := range []string{total, perCall} {
		if n == "" {
			continue
		}
		if !exactLimit.MatchString(n) {
			return ErrInvalid
		}
		if _, e := integer(n, false); e != nil {
			return e
		}
	}
	return nil
}

func UsageRemaining(total, held, used string) *string {
	if total == "" {
		return nil
	}
	t, e := integer(total, false)
	if e != nil {
		return nil
	}
	h, e := integer(held, false)
	if e != nil {
		return nil
	}
	u, e := integer(used, false)
	if e != nil {
		return nil
	}
	n := int64(0)
	if h <= t && u <= t-h {
		n = t - h - u
	}
	out := str(n)
	return &out
}

// UsageTotalsOn derives lifetime exposure from the same immutable calls and
// recoverable commands as the final charge. No second balance or debit exists.
// Unknown commands remain fully reserved until their ledger result is known.
func UsageTotalsOn(ctx context.Context, q accounts.Querier, account, product, instance, asset, key string) (string, string, error) {
	var held, used string
	e := q.QueryRow(ctx, `SELECT
 COALESCE(sum(CASE WHEN c.snapshot->>'status' IN ('PENDING','RESERVED','MISSING_USAGE') THEN GREATEST((c.snapshot->>'held')::numeric,COALESCE(pending.held,0)) ELSE 0 END),0)::text,
 COALESCE(sum(CASE WHEN c.snapshot->>'status'='SETTLED' THEN (c.snapshot->>'charged')::numeric ELSE 0 END),0)::text
 FROM billing_calls c
 LEFT JOIN LATERAL (SELECT max((bc.effect#>>'{call,held}')::numeric) AS held FROM billing_commands bc WHERE bc.account_id=c.account_id AND bc.effect#>>'{call,call_id}'=c.id AND bc.kind IN ('reserve','topup') AND bc.status IN ('CREATED','SUBMITTING','UNKNOWN')) pending ON true
 WHERE c.account_id=$1 AND c.snapshot->>'origin_product_id'=$2 AND COALESCE(NULLIF(c.snapshot->>'instance_id',''),'default')=$3 AND COALESCE(NULLIF(c.snapshot->>'charge_asset',''),'CREDIT/6')=$4 AND ($5='' OR c.snapshot->>'credential_id'=$5)`, account, product, instance, asset, key).Scan(&held, &used)
	return held, used, e
}

func budgetAsset(mode string) (string, error) {
	switch mode {
	case FundingCredits:
		return CreditAsset, nil
	case FundingPrepaidCash:
		return CashAsset, nil
	default:
		return "", ErrInvalid
	}
}

func (s *Service) usageBudgetOn(ctx context.Context, q accounts.Querier, account, product, instance, mode string) (UsageBudget, error) {
	asset, e := budgetAsset(mode)
	out := UsageBudget{AccountID: account, OriginProductID: product, InstanceID: instance, FundingMode: mode, Asset: asset}
	if e != nil || !usageInstanceID.MatchString(instance) {
		return out, ErrInvalid
	}
	e = q.QueryRow(ctx, `SELECT version,max_total,max_per_call FROM usage_budgets WHERE account_id=$1 AND origin_product_id=$2 AND instance_id=$3 AND asset=$4`, account, product, instance, asset).Scan(&out.Version, &out.MaxTotal, &out.MaxPerCall)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	out.Held, out.Used, e = UsageTotalsOn(ctx, q, account, product, instance, asset, "")
	out.Remaining = UsageRemaining(out.MaxTotal, out.Held, out.Used)
	return out, e
}

func (s *Service) GetUsageBudget(ctx context.Context, actor, account, productID, instance, mode string) (UsageBudget, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return UsageBudget{}, e
	}
	if e := product(ctx, s.pool, productID); e != nil {
		return UsageBudget{}, e
	}
	return s.usageBudgetOn(ctx, s.pool, account, productID, instance, mode)
}

func (s *Service) SetUsageBudget(ctx context.Context, actor, account, productID, instance, mode string, request UsageBudgetRequest) (UsageBudget, error) {
	var out UsageBudget
	if e := ValidateUsageLimits(request.MaxTotal, request.MaxPerCall); e != nil {
		return out, e
	}
	if request.Version < 0 {
		return out, ErrInvalid
	}
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		if e := product(ctx, c, productID); e != nil {
			return e
		}
		old, e := s.usageBudgetOn(ctx, c, account, productID, instance, mode)
		if e != nil {
			return e
		}
		if old.Version != request.Version {
			return ErrConflict
		}
		// Lowering a cap below existing exposure is safe: old dispatched work
		// remains settleable, and future holds fail until enough capacity frees.
		out = old
		out.Version++
		out.MaxTotal, out.MaxPerCall = request.MaxTotal, request.MaxPerCall
		out.Remaining = UsageRemaining(out.MaxTotal, out.Held, out.Used)
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		_, e = tx.Exec(ctx, `INSERT INTO usage_budgets(account_id,origin_product_id,instance_id,asset,version,max_total,max_per_call,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account_id,origin_product_id,instance_id,asset) DO UPDATE SET version=excluded.version,max_total=excluded.max_total,max_per_call=excluded.max_per_call,updated_by=excluded.updated_by,updated_at=now()`, account, productID, instance, out.Asset, out.Version, out.MaxTotal, out.MaxPerCall, actor)
		if e != nil {
			return e
		}
		if e = event(ctx, tx, account, actor, productID, "usage_budget.updated", out); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return out, e
}

func enforceUsageLimit(total, perCall, held, used string, additional, callTotal int64) error {
	if perCall != "" {
		cap, e := integer(perCall, false)
		if e != nil {
			return e
		}
		if callTotal > cap {
			return accounts.ErrForbidden
		}
	}
	if total != "" {
		cap, e := integer(total, false)
		if e != nil {
			return e
		}
		h, e := integer(held, false)
		if e != nil {
			return e
		}
		u, e := integer(used, false)
		if e != nil {
			return e
		}
		if h > cap || u > cap-h || additional > cap-h-u {
			return accounts.ErrForbidden
		}
	}
	return nil
}

// Called only while holding the common account mutation lock, before a new
// reserve/topup command is persisted. Replays return before this check.
func (s *Service) checkUsageBudget(ctx context.Context, q accounts.Querier, call Call, additional, callTotal int64) error {
	instance := call.InstanceID
	if instance == "" {
		instance = "default"
	}
	budget, e := s.usageBudgetOn(ctx, q, call.AccountID, call.OriginProductID, instance, callFundingMode(call))
	if e != nil {
		return e
	}
	if e = enforceUsageLimit(budget.MaxTotal, budget.MaxPerCall, budget.Held, budget.Used, additional, callTotal); e != nil {
		return e
	}
	if call.CredentialID == "" {
		return nil
	}
	var total, perCall, kind, storedInstance string
	e = q.QueryRow(ctx, `SELECT max_total,max_per_call,credential_kind,instance_id FROM model_credentials WHERE id=$1 AND actor_id=$2 AND account_id=$3 AND origin_product_id=$4`, call.CredentialID, call.ActorID, call.AccountID, call.OriginProductID).Scan(&total, &perCall, &kind, &storedInstance)
	if e != nil {
		return e
	}
	if kind != "api_key" {
		return nil
	}
	if storedInstance != call.InstanceID {
		return accounts.ErrForbidden
	}
	if s.productUsers != nil {
		if e = s.productUsers.RequirePermissionOn(ctx, q, call.ActorID, call.OriginProductID, instance, "product:use"); e != nil {
			return e
		}
	}
	var allowed bool
	if e = q.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3 AND NOT active)`, call.ActorID, call.OriginProductID, instance).Scan(&allowed); e != nil {
		return e
	}
	if !allowed {
		return accounts.ErrForbidden
	}
	held, used, e := UsageTotalsOn(ctx, q, call.AccountID, call.OriginProductID, instance, callAsset(call), call.CredentialID)
	if e != nil {
		return e
	}
	return enforceUsageLimit(total, perCall, held, used, additional, callTotal)
}
