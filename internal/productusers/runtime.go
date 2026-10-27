package productusers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

type RuntimeBinding struct {
	ID                string `json:"id"`
	LocalUserID       string `json:"local_user_id"`
	UserID            string `json:"user_id"`
	Issuer            string `json:"issuer"`
	Subject           string `json:"subject"`
	PersonalAccountID string `json:"personal_account_id"`
	ProductID         string `json:"product_id"`
	InstanceID        string `json:"instance_id"`
}

const bindingColumns = "id,local_user_id,user_id,issuer,subject,personal_account_id,product_id,instance_id"

func scanBinding(row pgx.Row) (b RuntimeBinding, e error) {
	e = row.Scan(&b.ID, &b.LocalUserID, &b.UserID, &b.Issuer, &b.Subject, &b.PersonalAccountID, &b.ProductID, &b.InstanceID)
	if errors.Is(e, pgx.ErrNoRows) {
		e = accounts.ErrNotFound
	}
	return
}
func (s *Store) RuntimeBinding(ctx context.Context, product, instance, localID string) (RuntimeBinding, error) {
	return s.RuntimeBindingOn(ctx, s.pool, product, instance, localID)
}
func (s *Store) RuntimeBindingOn(ctx context.Context, q accounts.Querier, product, instance, localID string) (RuntimeBinding, error) {
	return scanBinding(q.QueryRow(ctx, `SELECT `+bindingColumns+` FROM product_runtime_bindings WHERE product_id=$1 AND instance_id=$2 AND local_user_id=$3`, product, Instance(instance), localID))
}
func (s *Store) RuntimeUser(ctx context.Context, userID string) (u accounts.User, e error) {
	e = s.pool.QueryRow(ctx, `SELECT id,name,email FROM users WHERE id=$1`, userID).Scan(&u.ID, &u.Name, &u.Email)
	if errors.Is(e, pgx.ErrNoRows) {
		e = accounts.ErrNotFound
	}
	return
}
func (s *Store) EnsureRuntimeBinding(ctx context.Context, product, instance, localID, bindingKey string, identity accounts.Identity, u accounts.User) (RuntimeBinding, error) {
	instance = Instance(instance)
	for _, v := range []string{product, instance, localID, bindingKey, identity.Issuer, identity.Subject, u.ID} {
		if strings.TrimSpace(v) != v || v == "" || len(v) > 512 {
			return RuntimeBinding{}, accounts.ErrInvalid
		}
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return RuntimeBinding{}, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "runtime-binding:"+product+"/"+instance); e != nil {
		return RuntimeBinding{}, e
	}
	var proven bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identities WHERE issuer=$1 AND subject=$2 AND user_id=$3)`, identity.Issuer, identity.Subject, u.ID).Scan(&proven); e != nil {
		return RuntimeBinding{}, e
	}
	if !proven {
		return RuntimeBinding{}, accounts.ErrForbidden
	}
	old, e := s.RuntimeBindingOn(ctx, tx, product, instance, localID)
	if e == nil {
		if old.UserID != u.ID {
			return RuntimeBinding{}, accounts.ErrConflict
		}
		return old, tx.Commit(ctx)
	}
	if !errors.Is(e, accounts.ErrNotFound) {
		return RuntimeBinding{}, e
	}
	sum := sha256.Sum256([]byte(product + "\x00" + instance + "\x00" + localID))
	b := RuntimeBinding{ID: "rb_" + hex.EncodeToString(sum[:]), LocalUserID: localID, UserID: u.ID, Issuer: identity.Issuer, Subject: identity.Subject, ProductID: product, InstanceID: instance}
	if e = tx.QueryRow(ctx, `SELECT id FROM accounts WHERE kind='personal' AND personal_user_id=$1`, u.ID).Scan(&b.PersonalAccountID); e != nil {
		return RuntimeBinding{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO product_runtime_bindings(id,product_id,instance_id,local_user_id,user_id,issuer,subject,personal_account_id,binding_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, b.ID, product, instance, localID, u.ID, identity.Issuer, identity.Subject, b.PersonalAccountID, bindingKey)
	var pe *pgconn.PgError
	if errors.As(e, &pe) && pe.Code == "23505" {
		return RuntimeBinding{}, accounts.ErrConflict
	}
	if e != nil {
		return RuntimeBinding{}, e
	}
	return b, tx.Commit(ctx)
}

type RuntimeAccess struct {
	Binding RuntimeBinding `json:"binding"`
	Access  Access         `json:"access"`
}

func (s *Store) ResolveRuntime(ctx context.Context, product, instance, localID string) (out RuntimeAccess, e error) {
	if !s.Managed(product, instance) {
		return out, accounts.ErrForbidden
	}
	out.Binding, e = s.RuntimeBinding(ctx, product, instance, localID)
	if e != nil {
		return
	}
	u, e := s.RuntimeUser(ctx, out.Binding.UserID)
	if e != nil {
		return out, e
	}
	out.Access, e = s.Resolve(ctx, u, product, instance)
	out.Access.ExpiresAt = time.Now().Add(time.Minute).Unix()
	return
}

type RuntimeManagedUser struct {
	User       User       `json:"user"`
	Assignment Assignment `json:"assignment"`
}

func (s *Store) RuntimeUsers(ctx context.Context, actor, product, instance, search string) ([]RuntimeManagedUser, error) {
	if !s.Managed(product, instance) {
		return nil, accounts.ErrForbidden
	}
	if e := s.RequirePermissionOn(ctx, s.pool, actor, product, instance, "user:read"); e != nil {
		return nil, e
	}
	rows, e := s.pool.Query(ctx, `SELECT u.id,u.name,u.email FROM users u JOIN product_runtime_bindings b ON b.user_id=u.id WHERE b.product_id=$1 AND b.instance_id=$2 AND ($3='' OR u.name ILIKE '%'||$3||'%' OR u.email ILIKE '%'||$3||'%' OR b.local_user_id=$3) ORDER BY u.id LIMIT 500`, product, Instance(instance), search)
	if e != nil {
		return nil, e
	}
	us := []User{}
	for rows.Next() {
		var u User
		if e = rows.Scan(&u.ID, &u.Name, &u.Email); e != nil {
			rows.Close()
			return nil, e
		}
		us = append(us, u)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	out := []RuntimeManagedUser{}
	for _, u := range us {
		a, e := s.Assignments(ctx, u.ID)
		if e != nil {
			return nil, e
		}
		for _, v := range a {
			if v.ProductID == product && v.InstanceID == Instance(instance) {
				out = append(out, RuntimeManagedUser{u, v})
			}
		}
	}
	return out, nil
}

// SetRuntimeAccess delegates only the caller's existing product permissions.
// It never changes the global identity status or another product's assignment.
func (s *Store) SetRuntimeAccess(ctx context.Context, actor, target, product, instance string, roles []string, active bool, expected int64) (Assignment, error) {
	instance = Instance(instance)
	m, ok := s.registry.Get(product, instance)
	if !ok || expected < 0 {
		return Assignment{}, accounts.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Assignment{}, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "runtime-access:"+product+"/"+instance); e != nil {
		return Assignment{}, e
	}
	// Lock current actor and target assignments in deterministic order.
	rows, err := tx.Query(ctx, `SELECT user_id FROM product_user_access WHERE product_id=$1 AND instance_id=$2 AND user_id IN($3,$4) ORDER BY user_id FOR UPDATE`, product, instance, actor, target)
	if err != nil {
		return Assignment{}, err
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return Assignment{}, err
	}
	if e = s.RequirePermissionOn(ctx, tx, actor, product, instance, "user:write"); e != nil {
		return Assignment{}, e
	}
	var actorUser accounts.User
	if e = tx.QueryRow(ctx, `SELECT id,name,email FROM users WHERE id=$1`, actor).Scan(&actorUser.ID, &actorUser.Name, &actorUser.Email); e != nil {
		return Assignment{}, e
	}
	actorAccess, e := s.ResolveOn(ctx, tx, actorUser, product, instance)
	if e != nil {
		return Assignment{}, e
	}
	var targetRole int
	var targetRoles []byte
	e = tx.QueryRow(ctx, `SELECT product_role,roles FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, target, product, instance).Scan(&targetRole, &targetRoles)
	if errors.Is(e, pgx.ErrNoRows) {
		targetRole = 1
	} else if e != nil {
		return Assignment{}, e
	}
	var assigned []string
	if len(targetRoles) > 0 {
		if e = json.Unmarshal(targetRoles, &assigned); e != nil {
			return Assignment{}, e
		}
	}
	targetRole = EffectiveProductRole(m, assigned, targetRole)
	if actor == target || targetRole >= actorAccess.ProductRole && actorAccess.ProductRole < 100 || actorAccess.ProductRole < 10 {
		return Assignment{}, accounts.ErrForbidden
	}
	var bound bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_runtime_bindings WHERE product_id=$1 AND instance_id=$2 AND user_id=$3)`, product, instance, target).Scan(&bound); e != nil {
		return Assignment{}, e
	}
	if !bound {
		return Assignment{}, accounts.ErrNotFound
	}
	roles = normalize(roles)
	for _, id := range roles {
		found := false
		for _, role := range m.Roles {
			if role.ID != id {
				continue
			}
			found = true
			if role.ProductRole >= actorAccess.ProductRole && actorAccess.ProductRole < 100 {
				return Assignment{}, accounts.ErrForbidden
			}
			for _, p := range role.Permissions {
				if e = s.RequirePermissionOn(ctx, tx, actor, product, instance, p); e != nil {
					return Assignment{}, accounts.ErrForbidden
				}
			}
		}
		if !found {
			return Assignment{}, accounts.ErrInvalid
		}
	}
	var current int64
	e = tx.QueryRow(ctx, `SELECT version FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3 FOR UPDATE`, target, product, instance).Scan(&current)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return Assignment{}, e
	}
	if current != expected {
		return Assignment{}, accounts.ErrConflict
	}
	v := Assignment{product, instance, roles, active, expected + 1, true, m.Version}
	raw, _ := json.Marshal(roles)
	if _, e = tx.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version,product_role) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(user_id,product_id,instance_id) DO UPDATE SET roles=excluded.roles,active=excluded.active,version=excluded.version,product_role=excluded.product_role,permission_overrides='{}',updated_at=now()`, target, product, instance, raw, active, v.Version, baselineProductRole(m, roles)); e != nil {
		return Assignment{}, e
	}
	snapshot, _ := json.Marshal(v)
	if _, e = tx.Exec(ctx, `INSERT INTO product_user_audit(actor_id,target_id,product_id,instance_id,action,version,snapshot) VALUES($1,$2,$3,$4,'access',$5,$6)`, actor, target, product, instance, v.Version, snapshot); e != nil {
		return Assignment{}, e
	}
	return v, tx.Commit(ctx)
}
