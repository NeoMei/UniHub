package billing

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
	"unihub/internal/accounts"
)

// An accepted asynchronous task may report its final cost after credentials
// expire. A supplemental charge belongs to that exact settled original call;
// it grants no new product use or provider dispatch authority.
func (s *Service) runtimeAcceptedParent(ctx context.Context, q accounts.Querier, scope RuntimeScope, r RuntimeReserveRequest) (RuntimeCall, error) {
	parent, e := s.runtimeCallOn(ctx, q, scope, r.ParentCallID)
	if e != nil {
		return parent, e
	}
	if r.ParentCallID == r.CallID || parent.Status != "SETTLED" || parent.ParentCallID != "" || parent.LocalUserID != r.LocalUserID || parent.LocalKeyID != r.LocalKeyID || parent.AccountID != r.AccountID || parent.Model != r.Model || parent.Operation != r.Operation || parent.Asset != r.Asset || parent.UnitVersion != r.UnitVersion || parent.PriceVersion != r.PriceVersion || parent.PricingDigest != r.PricingDigest {
		return parent, accounts.ErrForbidden
	}
	return parent, nil
}

// Only a proven no-posting rejection may start another attempt of the same
// accepted obligation. UNKNOWN/SUBMITTING first reconcile their old reference.
func (s *Service) runtimeRetryableParentCharge(ctx context.Context, c *pgxpool.Conn, call RuntimeCall) error {
	var cmdID, reason string
	key := call.ID
	if call.Attempt > 0 {
		key += ":retry:" + strconv.Itoa(call.Attempt)
	}
	if e := c.QueryRow(ctx, `SELECT id,error_code FROM billing_commands WHERE account_id=$1 AND kind='runtime_reserve' AND request_key=$2 AND status='REJECTED'`, call.AccountID, key).Scan(&cmdID, &reason); e != nil {
		return e
	}
	cmd, e := readCommand(ctx, c, cmdID)
	if e != nil {
		return e
	}
	if reason != "insufficient_funds" {
		return accounts.ErrForbidden
	}
	return s.runtimeRetryableCommand(ctx, c, cmd)

}

func (s *Service) runtimeRetryableCommand(ctx context.Context, c *pgxpool.Conn, cmd command) error {
	var reason string
	if e := c.QueryRow(ctx, `SELECT error_code FROM billing_commands WHERE id=$1 AND status='REJECTED'`, cmd.ID).Scan(&reason); e != nil {
		return e
	}
	if reason != "insufficient_funds" {
		return accounts.ErrForbidden
	}
	posted, e := s.ledger.Find(ctx, cmd.Ledger.Reference)
	if e != nil {
		return e
	}
	if posted != nil {
		return ErrPending
	}
	return nil
}
