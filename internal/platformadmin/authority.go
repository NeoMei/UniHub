package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"slices"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

type Principal struct {
	UserID          string   `json:"user_id"`
	Roles           []string `json:"roles"`
	Permissions     []string `json:"permissions"`
	Enabled         bool     `json:"enabled"`
	Version         int64    `json:"version"`
	LegacyUserAdmin bool     `json:"legacy_user_admin"`
	Source          string   `json:"source,omitempty"`
}

var allPermissions = []string{"users:read", "users:write", "users:status", "users:sessions", "admins:manage", "accounts:read", "teams:write", "teams:privileged", "products:read", "access:write", "access:privileged", "finance:read", "finance:write", "keys:read", "keys:revoke", "calls:read", "audit:read", "system:read", "export:read", "identity:manage"}
var rolePermissions = map[string][]string{
	"super_admin":   allPermissions,
	"user_admin":    {"users:read", "users:write", "users:status", "users:sessions", "accounts:read", "teams:write", "products:read", "access:write", "identity:manage", "keys:revoke", "audit:read", "system:read", "export:read"},
	"finance_admin": {"users:read", "finance:read", "finance:write", "accounts:read", "products:read", "keys:read", "calls:read", "audit:read", "system:read", "export:read"},
	"auditor":       {"users:read", "accounts:read", "products:read", "finance:read", "keys:read", "calls:read", "audit:read", "system:read", "export:read"},
}
var legacyPermissions = []string{"users:read", "users:status", "products:read", "access:write"}

func principalOn(ctx context.Context, q accounts.Querier, actor string) (Principal, error) {
	p := Principal{UserID: actor, Roles: []string{}, Permissions: []string{}}
	var raw []byte
	var active bool
	e := q.QueryRow(ctx, `SELECT u.active,COALESCE(a.roles,'[]'),COALESCE(a.legacy_user_admin,false),COALESCE(a.version,0),COALESCE(a.source,'') FROM users u LEFT JOIN platform_admin_roles a ON a.user_id=u.id WHERE u.id=$1`, actor).Scan(&active, &raw, &p.LegacyUserAdmin, &p.Version, &p.Source)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, accounts.ErrForbidden
	}
	if e != nil {
		return p, e
	}
	if json.Unmarshal(raw, &p.Roles) != nil {
		return p, accounts.ErrForbidden
	}
	for _, r := range p.Roles {
		ps, ok := rolePermissions[r]
		if !ok {
			return p, accounts.ErrForbidden
		}
		p.Permissions = append(p.Permissions, ps...)
	}
	if p.LegacyUserAdmin {
		p.Permissions = append(p.Permissions, legacyPermissions...)
	}
	slices.Sort(p.Permissions)
	p.Permissions = slices.Compact(p.Permissions)
	p.Enabled = active && len(p.Permissions) > 0
	if !active {
		p.Permissions = []string{}
	}
	return p, nil
}
func (s *Store) Principal(ctx context.Context, actor string) (Principal, error) {
	return principalOn(ctx, s.pool, actor)
}
func (s *Store) requireOn(ctx context.Context, q accounts.Querier, actor, permission string) error {
	p, e := principalOn(ctx, q, actor)
	if e != nil {
		return e
	}
	if !p.Enabled || !slices.Contains(p.Permissions, permission) {
		return accounts.ErrForbidden
	}
	return nil
}
func (s *Store) Require(ctx context.Context, actor, permission string) error {
	return s.requireOn(ctx, s.pool, actor, permission)
}
func (s *Store) requireTargetOn(ctx context.Context, q accounts.Querier, actor, target, permission string) error {
	if e := s.requireOn(ctx, q, actor, permission); e != nil {
		return e
	}
	p, e := principalOn(ctx, q, actor)
	if e != nil {
		return e
	}
	if slices.Contains(p.Roles, "super_admin") {
		return nil
	}
	var protected bool
	e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_admin_roles WHERE user_id=$1 AND (roles<>'[]' OR legacy_user_admin))`, target).Scan(&protected)
	if e != nil {
		return e
	}
	if protected {
		return accounts.ErrForbidden
	}
	return nil
}
func (s *Store) RequireTarget(ctx context.Context, actor, target, permission string) error {
	return s.requireTargetOn(ctx, s.pool, actor, target, permission)
}
func normalizedRoles(roles []string) ([]string, error) {
	out := append([]string{}, roles...)
	slices.Sort(out)
	out = slices.Compact(out)
	for _, r := range out {
		if _, ok := rolePermissions[r]; !ok {
			return nil, accounts.ErrInvalid
		}
	}
	return out, nil
}
func (s *Store) SetRoles(ctx context.Context, actor, target string, roles []string, expected int64, reason string) (Principal, error) {
	var out Principal
	roles, e := normalizedRoles(roles)
	if e != nil || expected < 0 || !validReason(reason) {
		return out, accounts.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer database.Rollback(tx)
	if e = lockAuthority(ctx, tx); e != nil {
		return out, e
	}
	if e = s.requireOn(ctx, tx, actor, "admins:manage"); e != nil {
		return out, e
	}
	var active bool
	if e = tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1 FOR UPDATE`, target).Scan(&active); errors.Is(e, pgx.ErrNoRows) {
		return out, accounts.ErrNotFound
	} else if e != nil {
		return out, e
	}
	if !active && len(roles) > 0 {
		return out, accounts.ErrInvalid
	}
	old, e := principalOn(ctx, tx, target)
	if e != nil {
		return out, e
	}
	if old.Version != expected {
		return out, accounts.ErrConflict
	}
	if slices.Contains(old.Roles, "super_admin") && !slices.Contains(roles, "super_admin") {
		if actor == target {
			return out, accounts.ErrForbidden
		}
		if e = remainingSuper(ctx, tx, target); e != nil {
			return out, e
		}
	}
	raw, _ := json.Marshal(roles)
	_, e = tx.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,legacy_user_admin,version,source,granted_by) VALUES($1,$2,false,1,'explicit',$3) ON CONFLICT(user_id) DO UPDATE SET roles=excluded.roles,legacy_user_admin=false,version=platform_admin_roles.version+1,source='explicit',granted_by=excluded.granted_by,updated_at=now()`, target, raw, actor)
	if e != nil {
		return out, e
	}
	out, e = principalOn(ctx, tx, target)
	if e != nil {
		return out, e
	}
	if e = s.WriteAudit(ctx, tx, actor, "admin.roles", target, reason, map[string]any{"before": old, "after": out}); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func remainingSuper(ctx context.Context, tx pgx.Tx, target string) error {
	var n int
	e := tx.QueryRow(ctx, `SELECT count(*) FROM platform_admin_roles a JOIN users u ON u.id=a.user_id WHERE u.active AND a.user_id<>$1 AND a.roles ? 'super_admin'`, target).Scan(&n)
	if e != nil {
		return e
	}
	if n == 0 {
		return accounts.ErrForbidden
	}
	return nil
}

// Bootstrap is a deployment-tool operation, never exposed as an HTTP route.
func (s *Store) Bootstrap(ctx context.Context, actor, target, reason string) error {
	return s.bootstrap(ctx, actor, target, reason, false)
}
func (s *Store) RecoverSuperAdmin(ctx context.Context, actor, target, reason string) error {
	return s.bootstrap(ctx, actor, target, reason, true)
}
func (s *Store) bootstrap(ctx context.Context, actor, target, reason string, recover bool) error {
	if !validReason(reason) || actor == "" {
		return accounts.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if e = lockAuthority(ctx, tx); e != nil {
		return e
	}
	var active bool
	if e = tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1 FOR UPDATE`, target).Scan(&active); errors.Is(e, pgx.ErrNoRows) {
		return accounts.ErrNotFound
	} else if e != nil {
		return e
	}
	if !active {
		return accounts.ErrForbidden
	}
	var n int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM platform_admin_roles WHERE roles ? 'super_admin'`).Scan(&n); e != nil {
		return e
	}
	if n > 0 && !recover {
		return accounts.ErrConflict
	}
	_, e = tx.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,'["super_admin"]','bootstrap',$2) ON CONFLICT(user_id) DO UPDATE SET roles=(SELECT jsonb_agg(DISTINCT value) FROM jsonb_array_elements(platform_admin_roles.roles || '["super_admin"]') value),version=platform_admin_roles.version+1,source='recovery',granted_by=excluded.granted_by,updated_at=now()`, target, actor)
	if e != nil {
		return e
	}
	action := "admin.bootstrap"
	if recover {
		action = "admin.recovery"
	}
	if e = s.WriteAudit(ctx, tx, actor, action, target, reason, map[string]any{"role": "super_admin"}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// ImportLegacy inserts once. A retained row is a tombstone after later revocation.
func (s *Store) ImportLegacy(ctx context.Context, actor string, ids []string, reason string) error {
	if actor == "" || !validReason(reason) {
		return accounts.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer database.Rollback(tx)
	if e = lockAuthority(ctx, tx); e != nil {
		return e
	}
	for _, id := range ids {
		var exists bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, id).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return accounts.ErrNotFound
		}
		tag, e := tx.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,legacy_user_admin,source,granted_by) VALUES($1,true,'legacy-user-admin',$2) ON CONFLICT(user_id) DO NOTHING`, id, actor)
		if e != nil {
			return e
		}
		if tag.RowsAffected() > 0 {
			if e = s.WriteAudit(ctx, tx, actor, "admin.legacy-import", id, reason, map[string]any{"legacy_user_admin": true}); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
