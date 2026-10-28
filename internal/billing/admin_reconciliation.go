package billing

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

// AdminReconcile continues exactly one durable command using its original
// reference and postings. The financial actor remains the command's actor;
// administrative authorization and initiation have their own audited actor.
func (s *Service) AdminReconcile(ctx context.Context, authority AdminAuthority, actor, id, version, reason, key string) (map[string]any, error) {
	if authority == nil || !keyOK(reason) || !keyOK(key) || !keyOK(id) {
		return nil, ErrInvalid
	}
	if s.ledger == nil {
		return nil, ErrUnsupported
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
	var account, status string
	if e = tx.QueryRow(ctx, `SELECT account_id,status FROM billing_commands WHERE id=$1`, id).Scan(&account, &status); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, accounts.ErrNotFound
		}
		return nil, e
	}
	if status != version {
		return nil, ErrConflict
	}
	if status != "CREATED" && status != "SUBMITTING" && status != "UNKNOWN" {
		return nil, ErrConflict
	}
	if e = authority.WriteAudit(ctx, tx, actor, "finance.reconciliation_requested", id, reason, map[string]string{"account_id": account, "source": "billing_commands", "expected_version": version, "idempotency_key": key}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	runError := s.locked(ctx, account, func(c *pgxpool.Conn) error { return s.runLocked(ctx, c, id) })
	var transaction *string
	if e = s.pool.QueryRow(ctx, `SELECT status,ledger_transaction_id FROM billing_commands WHERE id=$1`, id).Scan(&status, &transaction); e != nil {
		return nil, e
	}
	out := map[string]any{"id": id, "account_id": account, "source": "billing_commands", "status": status, "version": status, "ledger_transaction_id": transaction, "reconciliation_pending": status == "UNKNOWN" || status == "SUBMITTING" || status == "CREATED"}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5e9)
	defer cancel()
	atx, e := s.pool.Begin(auditCtx)
	if e != nil {
		return nil, e
	}
	defer database.Rollback(atx)
	if e = authority.WriteAudit(auditCtx, atx, actor, "finance.reconciliation_readback", id, reason, out); e != nil {
		return nil, e
	}
	if e = atx.Commit(auditCtx); e != nil {
		return nil, e
	}
	if runError != nil && status != "UNKNOWN" && status != "SUBMITTING" && status != "CREATED" && status != "REJECTED" && status != "CANCELED" {
		return nil, runError
	}
	return out, nil
}
