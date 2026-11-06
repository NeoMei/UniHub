package billing

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/database"
)

// Native admissions follow the same authority-before-account order as scope
// upgrades and administration. Keep the locks until the durable ledger decision
// so an administrative revocation cannot split authorization from submission.
func (s *Service) nativeLocked(ctx context.Context, account, product, instance string, fn func(*pgxpool.Conn) error) error {
	c, e := s.pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer c.Release()
	locks := []string{"unihub:platform-administration", "unihub:user-administration", "product-auth:" + product + "/" + instance, "runtime-access:" + product + "/" + instance}
	acquired := []string{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for i := len(acquired) - 1; i >= 0; i-- {
			var unlocked bool
			if err := c.QueryRow(cleanup, `SELECT pg_advisory_unlock_shared(hashtextextended($1,0))`, acquired[i]).Scan(&unlocked); err != nil || !unlocked {
				_ = c.Conn().Close(cleanup)
				return
			}
		}
	}()
	for _, lock := range locks {
		if _, e = c.Exec(ctx, `SELECT pg_advisory_lock_shared(hashtextextended($1,0))`, lock); e != nil {
			return e
		}
		acquired = append(acquired, lock)
	}
	unlock, e := database.LockAccount(ctx, c, account)
	if e != nil {
		return e
	}
	defer unlock(context.Background())
	return fn(c)
}

func (s *Service) callLocked(ctx context.Context, call Call, fn func(*pgxpool.Conn) error) error {
	// Legacy prepaid calls may outlive an in-place Key upgrade. Use the same
	// authority-before-account order without a racy lookup of current Key scope.
	if call.NativePricing != nil || (call.CredentialID != "" && call.OriginProductID == "superwagie" && callFundingMode(call) == FundingPrepaidCash && callAsset(call) == CashAsset) {
		instance := call.InstanceID
		if instance == "" {
			instance = "default"
		}
		return s.nativeLocked(ctx, call.AccountID, call.OriginProductID, instance, fn)
	}
	return s.locked(ctx, call.AccountID, fn)
}

func (s *Service) recoverCommand(ctx context.Context, id string) error {
	cmd, e := readCommand(ctx, s.pool, id)
	if e != nil {
		return e
	}
	call := Call{AccountID: cmd.Account}
	if cmd.Effect.Call != nil {
		call = *cmd.Effect.Call
	}
	return s.callLocked(ctx, call, func(c *pgxpool.Conn) error { return s.runLocked(ctx, c, id) })
}
