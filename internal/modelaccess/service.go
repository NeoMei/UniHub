// Package modelaccess issues short-lived opaque capabilities for the llmrouter
// audience. A model capability is never a login, wallet or settlement credential.
package modelaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"slices"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
)

var ErrCredential = errors.New("model credential invalid or no longer authorized")

type Eligibility interface {
	Subscriptions(context.Context, string, string) ([]billing.Subscription, error)
	Rates(context.Context) ([]billing.Rate, error)
}
type Service struct {
	pool        *pgxpool.Pool
	accounts    *accounts.Store
	eligibility Eligibility
}
type Request struct {
	OriginProductID string   `json:"origin_product_id"`
	Models          []string `json:"models"`
	TaskID          string   `json:"task_id"`
}
type Claims struct {
	ID              string    `json:"id"`
	Audience        string    `json:"audience"`
	ActorID         string    `json:"actor_id"`
	AccountID       string    `json:"account_id"`
	OriginProductID string    `json:"origin_product_id"`
	Models          []string  `json:"models"`
	TaskID          string    `json:"task_id"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type Grant struct {
	Claims
	Token string `json:"token"`
}

func New(pool *pgxpool.Pool, a *accounts.Store, e Eligibility) *Service { return &Service{pool, a, e} }
func opaque(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + hex.EncodeToString(b), nil
}
func digest(token string) string { h := sha256.Sum256([]byte(token)); return hex.EncodeToString(h[:]) }
func (s *Service) eligible(ctx context.Context, actor, account, product string) (time.Time, error) {
	if _, e := s.accounts.Role(ctx, actor, account); e != nil {
		return time.Time{}, e
	}
	subs, e := s.eligibility.Subscriptions(ctx, actor, account)
	if e != nil {
		return time.Time{}, e
	}
	for _, sub := range subs {
		if sub.ProductID == product && sub.Status == "active" && sub.PaidThrough.After(time.Now().UTC()) {
			return sub.PaidThrough, nil
		}
	}
	return time.Time{}, accounts.ErrForbidden
}
func (s *Service) Issue(ctx context.Context, actor, account string, r Request) (Grant, error) {
	if r.OriginProductID == "" || len(r.Models) == 0 || len(r.Models) > 32 || strings.TrimSpace(r.TaskID) == "" || len(r.TaskID) > 128 {
		return Grant{}, billing.ErrInvalid
	}
	until, e := s.eligible(ctx, actor, account, r.OriginProductID)
	if e != nil {
		return Grant{}, e
	}
	rates, e := s.eligibility.Rates(ctx)
	if e != nil {
		return Grant{}, e
	}
	allowed := map[string]bool{}
	for _, v := range rates {
		if v.Status == "published" {
			allowed[v.Model] = true
		}
	}
	models := slices.Clone(r.Models)
	slices.Sort(models)
	models = slices.Compact(models)
	for _, v := range models {
		if !allowed[v] {
			return Grant{}, accounts.ErrForbidden
		}
	}
	token, e := opaque("uh_model_")
	if e != nil {
		return Grant{}, e
	}
	id, e := opaque("mc_")
	if e != nil {
		return Grant{}, e
	}
	expiry := time.Now().UTC().Add(10 * time.Minute)
	if until.Before(expiry) {
		expiry = until
	}
	grant := Grant{Claims: Claims{ID: id, Audience: "llmrouter", ActorID: actor, AccountID: account, OriginProductID: r.OriginProductID, Models: models, TaskID: r.TaskID, ExpiresAt: expiry}, Token: token}
	data, _ := json.Marshal(models)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Grant{}, e
	}
	defer database.Rollback(tx)
	if e = database.LockAccountTx(ctx, tx, account); e != nil {
		return Grant{}, e
	}
	if _, e = s.accounts.RoleOn(ctx, tx, actor, account); e != nil {
		return Grant{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, digest(token), actor, account, r.OriginProductID, data, r.TaskID, expiry); e != nil {
		return Grant{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action) VALUES($1,$2,$3,'model_credential.issued')`, account, actor, id); e != nil {
		return Grant{}, e
	}
	return grant, tx.Commit(ctx)
}

// Verify is called only by the separately authenticated gateway. It returns no
// capability secret. New reservations also recheck membership in billing's lock.
func (s *Service) Verify(ctx context.Context, token, model string) (Claims, error) {
	if len(token) != 73 || !strings.HasPrefix(token, "uh_model_") {
		return Claims{}, ErrCredential
	}
	var c Claims
	var data []byte
	e := s.pool.QueryRow(ctx, `SELECT id,actor_id,account_id,origin_product_id,models,task_id,expires_at FROM model_credentials WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>now()`, digest(token)).Scan(&c.ID, &c.ActorID, &c.AccountID, &c.OriginProductID, &data, &c.TaskID, &c.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return c, ErrCredential
	}
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(data, &c.Models); e != nil {
		return c, e
	}
	if model != "" && !slices.Contains(c.Models, model) {
		return Claims{}, ErrCredential
	}
	if _, e = s.eligible(ctx, c.ActorID, c.AccountID, c.OriginProductID); e != nil {
		return Claims{}, ErrCredential
	}
	c.Audience = "llmrouter"
	return c, nil
}
func (s *Service) Revoke(ctx context.Context, actor, account, id string) error {
	return s.RevokeForProduct(ctx, actor, account, id, "")
}

// RevokeForProduct preserves account and actor checks while restricting product
// clients to their own immutable credential origin. Empty product is console scope.
func (s *Service) RevokeForProduct(ctx context.Context, actor, account, id, product string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if e = database.LockAccountTx(ctx, tx, account); e != nil {
		return e
	}
	role, e := s.accounts.RoleOn(ctx, tx, actor, account)
	if e != nil {
		return e
	}
	var owner, origin string
	if e = tx.QueryRow(ctx, `SELECT actor_id,origin_product_id FROM model_credentials WHERE id=$1 AND account_id=$2`, id, account).Scan(&owner, &origin); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return accounts.ErrNotFound
		}
		return e
	}
	if product != "" && product != origin {
		return accounts.ErrNotFound
	}
	if actor != owner && role != "owner" && role != "billing_admin" {
		return accounts.ErrForbidden
	}
	if _, e = tx.Exec(ctx, `UPDATE model_credentials SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(account_id,actor_id,operation_id,action) VALUES($1,$2,$3,'model_credential.revoked')`, account, actor, id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
