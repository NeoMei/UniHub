package platformadmin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"slices"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productusers"
)

type User struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Email               string     `json:"email"`
	Active              bool       `json:"active"`
	Version             int64      `json:"version"`
	CreatedAt           time.Time  `json:"created_at"`
	LastAuthenticatedAt *time.Time `json:"last_authenticated_at"`
	Principal           *Principal `json:"principal,omitempty"`
}
type UserIdentity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}
type UserAccount struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Role string `json:"role"`
}
type UserDetail struct {
	User          User                      `json:"user"`
	Identities    []UserIdentity            `json:"identities"`
	Accounts      []UserAccount             `json:"accounts"`
	ProductAccess []productusers.Assignment `json:"product_access"`
	Principal     Principal                 `json:"principal"`
}
type directoryCursor struct {
	Created time.Time `json:"created"`
	ID      string    `json:"id"`
	AsOf    time.Time `json:"as_of"`
	Filter  string    `json:"filter"`
}

var directorySigningKey = func() []byte {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return b
}()

func encodeDirectoryCursor(c directoryCursor) string {
	raw, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, directorySigningKey)
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func decodeDirectoryCursor(v string, c *directoryCursor) error {
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return accounts.ErrInvalid
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return accounts.ErrInvalid
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, directorySigningKey)
	mac.Write(raw)
	if e != nil || !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(raw, c) != nil {
		return accounts.ErrInvalid
	}
	return nil
}
func directoryOptions(o ListOptions, scope ...string) (ListOptions, directoryCursor, error) {
	if o.Limit == 0 {
		o.Limit = 50
	}
	if (!o.CreatedFrom.IsZero() && !o.CreatedTo.IsZero() && !o.CreatedFrom.Before(o.CreatedTo)) || o.Limit < 1 || o.Limit > 100 || len(o.Search) > 100 || len(o.Cursor) > 4096 || (o.Status != "" && o.Status != "active" && o.Status != "disabled") {
		return o, directoryCursor{}, accounts.ErrInvalid
	}
	if o.Role != "" {
		if _, ok := rolePermissions[o.Role]; !ok && o.Role != "legacy_user_admin" && o.Role != "any" {
			return o, directoryCursor{}, accounts.ErrInvalid
		}
	}
	hash := sha256.Sum256([]byte(o.Search + "\x00" + o.Status + "\x00" + o.Role + "\x00" + strings.Join(scope, "\x00") + "\x00" + o.CreatedFrom.UTC().Format(time.RFC3339Nano) + "\x00" + o.CreatedTo.UTC().Format(time.RFC3339Nano)))
	filter := hex.EncodeToString(hash[:])
	c := directoryCursor{AsOf: time.Now().UTC(), Filter: filter}
	if o.Cursor != "" {
		e := decodeDirectoryCursor(o.Cursor, &c)
		if e != nil || c.Filter != filter || c.ID == "" || c.AsOf.IsZero() || time.Since(c.AsOf) > 24*time.Hour || c.AsOf.After(time.Now().Add(time.Minute)) {
			return o, c, accounts.ErrInvalid
		}
	}
	return o, c, nil
}
func (s *Store) ListUsers(ctx context.Context, actor string, opts ListOptions) (Page[User], error) {
	var out Page[User]
	if e := s.Require(ctx, actor, "users:read"); e != nil {
		return out, e
	}
	o, c, e := directoryOptions(opts, "users", actor)
	if e != nil {
		return out, e
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer database.Rollback(tx)
	where := `u.created_at<=$1 AND ($2='' OR strpos(lower(u.name),lower($2))>0 OR strpos(lower(u.email),lower($2))>0 OR u.id=$2) AND ($3='' OR u.active=($3='active')) AND ($4='' OR ($4='any' AND (a.roles<>'[]' OR a.legacy_user_admin)) OR ($4='legacy_user_admin' AND a.legacy_user_admin) OR a.roles ? $4) AND ($5::timestamptz IS NULL OR u.created_at>=$5) AND ($6::timestamptz IS NULL OR u.created_at<$6)`
	var from, to *time.Time
	if !o.CreatedFrom.IsZero() {
		from = &o.CreatedFrom
	}
	if !o.CreatedTo.IsZero() {
		to = &o.CreatedTo
	}
	args := []any{c.AsOf, o.Search, o.Status, o.Role, from, to}
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM users u LEFT JOIN platform_admin_roles a ON a.user_id=u.id WHERE `+where, args...).Scan(&out.Total); e != nil {
		return out, e
	}
	args = append(args, c.Created, c.ID, o.Limit+1)
	rows, e := tx.Query(ctx, `SELECT u.id,u.name,u.email,u.active,u.status_version,u.created_at,u.last_authenticated_at FROM users u LEFT JOIN platform_admin_roles a ON a.user_id=u.id WHERE `+where+` AND ($8='' OR (u.created_at,u.id)>($7,$8)) ORDER BY u.created_at,u.id LIMIT $9`, args...)
	if e != nil {
		return out, e
	}
	out.Items = []User{}
	for rows.Next() {
		var u User
		if e = rows.Scan(&u.ID, &u.Name, &u.Email, &u.Active, &u.Version, &u.CreatedAt, &u.LastAuthenticatedAt); e != nil {
			rows.Close()
			return out, e
		}
		out.Items = append(out.Items, u)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Items) > o.Limit {
		out.Items = out.Items[:o.Limit]
		last := out.Items[len(out.Items)-1]
		c.Created, c.ID = last.CreatedAt, last.ID
		out.NextCursor = encodeDirectoryCursor(c)
	}
	out.AsOf = c.AsOf
	return out, tx.Commit(ctx)
}
func (s *Store) ListAdmins(ctx context.Context, actor string, o ListOptions) (Page[User], error) {
	var out Page[User]
	if e := s.Require(ctx, actor, "admins:manage"); e != nil {
		return out, e
	}
	if o.Role == "" {
		o.Role = "any"
	}
	out, e := s.ListUsers(ctx, actor, o)
	if e != nil {
		return out, e
	}
	for i := range out.Items {
		p, e := s.Principal(ctx, out.Items[i].ID)
		if e != nil {
			return out, e
		}
		out.Items[i].Principal = &p
	}
	return out, nil
}
func (s *Store) GetUser(ctx context.Context, actor, target string) (UserDetail, error) {
	out := UserDetail{Identities: []UserIdentity{}, Accounts: []UserAccount{}, ProductAccess: []productusers.Assignment{}}
	if e := s.Require(ctx, actor, "users:read"); e != nil {
		return out, e
	}
	e := s.pool.QueryRow(ctx, `SELECT id,name,email,active,status_version,created_at,last_authenticated_at FROM users WHERE id=$1`, target).Scan(&out.User.ID, &out.User.Name, &out.User.Email, &out.User.Active, &out.User.Version, &out.User.CreatedAt, &out.User.LastAuthenticatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, accounts.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	rows, e := s.pool.Query(ctx, `SELECT issuer,subject FROM identities WHERE user_id=$1 ORDER BY issuer,subject`, target)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var i UserIdentity
		if e = rows.Scan(&i.Issuer, &i.Subject); e != nil {
			rows.Close()
			return out, e
		}
		out.Identities = append(out.Identities, i)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = s.pool.Query(ctx, `SELECT a.id,a.kind,a.name,m.role FROM accounts a JOIN account_members m ON m.account_id=a.id WHERE m.user_id=$1 ORDER BY a.created_at,a.id`, target)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var a UserAccount
		if e = rows.Scan(&a.ID, &a.Kind, &a.Name, &a.Role); e != nil {
			rows.Close()
			return out, e
		}
		out.Accounts = append(out.Accounts, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	out.Principal, e = s.Principal(ctx, target)
	if e != nil {
		return out, e
	}
	if s.products != nil {
		out.ProductAccess, e = s.products.Assignments(ctx, target)
	}
	return out, e
}
func (s *Store) userMutation(ctx context.Context, actor, target, permission, reason string, fn func(pgx.Tx) error) error {
	if !validReason(reason) {
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
	if e = s.requireTargetOn(ctx, tx, actor, target, permission); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1 FOR UPDATE`, target).Scan(&exists); errors.Is(e, pgx.ErrNoRows) {
		return accounts.ErrNotFound
	} else if e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) SetStatus(ctx context.Context, actor, target string, active bool, expected int64, reason string) (User, error) {
	return s.setStatus(ctx, actor, target, active, expected, reason, false)
}
func (s *Store) SetOrdinaryStatus(ctx context.Context, actor, target string, active bool, expected int64, reason string) (User, error) {
	return s.setStatus(ctx, actor, target, active, expected, reason, true)
}
func (s *Store) setStatus(ctx context.Context, actor, target string, active bool, expected int64, reason string, ordinaryOnly bool) (User, error) {
	var out User
	if expected < 1 {
		return out, accounts.ErrInvalid
	}
	e := s.userMutation(ctx, actor, target, "users:status", reason, func(tx pgx.Tx) error {
		old, e := principalOn(ctx, tx, target)
		if e != nil {
			return e
		}
		if ordinaryOnly && (len(old.Roles) > 0 || old.LegacyUserAdmin) {
			return accounts.ErrForbidden
		}
		if !active && slices.Contains(old.Roles, "super_admin") {
			if actor == target {
				return accounts.ErrForbidden
			}
			if e = remainingSuper(ctx, tx, target); e != nil {
				return e
			}
		}
		e = tx.QueryRow(ctx, `UPDATE users SET active=$2,status_version=status_version+1 WHERE id=$1 AND status_version=$3 RETURNING id,name,email,active,status_version,created_at,last_authenticated_at`, target, active, expected).Scan(&out.ID, &out.Name, &out.Email, &out.Active, &out.Version, &out.CreatedAt, &out.LastAuthenticatedAt)
		if errors.Is(e, pgx.ErrNoRows) {
			return accounts.ErrConflict
		}
		if e != nil {
			return e
		}
		if !active {
			if _, e = revokeLocalOn(ctx, tx, target, true); e != nil {
				return e
			}
		}
		return s.WriteAudit(ctx, tx, actor, "user.status", target, reason, map[string]any{"active": active, "previous_version": expected, "version": out.Version})
	})
	return out, e
}

type Revocation struct {
	Scope            string    `json:"scope"`
	BrowserSessions  int64     `json:"browser_sessions"`
	Keys             int64     `json:"keys"`
	SessionNotBefore time.Time `json:"session_not_before"`
	Version          int64     `json:"version"`
	UpstreamStatus   string    `json:"upstream_status"`
}

func revokeLocalOn(ctx context.Context, tx pgx.Tx, target string, keys bool) (Revocation, error) {
	out := Revocation{Scope: "unihub_local", UpstreamStatus: "not_requested"}
	e := tx.QueryRow(ctx, `UPDATE users SET session_not_before=GREATEST(session_not_before,date_trunc('second',clock_timestamp())+interval '1 second') WHERE id=$1 RETURNING session_not_before,status_version`, target).Scan(&out.SessionNotBefore, &out.Version)
	if e != nil {
		return out, e
	}
	tag, e := tx.Exec(ctx, `DELETE FROM browser_sessions s USING identities i WHERE i.user_id=$1 AND s.issuer=i.issuer AND s.subject=i.subject`, target)
	if e != nil {
		return out, e
	}
	out.BrowserSessions = tag.RowsAffected()
	if keys {
		// Durable user API keys are revoked on disable. Task credentials already
		// dispatched must remain available to the trusted settlement path.
		tag, e = tx.Exec(ctx, `UPDATE model_credentials SET revoked_at=COALESCE(revoked_at,now()) WHERE actor_id=$1 AND credential_kind='api_key' AND revoked_at IS NULL`, target)
		if e != nil {
			return out, e
		}
		out.Keys = tag.RowsAffected()
	}
	return out, nil
}
func (s *Store) RevokeLocalSessions(ctx context.Context, actor, target string, expected int64, reason string) (Revocation, error) {
	var out Revocation
	e := s.userMutation(ctx, actor, target, "users:sessions", reason, func(tx pgx.Tx) error {
		var current int64
		if e := tx.QueryRow(ctx, `SELECT status_version FROM users WHERE id=$1`, target).Scan(&current); e != nil {
			return e
		}
		if current != expected {
			return accounts.ErrConflict
		}
		var e error
		out, e = revokeLocalOn(ctx, tx, target, false)
		if e != nil {
			return e
		}
		return s.WriteAudit(ctx, tx, actor, "user.sessions.local-revoke", target, reason, out)
	})
	return out, e
}

type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func sessionReference(hash string) string {
	sum := sha256.Sum256([]byte("unihub:session-reference:" + hash))
	return hex.EncodeToString(sum[:])
}
func (s *Store) ListSessions(ctx context.Context, actor, target string, o ListOptions) (Page[Session], error) {
	out := Page[Session]{Items: []Session{}}
	if e := s.RequireTarget(ctx, actor, target, "users:sessions"); e != nil {
		return out, e
	}
	o, c, e := directoryOptions(o, "sessions", actor, target)
	if e != nil {
		return out, e
	}
	if o.Search != "" || o.Status != "" || o.Role != "" || !o.CreatedFrom.IsZero() || !o.CreatedTo.IsZero() {
		return out, accounts.ErrInvalid
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer database.Rollback(tx)
	source := ` FROM browser_sessions s JOIN identities i ON s.issuer=i.issuer AND s.subject=i.subject WHERE i.user_id=$1 AND s.expires_at>now() AND s.created_at<=$2`
	if e = tx.QueryRow(ctx, `SELECT count(*)`+source, target, c.AsOf).Scan(&out.Total); e != nil {
		return out, e
	}
	ref := `encode(sha256(convert_to('unihub:session-reference:'||s.id_hash,'UTF8')),'hex')`
	rows, e := tx.Query(ctx, `SELECT `+ref+`,s.created_at,s.expires_at`+source+` AND ($4='' OR (s.created_at,`+ref+`)>($3,$4)) ORDER BY s.created_at,`+ref+` LIMIT $5`, target, c.AsOf, c.Created, c.ID, o.Limit+1)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var item Session
		if e = rows.Scan(&item.ID, &item.CreatedAt, &item.ExpiresAt); e != nil {
			rows.Close()
			return out, e
		}
		out.Items = append(out.Items, item)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Items) > o.Limit {
		out.Items = out.Items[:o.Limit]
		last := out.Items[len(out.Items)-1]
		c.ID, c.Created = last.ID, last.CreatedAt
		out.NextCursor = encodeDirectoryCursor(c)
	}
	out.AsOf = c.AsOf
	return out, tx.Commit(ctx)
}

func (s *Store) RevokeSession(ctx context.Context, actor, target, reference string, expected int64, reason string) error {
	return s.userMutation(ctx, actor, target, "users:sessions", reason, func(tx pgx.Tx) error {
		var ver int64
		if e := tx.QueryRow(ctx, `SELECT status_version FROM users WHERE id=$1`, target).Scan(&ver); e != nil {
			return e
		}
		if ver != expected {
			return accounts.ErrConflict
		}
		rows, e := tx.Query(ctx, `SELECT s.id_hash FROM browser_sessions s JOIN identities i ON i.issuer=s.issuer AND i.subject=s.subject WHERE i.user_id=$1`, target)
		if e != nil {
			return e
		}
		hash := ""
		for rows.Next() {
			var h string
			if e = rows.Scan(&h); e != nil {
				rows.Close()
				return e
			}
			if sessionReference(h) == reference {
				hash = h
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if hash == "" {
			return accounts.ErrNotFound
		}
		if _, e = tx.Exec(ctx, `DELETE FROM browser_sessions WHERE id_hash=$1`, hash); e != nil {
			return e
		}
		return s.WriteAudit(ctx, tx, actor, "user.session.revoke", target, reason, map[string]any{"session_reference": reference, "version": ver})
	})
}
func (s *Store) UserProductAccess(ctx context.Context, actor, target string) ([]productusers.Assignment, error) {
	if e := s.Require(ctx, actor, "products:read"); e != nil {
		return nil, e
	}
	if s.products == nil {
		return []productusers.Assignment{}, nil
	}
	return s.products.Assignments(ctx, target)
}
func (s *Store) SetProductAccess(ctx context.Context, actor, target, product, instance string, roles []string, active bool, expected int64, reason string) (productusers.Assignment, error) {
	var out productusers.Assignment
	if s.products == nil {
		return out, accounts.ErrNotFound
	}
	instance = productusers.Instance(instance)
	var manifest productusers.Manifest
	found := false
	for _, m := range s.products.Products() {
		if m.ProductID == product && m.InstanceID == instance {
			manifest = m
			found = true
			break
		}
	}
	if !found {
		return out, accounts.ErrNotFound
	}
	if expected < 0 {
		return out, accounts.ErrInvalid
	}
	roles = append([]string{}, roles...)
	slices.Sort(roles)
	roles = slices.Compact(roles)
	privileged := false
	for _, id := range roles {
		exists := false
		for _, role := range manifest.Roles {
			if role.ID == id {
				exists = true
				privileged = privileged || role.Privileged
			}
		}
		if !exists {
			return out, accounts.ErrInvalid
		}
	}
	e := s.userMutation(ctx, actor, target, "access:write", reason, func(tx pgx.Tx) error {
		var current int64
		var oldRaw []byte
		e := tx.QueryRow(ctx, `SELECT version,roles FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, target, product, instance).Scan(&current, &oldRaw)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if current != expected {
			return accounts.ErrConflict
		}
		oldRoles := manifest.DefaultRoles
		if oldRaw != nil {
			if json.Unmarshal(oldRaw, &oldRoles) != nil {
				return accounts.ErrInvalid
			}
		}
		for _, role := range manifest.Roles {
			if role.Privileged && slices.Contains(oldRoles, role.ID) {
				privileged = true
			}
		}
		if privileged {
			if e = s.requireOn(ctx, tx, actor, "access:privileged"); e != nil {
				return e
			}
		}
		out = productusers.Assignment{ProductID: product, InstanceID: instance, Roles: roles, Active: active, Version: expected + 1, Explicit: true, ManifestVersion: manifest.Version}
		raw, _ := json.Marshal(roles)
		_, e = tx.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id,product_id,instance_id) DO UPDATE SET roles=excluded.roles,active=excluded.active,version=excluded.version,updated_at=now()`, target, product, instance, raw, active, out.Version)
		if e != nil {
			return e
		}
		snapshot, _ := json.Marshal(out)
		if _, e = tx.Exec(ctx, `INSERT INTO product_user_audit(actor_id,target_id,product_id,instance_id,action,version,snapshot) VALUES($1,$2,$3,$4,'access',$5,$6)`, actor, target, product, instance, out.Version, snapshot); e != nil {
			return e
		}
		return s.WriteAudit(ctx, tx, actor, "user.product-access", target, reason, map[string]any{"product_id": product, "instance_id": instance, "previous_version": expected, "assignment": out})
	})
	return out, e
}

// CredentialAllowed rejects bearer tokens without a reliable issue time after revocation.
func (s *Store) CredentialAllowed(ctx context.Context, user string, issuedAt int64) error {
	var active bool
	var cutoff time.Time
	if e := s.pool.QueryRow(ctx, `SELECT active,session_not_before FROM users WHERE id=$1`, user).Scan(&active, &cutoff); e != nil {
		return e
	}
	if !active || (!cutoff.Equal(time.Unix(0, 0)) && (issuedAt <= 0 || time.Unix(issuedAt, 0).Before(cutoff))) {
		return accounts.ErrForbidden
	}
	return nil
}

func (s *Store) RevokeUserKeys(ctx context.Context, actor, target string, expected int64, reason string) (int64, error) {
	var count int64
	e := s.userMutation(ctx, actor, target, "keys:revoke", reason, func(tx pgx.Tx) error {
		var current int64
		if e := tx.QueryRow(ctx, `SELECT status_version FROM users WHERE id=$1`, target).Scan(&current); e != nil {
			return e
		}
		if current != expected {
			return accounts.ErrConflict
		}
		tag, e := tx.Exec(ctx, `UPDATE model_credentials SET revoked_at=now() WHERE actor_id=$1 AND credential_kind='api_key' AND revoked_at IS NULL`, target)
		if e != nil {
			return e
		}
		count = tag.RowsAffected()
		return s.WriteAudit(ctx, tx, actor, "user.keys.revoke", target, reason, map[string]any{"count": count, "version": current})
	})
	return count, e
}
