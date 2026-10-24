package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

func (s *Service) Settings(ctx context.Context, actor, account string) ([]Authorization, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	return list[Authorization](ctx, s.pool, `SELECT snapshot FROM billing_authorizations WHERE account_id=$1 ORDER BY product_id,purpose`, account)
}
func (s *Service) SetAuthorization(ctx context.Context, actor, account string, a Authorization) (Authorization, error) {
	if a.Purpose != "funding" && a.Purpose != "renewal" {
		return a, ErrInvalid
	}
	if a.Enabled {
		if _, e := integer(a.MaxPerOperation, true); e != nil {
			return a, e
		}
		if _, e := integer(a.MaxTotal, true); e != nil {
			return a, e
		}
		if !s.now().Before(a.ExpiresAt) {
			return a, ErrInvalid
		}
	}
	e := s.locked(ctx, account, func(c *pgxpool.Conn) error {
		if e := s.finance(ctx, c, actor, account); e != nil {
			return e
		}
		if e := product(ctx, c, a.ProductID); e != nil {
			return e
		}
		old, e := load[Authorization](ctx, c, `SELECT snapshot FROM billing_authorizations WHERE account_id=$1 AND product_id=$2 AND purpose=$3`, account, a.ProductID, a.Purpose)
		if e != nil && !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		if e == nil && a.Version != old.Version {
			return ErrConflict
		}
		if e != nil && a.Version != 0 {
			return ErrConflict
		}
		a.Version = old.Version + 1
		a.ActorID = actor
		a.Used = old.Used
		if a.Used == "" {
			a.Used = "0"
		}
		if a.Enabled {
			max, _ := integer(a.MaxTotal, true)
			used, _ := integer(a.Used, false)
			if max < used {
				return ErrInvalid
			}
		} else {
			if a.MaxPerOperation == "" {
				a.MaxPerOperation = old.MaxPerOperation
			}
			if a.MaxTotal == "" {
				a.MaxTotal = old.MaxTotal
			}
		}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer database.Rollback(tx)
		_, e = tx.Exec(ctx, `INSERT INTO billing_authorizations(account_id,product_id,purpose,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT(account_id,product_id,purpose) DO UPDATE SET snapshot=excluded.snapshot`, account, a.ProductID, a.Purpose, encode(a))
		if e != nil {
			return e
		}
		if e = event(ctx, tx, account, actor, a.ProductID, a.Purpose+".authorization", a); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return a, e
}
func (s *Service) budget(ctx context.Context, q accounts.Querier, actor, account, product, purpose string, amount int64, nowTime time.Time) (Authorization, error) {
	a, e := load[Authorization](ctx, q, `SELECT snapshot FROM billing_authorizations WHERE account_id=$1 AND product_id=$2 AND purpose=$3`, account, product, purpose)
	if e != nil {
		return a, accounts.ErrForbidden
	}
	if e = s.finance(ctx, q, a.ActorID, account); e != nil {
		return a, e
	}
	per, e := integer(a.MaxPerOperation, true)
	if e != nil {
		return a, e
	}
	total, e := integer(a.MaxTotal, true)
	if e != nil {
		return a, e
	}
	used, e := integer(a.Used, false)
	if e != nil {
		return a, e
	}
	if !a.Enabled || !nowTime.Before(a.ExpiresAt) || amount > per || amount > total-used {
		return a, accounts.ErrForbidden
	}
	a.Used = str(used + amount)
	return a, nil
}
func updateAuth(ctx context.Context, tx pgx.Tx, account string, a Authorization) error {
	_, e := tx.Exec(ctx, `UPDATE billing_authorizations SET snapshot=$4 WHERE account_id=$1 AND product_id=$2 AND purpose=$3`, account, a.ProductID, a.Purpose, encode(a))
	return e
}
func (s *Service) releaseBudget(ctx context.Context, tx pgx.Tx, o Order) error {
	for _, x := range []struct {
		purpose string
		version int64
		amount  string
	}{{"funding", o.FundingAuthorizationVersion, o.UnifiedFunding}, {"renewal", o.RenewalAuthorizationVersion, o.Amount}} {
		if x.version == 0 {
			continue
		}
		a, e := load[Authorization](ctx, tx, `SELECT snapshot FROM billing_authorizations WHERE account_id=$1 AND product_id=$2 AND purpose=$3`, o.AccountID, o.ProductID, x.purpose)
		if e != nil {
			return e
		}
		used, _ := integer(a.Used, false)
		n, _ := integer(x.amount, false)
		if n > used {
			return ErrConflict
		}
		a.Used = str(used - n)
		if e = updateAuth(ctx, tx, o.AccountID, a); e != nil {
			return e
		}
	}
	return nil
}

// The authorization cap is already reserved in the order snapshot. This check
// never consumes twice; it only prevents a CREATED command crossing revocation.
func (s *Service) validatePaymentAuthorization(ctx context.Context, q accounts.Querier, o Order) error {
	for _, x := range []struct {
		purpose string
		version int64
	}{{"funding", o.FundingAuthorizationVersion}, {"renewal", o.RenewalAuthorizationVersion}} {
		if x.version == 0 {
			continue
		}
		a, e := load[Authorization](ctx, q, `SELECT snapshot FROM billing_authorizations WHERE account_id=$1 AND product_id=$2 AND purpose=$3`, o.AccountID, o.ProductID, x.purpose)
		if e != nil {
			return e
		}
		if !a.Enabled || a.Version != x.version || !s.now().Before(a.ExpiresAt) {
			return accounts.ErrForbidden
		}
		if e = s.finance(ctx, q, a.ActorID, o.AccountID); e != nil {
			return e
		}
	}
	return nil
}
