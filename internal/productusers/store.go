package productusers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
type Access struct {
	Issuer      string   `json:"issuer"`
	Subject     string   `json:"subject"`
	ProductID   string   `json:"product_id"`
	InstanceID  string   `json:"instance_id"`
	User        User     `json:"user"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
	Version     int64    `json:"version"`
	Active      bool     `json:"active"`
	ExpiresAt   int64    `json:"expires_at"`
}
type Assignment struct {
	ProductID       string   `json:"product_id"`
	InstanceID      string   `json:"instance_id"`
	Roles           []string `json:"roles"`
	Active          bool     `json:"active"`
	Version         int64    `json:"assignment_version"`
	Explicit        bool     `json:"explicit"`
	ManifestVersion int64    `json:"manifest_version"`
}
type ManagedUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Active  bool   `json:"active"`
	Version int64  `json:"version"`
}
type Application struct {
	Access
	Name     string `json:"name"`
	EntryURL string `json:"entry_url"`
}
type Store struct {
	pool     *pgxpool.Pool
	registry Registry
	issuer   string
	admins   []string
}

func New(pool *pgxpool.Pool, registry Registry, issuer string, admins []string) *Store {
	return &Store{pool, registry, strings.TrimSuffix(issuer, "/"), normalize(admins)}
}
func (s *Store) Enabled() bool { return len(s.admins) > 0 }
func (s *Store) CanManage(user string) bool {
	for _, id := range s.admins {
		if user == id {
			return true
		}
	}
	return false
}
func (s *Store) Products() []Manifest { return s.registry.List() }
func (s *Store) Managed(product, instance string) bool {
	_, ok := s.registry.Get(product, instance)
	return ok
}

// RequirePermissionOn reads the current assignment on the caller's connection.
// Billing already holds the payer lock and must not acquire another pool slot.
func (s *Store) RequirePermissionOn(ctx context.Context, q accounts.Querier, actor, product, instance, permission string) error {
	m, managed := s.registry.Get(product, instance)
	if !managed {
		return nil
	}
	var active bool
	var roles *json.RawMessage
	var assignmentActive *bool
	if e := q.QueryRow(ctx, `SELECT u.active,a.roles,a.active FROM users u LEFT JOIN product_user_access a ON a.user_id=u.id AND a.product_id=$2 AND a.instance_id=$3 WHERE u.id=$1`, actor, product, instance).Scan(&active, &roles, &assignmentActive); e != nil {
		return e
	}
	if !active || (assignmentActive != nil && !*assignmentActive) {
		return accounts.ErrForbidden
	}
	assigned := m.DefaultRoles
	if roles != nil && json.Unmarshal(*roles, &assigned) != nil {
		return accounts.ErrForbidden
	}
	allowed := false
	for _, id := range assigned {
		found := false
		for _, role := range m.Roles {
			if role.ID != id {
				continue
			}
			found = true
			for _, p := range role.Permissions {
				if p == permission {
					allowed = true
				}
			}
			break
		}
		if !found {
			return accounts.ErrForbidden
		}
	}
	if !allowed {
		return accounts.ErrForbidden
	}
	return nil
}
func (s *Store) CheckActive(ctx context.Context, user string) error {
	var active bool
	if e := s.pool.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, user).Scan(&active); e != nil {
		return e
	}
	if !active {
		return accounts.ErrForbidden
	}
	return nil
}
func (s *Store) Resolve(ctx context.Context, u accounts.User, product, instance string) (Access, error) {
	m, ok := s.registry.Get(product, instance)
	if !ok {
		return Access{}, accounts.ErrNotFound
	}
	var active bool
	var statusVersion int64
	var raw *json.RawMessage
	var enabled *bool
	var ver *int64
	e := s.pool.QueryRow(ctx, `SELECT u.active,u.status_version,a.roles,a.active,a.version FROM users u LEFT JOIN product_user_access a ON a.user_id=u.id AND a.product_id=$2 AND a.instance_id=$3 WHERE u.id=$1`, u.ID, m.ProductID, m.InstanceID).Scan(&active, &statusVersion, &raw, &enabled, &ver)
	if e != nil {
		return Access{}, e
	}
	var roles []byte
	assignmentActive := true
	var version int64
	if raw != nil {
		roles = []byte(*raw)
		assignmentActive = *enabled
		version = *ver
	}
	if !active || (roles != nil && !assignmentActive) {
		return Access{}, accounts.ErrForbidden
	}
	assigned := m.DefaultRoles
	if roles != nil {
		if json.Unmarshal(roles, &assigned) != nil {
			return Access{}, accounts.ErrForbidden
		}
	}
	assigned = normalize(assigned)
	permissions := []string{}
	for _, id := range assigned {
		found := false
		for _, role := range m.Roles {
			if role.ID == id {
				permissions = append(permissions, role.Permissions...)
				found = true
				break
			}
		}
		if !found {
			return Access{}, accounts.ErrForbidden
		}
	}
	return Access{Issuer: s.issuer, Subject: u.ID, ProductID: m.ProductID, InstanceID: m.InstanceID, User: User{u.ID, u.Name, u.Email}, Roles: assigned, Permissions: normalize(permissions), Version: m.Version + version + statusVersion - 1, Active: true}, nil
}
func (s *Store) Applications(ctx context.Context, u accounts.User, product, instance string) ([]Application, error) {
	out := []Application{}
	for _, m := range s.Products() {
		if product != "" && (product != m.ProductID || Instance(instance) != m.InstanceID) {
			continue
		}
		a, e := s.Resolve(ctx, u, m.ProductID, m.InstanceID)
		if errors.Is(e, accounts.ErrForbidden) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if len(a.Permissions) == 0 {
			continue
		}
		out = append(out, Application{a, m.Name, m.EntryURL})
	}
	return out, nil
}
func (s *Store) Users(ctx context.Context, search string, limit int) ([]ManagedUser, error) {
	if len(search) > 100 || limit < 1 || limit > 100 {
		return nil, accounts.ErrInvalid
	}
	rows, e := s.pool.Query(ctx, `SELECT id,name,email,active,status_version FROM users WHERE $1='' OR strpos(lower(name),lower($1))>0 OR strpos(lower(email),lower($1))>0 OR id=$1 ORDER BY created_at,id LIMIT $2`, search, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ManagedUser{}
	for rows.Next() {
		var u ManagedUser
		if e = rows.Scan(&u.ID, &u.Name, &u.Email, &u.Active, &u.Version); e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) Assignments(ctx context.Context, user string) ([]Assignment, error) {
	var exists bool
	if e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, user).Scan(&exists); e != nil {
		return nil, e
	}
	if !exists {
		return nil, accounts.ErrNotFound
	}
	out := []Assignment{}
	for _, m := range s.Products() {
		v := Assignment{ProductID: m.ProductID, InstanceID: m.InstanceID, Roles: m.DefaultRoles, Active: true, ManifestVersion: m.Version}
		var raw []byte
		e := s.pool.QueryRow(ctx, `SELECT roles,active,version FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, user, m.ProductID, m.InstanceID).Scan(&raw, &v.Active, &v.Version)
		if e == nil {
			v.Explicit = true
			if json.Unmarshal(raw, &v.Roles) != nil {
				return nil, accounts.ErrInvalid
			}
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Store) mutation(ctx context.Context, actor, target string, fn func(pgx.Tx) error) error {
	if !s.CanManage(actor) {
		return accounts.ErrForbidden
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:user-administration',0))`); e != nil {
		return e
	}
	var active bool
	if e = tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, actor).Scan(&active); e != nil || !active {
		return accounts.ErrForbidden
	}
	if e = tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1 FOR UPDATE`, target).Scan(&active); errors.Is(e, pgx.ErrNoRows) {
		return accounts.ErrNotFound
	} else if e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) SetAccess(ctx context.Context, actor, target, product, instance string, roles []string, active bool, expected int64) (Assignment, error) {
	m, ok := s.registry.Get(product, instance)
	if !ok {
		return Assignment{}, accounts.ErrNotFound
	}
	if expected < 0 {
		return Assignment{}, accounts.ErrInvalid
	}
	roles = normalize(roles)
	for _, id := range roles {
		found := false
		for _, role := range m.Roles {
			if role.ID == id {
				found = true
			}
		}
		if !found {
			return Assignment{}, accounts.ErrInvalid
		}
	}
	v := Assignment{m.ProductID, m.InstanceID, roles, active, expected + 1, true, m.Version}
	raw, _ := json.Marshal(roles)
	e := s.mutation(ctx, actor, target, func(tx pgx.Tx) error {
		var current int64
		e := tx.QueryRow(ctx, `SELECT version FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, target, m.ProductID, m.InstanceID).Scan(&current)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if current != expected {
			return accounts.ErrConflict
		}
		_, e = tx.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id,product_id,instance_id) DO UPDATE SET roles=excluded.roles,active=excluded.active,version=excluded.version,updated_at=now()`, target, m.ProductID, m.InstanceID, raw, active, v.Version)
		if e != nil {
			return e
		}
		snapshot, _ := json.Marshal(v)
		_, e = tx.Exec(ctx, `INSERT INTO product_user_audit(actor_id,target_id,product_id,instance_id,action,version,snapshot) VALUES($1,$2,$3,$4,'access',$5,$6)`, actor, target, m.ProductID, m.InstanceID, v.Version, snapshot)
		return e
	})
	return v, e
}
func (s *Store) SetStatus(ctx context.Context, actor, target string, active bool, expected int64) (ManagedUser, error) {
	var u ManagedUser
	if expected < 1 {
		return u, accounts.ErrInvalid
	}
	e := s.mutation(ctx, actor, target, func(tx pgx.Tx) error {
		if !active && s.CanManage(target) {
			var remaining int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=ANY($1) AND active AND id<>$2`, s.admins, target).Scan(&remaining); e != nil {
				return e
			}
			if remaining == 0 {
				return accounts.ErrForbidden
			}
		}
		e := tx.QueryRow(ctx, `UPDATE users SET active=$2,status_version=status_version+1 WHERE id=$1 AND status_version=$3 RETURNING id,name,email,active,status_version`, target, active, expected).Scan(&u.ID, &u.Name, &u.Email, &u.Active, &u.Version)
		if errors.Is(e, pgx.ErrNoRows) {
			return accounts.ErrConflict
		}
		if e != nil {
			return e
		}
		snapshot, _ := json.Marshal(u)
		_, e = tx.Exec(ctx, `INSERT INTO product_user_audit(actor_id,target_id,action,version,snapshot) VALUES($1,$2,'status',$3,$4)`, actor, target, u.Version, snapshot)
		return e
	})
	return u, e
}
