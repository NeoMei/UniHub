// Package platformadmin provides current database-backed central authority.
package platformadmin

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/productusers"
)

type Store struct {
	pool             *pgxpool.Pool
	products         *productusers.Store
	identityProvider IdentityProvider
}

func New(pool *pgxpool.Pool, products *productusers.Store) *Store {
	s := &Store{pool: pool, products: products}
	if products != nil {
		products.ConfigureManagement(s)
	}
	return s
}

type ListOptions struct {
	Search      string
	Cursor      string
	Status      string
	Role        string
	Limit       int
	CreatedFrom time.Time
	CreatedTo   time.Time
}
type Page[T any] struct {
	Items      []T       `json:"items"`
	Total      int64     `json:"total"`
	NextCursor string    `json:"next_cursor,omitempty"`
	AsOf       time.Time `json:"as_of"`
}

func (s *Store) WriteAudit(ctx context.Context, tx pgx.Tx, actor, action, target, reason string, detail any) error {
	return WriteAudit(ctx, tx, actor, action, target, reason, detail)
}

func WriteAudit(ctx context.Context, tx pgx.Tx, actor, action, target, reason string, detail any) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(action) == "" || !validReason(reason) {
		return accounts.ErrInvalid
	}
	raw, e := json.Marshal(detail)
	if e != nil {
		return accounts.ErrInvalid
	}
	_, e = tx.Exec(ctx, `INSERT INTO platform_admin_audit(actor_id,action,target_id,reason,detail) VALUES($1,$2,$3,$4,$5)`, actor, action, target, reason, raw)
	return e
}
func validReason(v string) bool { return strings.TrimSpace(v) != "" && len(v) <= 2000 }
func lockAuthority(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`)
	return e
}

func (s *Store) SetLegacyAccess(ctx context.Context, actor, target, product, instance string, roles []string, active bool, expected int64) (productusers.Assignment, error) {
	return s.SetProductAccess(ctx, actor, target, product, instance, roles, active, expected, "legacy user-management request")
}
func (s *Store) SetLegacyStatus(ctx context.Context, actor, target string, active bool, expected int64) (productusers.ManagedUser, error) {
	u, e := s.SetStatus(ctx, actor, target, active, expected, "legacy user-management request")
	return productusers.ManagedUser{ID: u.ID, Name: u.Name, Email: u.Email, Active: u.Active, Version: u.Version}, e
}
