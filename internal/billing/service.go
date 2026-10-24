package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
)

type Service struct {
	pool               *pgxpool.Pool
	accounts           *accounts.Store
	ledger             ledger.Ledger
	now                func() time.Time
	mu                 sync.RWMutex
	operators, trusted map[string]bool
	sandbox            bool
}

func New(p *pgxpool.Pool, a *accounts.Store, l ledger.Ledger) *Service {
	return &Service{pool: p, accounts: a, ledger: l, now: time.Now, operators: map[string]bool{}, trusted: map[string]bool{}}
}
func (s *Service) ConfigureOperators(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operators = map[string]bool{}
	for _, id := range ids {
		if id != "" {
			s.operators[id] = true
		}
	}
}
func (s *Service) ConfigureTrustedServices(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trusted = map[string]bool{}
	for _, id := range ids {
		if id != "" {
			s.trusted[id] = true
		}
	}
}
func (s *Service) ConfigureSandbox(v bool) { s.mu.Lock(); defer s.mu.Unlock(); s.sandbox = v }
func (s *Service) isSandbox() bool         { s.mu.RLock(); defer s.mu.RUnlock(); return s.sandbox }
func (s *Service) operator(id string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.operators[id] {
		return accounts.ErrForbidden
	}
	return nil
}
func (s *Service) trust(id string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.trusted[id] {
		return accounts.ErrForbidden
	}
	return nil
}
func (s *Service) finance(ctx context.Context, q accounts.Querier, actor, account string) error {
	r, e := s.accounts.RoleOn(ctx, q, actor, account)
	if e != nil {
		return e
	}
	if r != "owner" && r != "billing_admin" {
		return accounts.ErrForbidden
	}
	return nil
}
func id(prefix string) string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func encode(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func keyOK(v string) bool { return len(v) > 0 && len(v) <= 128 }

// dbq is implemented by pgx connections, pools, and transactions.
type dbq interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func load[T any](ctx context.Context, q accounts.Querier, sql string, args ...any) (T, error) {
	var x T
	var b []byte
	e := q.QueryRow(ctx, sql, args...).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return x, accounts.ErrNotFound
	}
	if e != nil {
		return x, e
	}
	e = json.Unmarshal(b, &x)
	return x, e
}
func list[T any](ctx context.Context, q dbq, sql string, args ...any) ([]T, error) {
	rows, e := q.Query(ctx, sql, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b []byte
		var x T
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &x); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Service) locked(ctx context.Context, account string, fn func(*pgxpool.Conn) error) error {
	c, e := s.pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer c.Release()
	unlock, e := database.LockAccount(ctx, c, account)
	if e != nil {
		return e
	}
	defer unlock(context.Background())
	return fn(c)
}
func event(ctx context.Context, tx pgx.Tx, account, actor, entity, action string, detail any) error {
	_, e := tx.Exec(ctx, `INSERT INTO billing_events(account_id,actor_id,entity_id,action,detail) VALUES($1,$2,$3,$4,$5)`, account, actor, entity, action, encode(detail))
	return e
}
func (s *Service) clear(ctx context.Context, c *pgxpool.Conn, account string) error {
	return s.clearExceptOrder(ctx, c, account, "")
}
func (s *Service) clearExceptOrder(ctx context.Context, c *pgxpool.Conn, account, except string) error {
	var pending bool
	e := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_commands WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN')) OR EXISTS(SELECT 1 FROM wallet_operations WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN') AND ($2='' OR created_at <= (SELECT (snapshot->>'created_at')::timestamptz FROM billing_orders WHERE id=$2 AND account_id=$1))) OR EXISTS(SELECT 1 FROM billing_orders WHERE account_id=$1 AND id<>$2 AND ((snapshot->>'payment_status'='PAID' AND snapshot->>'fulfillment_status'='PENDING' AND COALESCE(snapshot->>'refund_status','') IN ('','REJECTED')) OR snapshot->>'refund_status'='FROZEN'))`, account, except).Scan(&pending)
	if e != nil {
		return e
	}
	if pending {
		return ErrPending
	}
	return nil
}
func (s *Service) member(ctx context.Context, q accounts.Querier, actor, account string) error {
	_, e := s.accounts.RoleOn(ctx, q, actor, account)
	return e
}
func product(ctx context.Context, q accounts.Querier, p string) error {
	var ok bool
	e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id=$1 AND enabled)`, p).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ErrInvalid
	}
	return nil
}

func refundBlocks(status string) bool { return status != "" && status != "REJECTED" }
