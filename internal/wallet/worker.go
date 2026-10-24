package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
)

type pending struct {
	id, actor, kind, status string
	command                 ledger.Command
}

func (s *Service) Process(ctx context.Context, id string) error {
	var account string
	if e := s.pool.QueryRow(ctx, `SELECT account_id FROM wallet_operations WHERE id=$1`, id).Scan(&account); e != nil {
		return dbError(e)
	}
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	unlock, e := database.LockAccount(ctx, conn, account)
	if e != nil {
		return e
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = unlock(c)
	}()
	// Account locks protect submission, while this durable barrier protects its
	// ordering across both workers. A commercial payment with an uncertain ledger
	// result (or paid benefits still being recovered) must finish before a later
	// wallet operation changes the cash allocation it was based on.
	var commercialPending bool
	if e = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_commands WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN')) OR EXISTS(SELECT 1 FROM billing_orders WHERE account_id=$1 AND ((snapshot->>'payment_status'='PAID' AND snapshot->>'fulfillment_status'='PENDING' AND COALESCE(snapshot->>'refund_status','') IN ('','REJECTED')) OR snapshot->>'refund_status'='FROZEN'))`, account).Scan(&commercialPending); e != nil {
		return e
	}
	if commercialPending {
		return ErrPending
	}
	rows, e := conn.Query(ctx, `SELECT id,actor_id,kind,status,command FROM wallet_operations WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN') AND (created_at,id) <= (SELECT created_at,id FROM wallet_operations WHERE id=$2) ORDER BY created_at,id`, account, id)
	if e != nil {
		return e
	}
	jobs := []pending{}
	for rows.Next() {
		var p pending
		var b []byte
		if e = rows.Scan(&p.id, &p.actor, &p.kind, &p.status, &b); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal(b, &p.command); e != nil {
			rows.Close()
			return e
		}
		jobs = append(jobs, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, p := range jobs {
		if e = s.processOne(ctx, conn, account, p); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) processOne(ctx context.Context, c *pgxpool.Conn, account string, p pending) error {
	if p.status == "CREATED" && p.kind == "transfer" {
		if e := s.finance(ctx, c, p.actor, account); e != nil {
			if errors.Is(e, accounts.ErrForbidden) {
				return s.finish(ctx, c, account, p, "CANCELED", "authorization_revoked", nil)
			}
			return e
		}
	}
	if _, e := c.Exec(ctx, `UPDATE wallet_operations SET status='SUBMITTING',attempts=attempts+1,updated_at=now() WHERE id=$1`, p.id); e != nil {
		return e
	}
	existing, e := s.ledger.Find(ctx, p.command.Reference)
	if e == nil && existing != nil {
		return s.applyFound(ctx, c, account, p, existing)
	}
	if e != nil {
		return s.markUnknown(ctx, c, p.id)
	}
	result, e := s.ledger.Post(ctx, p.command)
	if e == nil {
		return s.applyFound(ctx, c, account, p, &result)
	}
	existing, lookupErr := s.ledger.Find(ctx, p.command.Reference)
	if lookupErr == nil && existing != nil {
		return s.applyFound(ctx, c, account, p, existing)
	}
	if lookupErr == nil && errors.Is(e, ledger.ErrInsufficientFunds) && p.status == "CREATED" {
		return s.finish(ctx, c, account, p, "REJECTED", "insufficient_funds", nil)
	}
	return s.markUnknown(ctx, c, p.id)
}
func (s *Service) applyFound(ctx context.Context, c *pgxpool.Conn, account string, p pending, t *ledger.Transaction) error {
	if t.ID == "" || t.Reference != p.command.Reference || !reflect.DeepEqual(t.Postings, p.command.Postings) {
		return s.markUnknown(ctx, c, p.id)
	}
	return s.finish(ctx, c, account, p, "APPLIED", "", &t.ID)
}
func (s *Service) finish(ctx context.Context, c *pgxpool.Conn, account string, p pending, status, code string, tid *string) error {
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `UPDATE wallet_operations SET status=$2,error_code=$3,ledger_transaction_id=$4,updated_at=now() WHERE id=$1`, p.id, status, code, tid); e != nil {
		return e
	}
	if e = audit(ctx, tx, account, p.actor, p.id, "operation."+status); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) markUnknown(ctx context.Context, c *pgxpool.Conn, id string) error {
	if _, e := c.Exec(ctx, `UPDATE wallet_operations SET status='UNKNOWN',error_code='awaiting_ledger_confirmation',updated_at=now(),next_attempt_at=now()+interval '5 seconds' WHERE id=$1`, id); e != nil {
		return e
	}
	return ErrPending
}

// Recover processes durable commands. Different workers serialize per account;
// UNKNOWN results keep the original reference and precede later account commands.
func (s *Service) Recover(ctx context.Context) error {
	// Select each account's head before filtering by backoff. Otherwise due
	// followers behind one UNKNOWN head can fill the whole batch forever and
	// repeatedly retry that head while starving unrelated accounts.
	rows, e := s.pool.Query(ctx, `SELECT id FROM (
		SELECT DISTINCT ON (account_id) id,created_at,next_attempt_at
		FROM wallet_operations WHERE status IN ('CREATED','SUBMITTING','UNKNOWN')
		ORDER BY account_id,created_at,id
	) heads WHERE next_attempt_at<=now() ORDER BY created_at,id LIMIT 100`)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var first error
	for _, id := range ids {
		if e = s.Process(ctx, id); e != nil && !errors.Is(e, ErrPending) && first == nil {
			first = e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return first
}
