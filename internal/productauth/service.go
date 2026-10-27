// Package productauth connects mature product authenticators to central identity
// and product authority. Passwords, OTPs and passkey secrets stay in the product.
package productauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"regexp"
	"slices"
	"strings"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

type Service struct {
	pool           *pgxpool.Pool
	accounts       *accounts.Store
	users          *productusers.Store
	issuer         string
	browserClients map[string]products.Client
}

func New(pool *pgxpool.Pool, a *accounts.Store, u *productusers.Store, issuer string) *Service {
	return &Service{pool: pool, accounts: a, users: u, issuer: strings.TrimSuffix(issuer, "/")}
}

type Identity struct {
	Binding        productusers.RuntimeBinding `json:"binding"`
	Access         productusers.Access         `json:"access"`
	ProductRole    int                         `json:"product_role"`
	AuthVersion    int64                       `json:"auth_version"`
	ActorToken     string                      `json:"actor_token,omitempty"`
	ActorExpiresAt int64                       `json:"actor_expires_at,omitempty"`
}
type Registration struct {
	LocalUserID   string `json:"local_user_id"`
	OperationID   string `json:"operation_id"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	AuthVersion   int64  `json:"auth_version"`
}
type Authentication struct {
	LocalUserID      string `json:"local_user_id"`
	AuthenticationID string `json:"authentication_id"`
	Method           string `json:"method"`
	AuthenticatedAt  int64  `json:"authenticated_at"`
	AuthVersion      int64  `json:"auth_version"`
}
type Mutation struct {
	OperationID         string                     `json:"operation_id"`
	ExpectedVersion     int64                      `json:"expected_version"`
	Active              *bool                      `json:"active,omitempty"`
	ProductRole         *int                       `json:"product_role,omitempty"`
	PermissionOverrides map[string]map[string]bool `json:"permission_overrides,omitempty"`
	NextAuthVersion     *int64                     `json:"next_auth_version,omitempty"`
	Event               string                     `json:"event"`
}
type Import struct {
	CentralUserID string `json:"central_user_id,omitempty"`
	Registration
	ReviewedBatchID     string                     `json:"reviewed_batch_id"`
	SnapshotDigest      string                     `json:"snapshot_digest"`
	ProductRole         int                        `json:"product_role"`
	Active              bool                       `json:"active"`
	PermissionOverrides map[string]map[string]bool `json:"permission_overrides"`
}

var localSubject = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var operation = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{7,127}$`)
var oauthMethod = regexp.MustCompile(`^oauth:[a-zA-Z0-9_-]{1,64}$`)

func digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func randomToken() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return "upa_" + hex.EncodeToString(b[:])
}
func validateAuthentication(in Authentication, now time.Time) error {
	if !localSubject.MatchString(in.LocalUserID) || !operation.MatchString(in.AuthenticationID) || in.AuthVersion < 1 || in.AuthenticatedAt < now.Unix()-60 || in.AuthenticatedAt > now.Unix()+5 {
		return accounts.ErrInvalid
	}
	if !slices.Contains([]string{"password", "2fa", "passkey", "wechat", "telegram", "session"}, in.Method) && !oauthMethod.MatchString(in.Method) {
		return accounts.ErrInvalid
	}
	return nil
}
func (s *Service) Lookup(ctx context.Context, product, instance, local string) (out Identity, e error) {
	if !localSubject.MatchString(local) || !s.users.Managed(product, instance) {
		return out, accounts.ErrInvalid
	}
	access, e := s.users.ResolveRuntime(ctx, product, instance, local)
	if e != nil {
		if !errors.Is(e, accounts.ErrForbidden) {
			return out, e
		}
		binding, lookupError := s.users.RuntimeBinding(ctx, product, instance, local)
		if lookupError != nil {
			return out, lookupError
		}
		out.Binding = binding
		out.Access = productusers.Access{Issuer: s.issuer, Subject: binding.UserID, ProductID: product, InstanceID: productusers.Instance(instance), User: productusers.User{ID: binding.UserID}, Active: false, ExpiresAt: time.Now().Add(time.Minute).Unix()}
		var version, statusVersion int64
		var rawRoles []byte
		e = s.pool.QueryRow(ctx, `SELECT a.product_role,a.version,u.status_version,a.roles FROM product_user_access a JOIN users u ON u.id=a.user_id WHERE a.user_id=$1 AND a.product_id=$2 AND a.instance_id=$3`, binding.UserID, product, productusers.Instance(instance)).Scan(&out.ProductRole, &version, &statusVersion, &rawRoles)
		if e != nil {
			return out, e
		}
		for _, m := range s.users.Products() {
			if m.ProductID == product && m.InstanceID == productusers.Instance(instance) {
				out.Access.Version = m.Version + version + statusVersion - 1
				var roles []string
				if e = json.Unmarshal(rawRoles, &roles); e != nil {
					return out, e
				}
				out.ProductRole = productusers.EffectiveProductRole(m, roles, out.ProductRole)
			}
		}
		out.Access.ProductRole = out.ProductRole
		if e = s.pool.QueryRow(ctx, `SELECT auth_version FROM product_auth_subjects WHERE binding_id=$1`, binding.ID).Scan(&out.AuthVersion); e != nil {
			return out, e
		}
		return out, nil
	}
	out.Binding = access.Binding
	out.Access = access.Access
	out.ProductRole = access.Access.ProductRole
	var deleted bool
	e = s.pool.QueryRow(ctx, `SELECT auth_version,deleted FROM product_auth_subjects WHERE binding_id=$1`, out.Binding.ID).Scan(&out.AuthVersion, &deleted)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, accounts.ErrNotFound
	}
	if e == nil && deleted {
		return out, accounts.ErrForbidden
	}
	return
}
func (s *Service) Register(ctx context.Context, product, instance string, in Registration) (Identity, error) {
	if !localSubject.MatchString(in.LocalUserID) || !operation.MatchString(in.OperationID) || in.AuthVersion < 1 || len(in.Username) > 128 || len(in.DisplayName) > 256 || len(in.Email) > 320 || !s.users.Managed(product, instance) {
		return Identity{}, accounts.ErrInvalid
	}
	// Exact product subject, never an email or a caller-selected central subject.
	identity := accounts.Identity{Issuer: s.issuer + "/product-auth/" + product + "/" + productusers.Instance(instance), Subject: in.LocalUserID, Name: in.DisplayName, Email: in.Email}
	if identity.Name == "" {
		identity.Name = in.Username
	}
	existing, e := s.users.RuntimeBinding(ctx, product, instance, in.LocalUserID)
	if e == nil {
		var key, request string
		e = s.pool.QueryRow(ctx, `SELECT registration_key,registration_digest FROM product_auth_subjects WHERE binding_id=$1`, existing.ID).Scan(&key, &request)
		if e == nil {
			if key != in.OperationID || request != digest(in) {
				return Identity{}, accounts.ErrConflict
			}
			return s.Lookup(ctx, product, instance, in.LocalUserID)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return Identity{}, e
		}
		if existing.Issuer != identity.Issuer || existing.Subject != identity.Subject {
			return Identity{}, accounts.ErrConflict
		}
	}
	if e != nil && !errors.Is(e, accounts.ErrNotFound) && !errors.Is(e, pgx.ErrNoRows) {
		return Identity{}, e
	}
	u, e := s.accounts.EnsureIdentity(ctx, identity)
	if e != nil {
		return Identity{}, e
	}
	binding, e := s.users.EnsureRuntimeBinding(ctx, product, instance, in.LocalUserID, in.OperationID, identity, u)
	if e != nil {
		return Identity{}, e
	}
	m := productusers.Manifest{}
	for _, v := range s.users.Products() {
		if v.ProductID == product && v.InstanceID == productusers.Instance(instance) {
			m = v
		}
	}
	roles, _ := json.Marshal(m.DefaultRoles)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Identity{}, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `INSERT INTO product_auth_subjects(binding_id,auth_version,registration_key,registration_digest) VALUES($1,$2,$3,$4) ON CONFLICT(binding_id) DO NOTHING`, binding.ID, in.AuthVersion, in.OperationID, digest(in)); e != nil {
		return Identity{}, e
	}
	var key, request string
	if e = tx.QueryRow(ctx, `SELECT registration_key,registration_digest FROM product_auth_subjects WHERE binding_id=$1 FOR UPDATE`, binding.ID).Scan(&key, &request); e != nil {
		return Identity{}, e
	}
	if key != in.OperationID || request != digest(in) {
		return Identity{}, accounts.ErrConflict
	}
	if _, e = tx.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version,product_role) VALUES($1,$2,$3,$4,true,1,1) ON CONFLICT DO NOTHING`, u.ID, product, productusers.Instance(instance), roles); e != nil {
		return Identity{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Identity{}, e
	}
	return s.Lookup(ctx, product, instance, in.LocalUserID)
}
func (s *Service) Authenticate(ctx context.Context, product, instance string, in Authentication) (Identity, error) {
	if e := validateAuthentication(in, time.Now()); e != nil {
		return Identity{}, e
	}
	out, e := s.Lookup(ctx, product, instance, in.LocalUserID)
	if e != nil {
		return out, e
	}
	if !out.Access.Active || in.AuthVersion != out.AuthVersion {
		return Identity{}, accounts.ErrForbidden
	}
	token := randomToken()
	expires := time.Now().Add(120 * time.Second)
	// Unique authentication IDs prevent successful-event replay. A lost response
	// requires fresh local session authentication; the original secret is unrecoverable.
	_, e = s.pool.Exec(ctx, `INSERT INTO product_auth_assertions(token_hash,binding_id,authentication_id,authentication_digest,auth_version,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, digest(token), out.Binding.ID, in.AuthenticationID, digest(in), in.AuthVersion, expires)
	if e != nil {
		var found bool
		if q := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_auth_assertions WHERE binding_id=$1 AND authentication_id=$2)`, out.Binding.ID, in.AuthenticationID).Scan(&found); q == nil && found {
			return Identity{}, accounts.ErrConflict
		}
		return Identity{}, e
	}
	// Re-check after publication so a concurrent central disable cannot issue authority.
	fresh, checkError := s.Lookup(ctx, product, instance, in.LocalUserID)
	if checkError != nil {
		return Identity{}, checkError
	}
	if !fresh.Access.Active || fresh.AuthVersion != in.AuthVersion {
		return Identity{}, accounts.ErrForbidden
	}
	out = fresh
	out.ActorToken = token
	out.ActorExpiresAt = expires.Unix()
	return out, nil
}
func (s *Service) VerifyActor(ctx context.Context, token, product, instance string) (accounts.User, error) {
	if !strings.HasPrefix(token, "upa_") || len(token) != 68 {
		return accounts.User{}, accounts.ErrForbidden
	}
	var local string
	var version int64
	e := s.pool.QueryRow(ctx, `SELECT b.local_user_id,a.auth_version FROM product_auth_assertions a JOIN product_runtime_bindings b ON b.id=a.binding_id WHERE a.token_hash=$1 AND b.product_id=$2 AND b.instance_id=$3 AND a.expires_at>now()`, digest(token), product, productusers.Instance(instance)).Scan(&local, &version)
	if e != nil {
		return accounts.User{}, accounts.ErrForbidden
	}
	v, e := s.Lookup(ctx, product, instance, local)
	if e != nil || !v.Access.Active || v.AuthVersion != version {
		return accounts.User{}, accounts.ErrForbidden
	}
	return s.users.RuntimeUser(ctx, v.Binding.UserID)
}
func (s *Service) Mutate(ctx context.Context, product, instance, actor, local string, in Mutation) (Identity, error) {
	if !operation.MatchString(in.OperationID) || in.ExpectedVersion < 1 || !slices.Contains([]string{"authorization", "disable", "enable", "delete", "password", "mfa", "passkey", "oauth", "profile"}, in.Event) {
		return Identity{}, accounts.ErrInvalid
	}
	target, e := s.users.RuntimeBinding(ctx, product, instance, local)
	if e != nil {
		return Identity{}, e
	}
	actorUser, e := s.users.RuntimeUser(ctx, actor)
	if e != nil {
		return Identity{}, e
	}
	actorAccess, e := s.users.Resolve(ctx, actorUser, product, instance)
	if e != nil {
		return Identity{}, e
	}
	// Identity administrators act only inside this product. A financial/account
	// administrator does not gain product user-management authority.
	self := actor == target.UserID
	authChange := in.ProductRole != nil || in.PermissionOverrides != nil || in.Active != nil || in.Event == "delete"
	if authChange && !(self && in.Event == "delete" && actorAccess.ProductRole < 100) && (!slices.Contains(actorAccess.Permissions, "user:write") || actorAccess.ProductRole < 10) {
		return Identity{}, accounts.ErrForbidden
	}
	if !self && !authChange && !slices.Contains(actorAccess.Permissions, "user:write") {
		return Identity{}, accounts.ErrForbidden
	}
	if in.ProductRole != nil && (*in.ProductRole != 1 && *in.ProductRole != 10 && *in.ProductRole != 100) {
		return Identity{}, accounts.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Identity{}, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "product-auth:"+product+"/"+instance); e != nil {
		return Identity{}, e
	}
	// Lock actor and target rows before comparing authority or updating it.
	rows, e := tx.Query(ctx, `SELECT user_id FROM product_user_access WHERE product_id=$1 AND instance_id=$2 AND user_id IN($3,$4) ORDER BY user_id FOR UPDATE`, product, productusers.Instance(instance), actor, target.UserID)
	if e != nil {
		return Identity{}, e
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return Identity{}, e
	}
	actorAccess, e = s.users.ResolveOn(ctx, tx, actorUser, product, instance)
	if e != nil {
		return Identity{}, e
	}
	if authChange && !(self && in.Event == "delete" && actorAccess.ProductRole < 100) && (!slices.Contains(actorAccess.Permissions, "user:write") || actorAccess.ProductRole < 10) {
		return Identity{}, accounts.ErrForbidden
	}
	if !self && !authChange && !slices.Contains(actorAccess.Permissions, "user:write") {
		return Identity{}, accounts.ErrForbidden
	}
	var oldDigest string
	e = tx.QueryRow(ctx, `SELECT request_digest FROM product_auth_events WHERE binding_id=$1 AND operation_id=$2`, target.ID, in.OperationID).Scan(&oldDigest)
	if e == nil {
		if oldDigest != digest(in) {
			return Identity{}, accounts.ErrConflict
		}
		if e = tx.Commit(ctx); e != nil {
			return Identity{}, e
		}
		return s.Lookup(ctx, product, instance, local)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Identity{}, e
	}
	var currentVersion, globalVersion, authVersion int64
	var currentRole int
	var currentRoles []byte
	var active, deleted bool
	var overrides json.RawMessage
	e = tx.QueryRow(ctx, `SELECT a.version,u.status_version,a.product_role,a.active,a.permission_overrides,p.auth_version,p.deleted,a.roles FROM product_user_access a JOIN users u ON u.id=a.user_id JOIN product_auth_subjects p ON p.binding_id=$4 WHERE a.user_id=$1 AND a.product_id=$2 AND a.instance_id=$3 FOR UPDATE OF a,p`, target.UserID, product, productusers.Instance(instance), target.ID).Scan(&currentVersion, &globalVersion, &currentRole, &active, &overrides, &authVersion, &deleted, &currentRoles)
	if e != nil {
		return Identity{}, e
	}
	manifestVersion := int64(0)
	var manifest productusers.Manifest
	for _, m := range s.users.Products() {
		if m.ProductID == product && m.InstanceID == productusers.Instance(instance) {
			manifestVersion = m.Version
			manifest = m
		}
	}
	if manifestVersion+currentVersion+globalVersion-1 != in.ExpectedVersion {
		return Identity{}, accounts.ErrConflict
	}
	if deleted {
		return Identity{}, accounts.ErrForbidden
	}
	var assigned []string
	if e = json.Unmarshal(currentRoles, &assigned); e != nil {
		return Identity{}, e
	}
	targetEffectiveRole := productusers.EffectiveProductRole(manifest, assigned, currentRole)
	if (!self && targetEffectiveRole >= actorAccess.ProductRole && actorAccess.ProductRole < 100) || (authChange && self && in.Event != "delete") {
		return Identity{}, accounts.ErrForbidden
	}
	var nextRoles []string
	if in.ProductRole != nil {
		nextRoles = append([]string{}, manifest.DefaultRoles...)
		for _, role := range manifest.Roles {
			if role.NativeBaseline && role.ProductRole == *in.ProductRole {
				nextRoles = append(nextRoles, role.ID)
			}
		}
		overrides = json.RawMessage(`{}`)
	}
	if in.ProductRole != nil {
		if *in.ProductRole < currentRole {
			overrides = json.RawMessage(`{}`)
		}
		if *in.ProductRole >= actorAccess.ProductRole && actorAccess.ProductRole < 100 {
			return Identity{}, accounts.ErrForbidden
		}
		currentRole = *in.ProductRole
	}
	if in.PermissionOverrides != nil {
		known := map[string]bool{}
		for _, r := range manifest.Roles {
			for _, p := range r.Permissions {
				known[p] = true
			}
		}
		for resource, actions := range in.PermissionOverrides {
			for action, allow := range actions {
				p := resource + ":" + action
				if !known[p] || allow && !slices.Contains(actorAccess.Permissions, p) {
					return Identity{}, accounts.ErrForbidden
				}
			}
		}
		overrides, _ = json.Marshal(in.PermissionOverrides)
	}
	if nextRoles != nil {
		var finalOverrides map[string]map[string]bool
		if e = json.Unmarshal(overrides, &finalOverrides); e != nil {
			return Identity{}, e
		}
		grant, err := productusers.EffectivePermissions(manifest, nextRoles, finalOverrides, currentRole)
		if err != nil {
			return Identity{}, err
		}
		for _, permission := range grant {
			if !slices.Contains(actorAccess.Permissions, permission) {
				return Identity{}, accounts.ErrForbidden
			}
		}
	}
	if in.Active != nil {
		active = *in.Active
	}
	if in.Event == "delete" {
		active = false
		deleted = true
	}
	if in.NextAuthVersion != nil {
		if *in.NextAuthVersion != authVersion+1 {
			return Identity{}, accounts.ErrConflict
		}
		authVersion = *in.NextAuthVersion
	}
	if authChange && !(self && in.Event == "delete" && actorAccess.ProductRole < 100) {
		if e = s.users.RequirePermissionOn(ctx, tx, actor, product, instance, "user:write"); e != nil {
			return Identity{}, e
		}
	}
	if nextRoles != nil {
		raw, _ := json.Marshal(nextRoles)
		if _, e = tx.Exec(ctx, `UPDATE product_user_access SET roles=$4 WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, target.UserID, product, productusers.Instance(instance), raw); e != nil {
			return Identity{}, e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE product_user_access SET product_role=$4,active=$5,permission_overrides=$6,version=version+1,updated_at=now() WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, target.UserID, product, productusers.Instance(instance), currentRole, active, overrides); e != nil {
		return Identity{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE product_auth_subjects SET auth_version=$2,deleted=$3 WHERE binding_id=$1`, target.ID, authVersion, deleted); e != nil {
		return Identity{}, e
	}
	snapshot, _ := json.Marshal(map[string]any{"role": currentRole, "active": active, "auth_version": authVersion, "deleted": deleted})
	if _, e = tx.Exec(ctx, `INSERT INTO product_auth_events(binding_id,operation_id,request_digest,actor_id,event,snapshot) VALUES($1,$2,$3,$4,$5,$6)`, target.ID, in.OperationID, digest(in), actor, in.Event, snapshot); e != nil {
		return Identity{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Identity{}, e
	}
	if !active {
		return s.Lookup(ctx, product, instance, local)
	}
	return s.Lookup(ctx, product, instance, local)
}
func (s *Service) Import(ctx context.Context, product, instance string, in Import) (Identity, error) {
	if !operation.MatchString(in.ReviewedBatchID) || len(in.SnapshotDigest) != 64 || (in.ProductRole != 1 && in.ProductRole != 10 && in.ProductRole != 100) {
		return Identity{}, accounts.ErrInvalid
	}
	if _, e := hex.DecodeString(in.SnapshotDigest); e != nil {
		return Identity{}, accounts.ErrInvalid
	}
	if in.CentralUserID != "" {
		if _, e := s.users.RuntimeUser(ctx, in.CentralUserID); e != nil {
			return Identity{}, e
		}
		issuer := s.issuer + "/product-auth/" + product + "/" + productusers.Instance(instance)
		tx, e := s.pool.Begin(ctx)
		if e != nil {
			return Identity{}, e
		}
		defer database.Rollback(tx)
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "product-auth-import:"+issuer+"/"+in.LocalUserID); e != nil {
			return Identity{}, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, issuer, in.LocalUserID, in.CentralUserID); e != nil {
			return Identity{}, e
		}
		var matched string
		if e = tx.QueryRow(ctx, `SELECT user_id FROM identities WHERE issuer=$1 AND subject=$2`, issuer, in.LocalUserID).Scan(&matched); e != nil {
			return Identity{}, e
		}
		if matched != in.CentralUserID {
			return Identity{}, accounts.ErrConflict
		}
		if e = tx.Commit(ctx); e != nil {
			return Identity{}, e
		}
	}
	out, e := s.Register(ctx, product, instance, in.Registration)
	if e != nil {
		return Identity{}, e
	}
	known := map[string]bool{}
	for _, m := range s.users.Products() {
		if m.ProductID == product && m.InstanceID == productusers.Instance(instance) {
			for _, r := range m.Roles {
				for _, p := range r.Permissions {
					known[p] = true
				}
			}
		}
	}
	for resource, actions := range in.PermissionOverrides {
		for action := range actions {
			if !known[resource+":"+action] {
				return Identity{}, accounts.ErrInvalid
			}
		}
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Identity{}, e
	}
	defer database.Rollback(tx)
	var batch, snapshot string
	if e = tx.QueryRow(ctx, `SELECT imported_batch,snapshot_digest FROM product_auth_subjects WHERE binding_id=$1 FOR UPDATE`, out.Binding.ID).Scan(&batch, &snapshot); e != nil {
		return Identity{}, e
	}
	if batch != "" {
		if batch != in.ReviewedBatchID || snapshot != digest(in) {
			return Identity{}, accounts.ErrConflict
		}
		if e = tx.Commit(ctx); e != nil {
			return Identity{}, e
		}
		return s.Lookup(ctx, product, instance, in.LocalUserID)
	}
	overrides, _ := json.Marshal(in.PermissionOverrides)
	if string(overrides) == "null" {
		overrides = []byte("{}")
	}
	if _, e = tx.Exec(ctx, `UPDATE product_user_access SET product_role=$4,active=$5,permission_overrides=$6,version=version+1 WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, out.Binding.UserID, product, productusers.Instance(instance), in.ProductRole, in.Active, overrides); e != nil {
		return Identity{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE product_auth_subjects SET imported_batch=$2,snapshot_digest=$3 WHERE binding_id=$1`, out.Binding.ID, in.ReviewedBatchID, digest(in)); e != nil {
		return Identity{}, e
	}
	event, _ := json.Marshal(in)
	if _, e = tx.Exec(ctx, `INSERT INTO product_auth_events(binding_id,operation_id,request_digest,actor_id,event,snapshot) VALUES($1,$2,$3,$4,'import',$5)`, out.Binding.ID, in.ReviewedBatchID, digest(in), out.Binding.UserID, event); e != nil {
		return Identity{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Identity{}, e
	}
	if !in.Active {
		return s.Lookup(ctx, product, instance, in.LocalUserID)
	}
	return s.Lookup(ctx, product, instance, in.LocalUserID)
}

// AdoptBinding records the authentication version for an already proven OIDC
// binding. It never creates a user, changes a role or replaces another binding.
func (s *Service) AdoptBinding(ctx context.Context, product, instance, local string, version int64) error {
	if version < 1 {
		return accounts.ErrInvalid
	}
	binding, e := s.users.RuntimeBinding(ctx, product, instance, local)
	if e != nil {
		return e
	}
	_, e = s.pool.Exec(ctx, `INSERT INTO product_auth_subjects(binding_id,auth_version,registration_key,registration_digest) VALUES($1,$2,$3,$4) ON CONFLICT(binding_id) DO NOTHING`, binding.ID, version, "oidc:"+binding.ID, digest(binding))
	return e
}

type Lifecycle struct {
	LocalUserID         string `json:"local_user_id"`
	OperationID         string `json:"operation_id"`
	PreviousAuthVersion int64  `json:"previous_auth_version"`
	NextAuthVersion     int64  `json:"next_auth_version"`
}
type LifecycleReceipt struct {
	AuthVersion int64 `json:"auth_version"`
}

// SyncLifecycle is a restricted product-authenticator operation. It can only
// revoke old authentication evidence; it never changes account state or grants.
func (s *Service) SyncLifecycle(ctx context.Context, product, instance string, in Lifecycle) (LifecycleReceipt, error) {
	if !localSubject.MatchString(in.LocalUserID) || !operation.MatchString(in.OperationID) || in.PreviousAuthVersion < 1 || in.NextAuthVersion != in.PreviousAuthVersion+1 {
		return LifecycleReceipt{}, accounts.ErrInvalid
	}
	b, e := s.users.RuntimeBinding(ctx, product, instance, in.LocalUserID)
	if e != nil {
		return LifecycleReceipt{}, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return LifecycleReceipt{}, e
	}
	defer database.Rollback(tx)
	var current int64
	if e = tx.QueryRow(ctx, `SELECT auth_version FROM product_auth_subjects WHERE binding_id=$1 FOR UPDATE`, b.ID).Scan(&current); e != nil {
		return LifecycleReceipt{}, e
	}
	var previous string
	e = tx.QueryRow(ctx, `SELECT request_digest FROM product_auth_events WHERE binding_id=$1 AND operation_id=$2`, b.ID, in.OperationID).Scan(&previous)
	if e == nil {
		if previous != digest(in) {
			return LifecycleReceipt{}, accounts.ErrConflict
		}
		return LifecycleReceipt{in.NextAuthVersion}, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return LifecycleReceipt{}, e
	}
	if current != in.PreviousAuthVersion {
		return LifecycleReceipt{}, accounts.ErrConflict
	}
	if _, e = tx.Exec(ctx, `UPDATE product_auth_subjects SET auth_version=$2 WHERE binding_id=$1`, b.ID, in.NextAuthVersion); e != nil {
		return LifecycleReceipt{}, e
	}
	snapshot, _ := json.Marshal(in)
	if _, e = tx.Exec(ctx, `INSERT INTO product_auth_events(binding_id,operation_id,request_digest,actor_id,event,snapshot) VALUES($1,$2,$3,$4,'authenticator_version',$5)`, b.ID, in.OperationID, digest(in), b.UserID, snapshot); e != nil {
		return LifecycleReceipt{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return LifecycleReceipt{}, e
	}
	return LifecycleReceipt{in.NextAuthVersion}, nil
}
