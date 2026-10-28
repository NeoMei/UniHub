package platformadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

var ErrIdentityUnavailable = errors.New("identity capability unavailable")
var ErrIdentityUnknown = errors.New("identity outcome unknown; inspect original operation")
var ErrRecoveryUnsupported = errors.New("user has no verified recovery channel")
var ErrRecoveryInvalid = errors.New("invalid or consumed recovery credential")

type IdentityCapabilities struct {
	Activation         bool   `json:"activation"`
	Directory          bool   `json:"directory"`
	DisplayName        bool   `json:"display_name"`
	CreatePending      bool   `json:"create_pending"`
	Recovery           bool   `json:"recovery"`
	RevokeSessions     bool   `json:"revoke_sessions"`
	ProductionVerified bool   `json:"production_verified"`
	Issuer             string `json:"issuer"`
	Organization       string `json:"organization"`
	ProviderVersion    string `json:"provider_version"`
	Reason             string `json:"reason,omitempty"`
}
type ProviderIdentity struct {
	Issuer        string `json:"issuer"`
	Subject       string `json:"subject"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Disabled      bool   `json:"disabled"`
	Pending       bool   `json:"pending"`
	Manageable    bool   `json:"manageable"`
	UserID        string `json:"user_id,omitempty"`
	CreatedAt     string `json:"registered_at,omitempty"`
}
type CreateIdentityInput struct {
	OperationID string `json:"operation_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Reason      string `json:"reason"`
}
type MaterializeIdentityInput struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}
type DisplayNameInput struct {
	OperationID string `json:"operation_id"`
	DisplayName string `json:"display_name"`
	Reason      string `json:"reason"`
}
type RecoveryInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
}
type IdentityOperation struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Issuer    string          `json:"issuer"`
	Subject   string          `json:"subject,omitempty"`
	TargetID  string          `json:"target_id,omitempty"`
	State     string          `json:"state"`
	Result    json.RawMessage `json:"result"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// IdentityProvider must bind every operation to a verified issuer, organization,
// and immutable provider subject. Delivery receives the secret only for direct
// delivery to the identity's existing verified user-owned channel, never admin.
// Errors after dispatch must be treated as ambiguous by callers.
type IdentityProvider interface {
	Capabilities(context.Context) IdentityCapabilities
	Directory(context.Context, ListOptions) (Page[ProviderIdentity], error)
	ReadIdentity(context.Context, string, string) (ProviderIdentity, error)
	CreatePendingIdentity(context.Context, string, CreateIdentityInput) (ProviderIdentity, error)
	SetIdentityDisplayName(context.Context, string, string, string) error
	DeliverRecovery(context.Context, ProviderIdentity, string, string) error
	SetRecoveryPassword(context.Context, ProviderIdentity, string, string) error
	RevokeIdentitySessions(context.Context, ProviderIdentity, string) error
}

type ActivationIdentityProvider interface {
	DeliverActivation(context.Context, ProviderIdentity, string, string) error
	ActivateIdentity(context.Context, ProviderIdentity, string) error
}

func (s *Store) SetIdentityProvider(p IdentityProvider) { s.identityProvider = p }
func (s *Store) IdentityCapabilities(ctx context.Context, actor string) (IdentityCapabilities, error) {
	if e := s.Require(ctx, actor, "users:read"); e != nil {
		return IdentityCapabilities{}, e
	}
	if s.identityProvider == nil {
		return IdentityCapabilities{Reason: "identity provider administration not configured"}, nil
	}
	return s.identityProvider.Capabilities(ctx), nil
}
func (s *Store) identityProviderFor(ctx context.Context, capability string) (IdentityProvider, error) {
	p := s.identityProvider
	if p == nil {
		return nil, ErrIdentityUnavailable
	}
	c := p.Capabilities(ctx)
	ok := false
	switch capability {
	case "directory":
		ok = c.Directory
	case "display_name":
		ok = c.DisplayName
	case "create":
		ok = c.CreatePending
	case "recovery":
		ok = c.Recovery && c.RevokeSessions
	case "activation":
		ok = c.Activation && c.RevokeSessions
	}
	if !ok || c.Issuer == "" || c.Organization == "" {
		return nil, ErrIdentityUnavailable
	}
	return p, nil
}
func (s *Store) ListIdentities(ctx context.Context, actor string, o ListOptions) (Page[ProviderIdentity], error) {
	if e := s.Require(ctx, actor, "users:read"); e != nil {
		return Page[ProviderIdentity]{}, e
	}
	p, e := s.identityProviderFor(ctx, "directory")
	if e != nil {
		return Page[ProviderIdentity]{}, e
	}
	if o.Limit <= 0 || o.Limit > 100 {
		o.Limit = 50
	}
	out, e := p.Directory(ctx, o)
	if e != nil {
		return out, e
	}
	if len(out.Items) > o.Limit {
		return Page[ProviderIdentity]{}, ErrIdentityUnavailable
	}
	for i := range out.Items {
		v := &out.Items[i]
		if v.Issuer != p.Capabilities(ctx).Issuer || v.Subject == "" {
			return Page[ProviderIdentity]{}, ErrIdentityUnavailable
		}
		e = s.pool.QueryRow(ctx, `SELECT user_id FROM identities WHERE issuer=$1 AND subject=$2`, v.Issuer, v.Subject).Scan(&v.UserID)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return Page[ProviderIdentity]{}, e
		}
	}
	return out, nil
}
func (s *Store) MaterializeIdentity(ctx context.Context, actor string, in MaterializeIdentityInput) (accounts.User, error) {
	if !teamReason(in.Reason) || in.Issuer == "" || in.Subject == "" {
		return accounts.User{}, accounts.ErrInvalid
	}
	if e := s.Require(ctx, actor, "identity:manage"); e != nil {
		return accounts.User{}, e
	}
	p, e := s.identityProviderFor(ctx, "directory")
	if e != nil {
		return accounts.User{}, e
	}
	id, e := p.ReadIdentity(ctx, in.Issuer, in.Subject)
	if e != nil {
		return accounts.User{}, e
	}
	if !id.Manageable || id.Subject != in.Subject || id.Issuer != in.Issuer {
		return accounts.User{}, accounts.ErrForbidden
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return accounts.User{}, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0)),pg_advisory_xact_lock(hashtextextended('identity:' || length($1::text)::text || ':' || $1 || $2,0))`, id.Issuer, id.Subject); e != nil {
		return accounts.User{}, e
	}
	if e = s.requireOn(ctx, tx, actor, "identity:manage"); e != nil {
		return accounts.User{}, e
	}
	var u accounts.User
	e = tx.QueryRow(ctx, `SELECT u.id,u.name,u.email FROM identities i JOIN users u ON u.id=i.user_id WHERE i.issuer=$1 AND i.subject=$2`, id.Issuer, id.Subject).Scan(&u.ID, &u.Name, &u.Email)
	if e == nil {
		if e = s.requireTargetOn(ctx, tx, actor, u.ID, "identity:manage"); e != nil {
			return u, e
		}
		return u, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return u, e
	}
	u = accounts.User{ID: centralIdentityID(), Name: id.DisplayName, Email: id.Email}
	if _, e = tx.Exec(ctx, `INSERT INTO users(id,name,email,active) VALUES($1,$2,$3,$4)`, u.ID, u.Name, u.Email, !id.Disabled && !id.Pending); e != nil {
		return u, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3)`, id.Issuer, id.Subject, u.ID); e != nil {
		return u, e
	}
	aid := identityID()
	if _, e = tx.Exec(ctx, `INSERT INTO accounts(id,kind,name,personal_user_id) VALUES($1,'personal',$2,$3)`, aid, u.Name, u.ID); e != nil {
		return u, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'owner')`, aid, u.ID); e != nil {
		return u, e
	}
	if e = WriteAudit(ctx, tx, actor, "identity.materialize", u.ID, in.Reason, map[string]any{"issuer": id.Issuer, "subject": id.Subject, "active": !id.Disabled && !id.Pending}); e != nil {
		return u, e
	}
	return u, tx.Commit(ctx)
}
func identityHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Store) beginIdentityOperation(ctx context.Context, actor, id, kind, issuer, subject, target, reason string, payload any) (IdentityOperation, bool, error) {
	if len(id) < 8 || len(id) > 128 || !teamReason(reason) {
		return IdentityOperation{}, false, accounts.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return IdentityOperation{}, false, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`); e != nil {
		return IdentityOperation{}, false, e
	}
	if target != "" {
		e = s.requireTargetOn(ctx, tx, actor, target, "identity:manage")
	} else {
		e = s.requireOn(ctx, tx, actor, "identity:manage")
	}
	if e != nil {
		return IdentityOperation{}, false, e
	}
	hash := identityHash(payload)
	tag, e := tx.Exec(ctx, `INSERT INTO platform_identity_operations(id,actor_id,kind,issuer,subject,target_id,request_hash,state,reason) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,'pending',$8) ON CONFLICT(id) DO NOTHING`, id, actor, kind, issuer, subject, target, hash, reason)
	if e != nil {
		return IdentityOperation{}, false, e
	}
	var storedActor, storedHash string
	var op IdentityOperation
	e = tx.QueryRow(ctx, `SELECT id,actor_id,kind,issuer,subject,COALESCE(target_id,''),request_hash,state,result,created_at,updated_at FROM platform_identity_operations WHERE id=$1 FOR UPDATE`, id).Scan(&op.ID, &storedActor, &op.Kind, &op.Issuer, &op.Subject, &op.TargetID, &storedHash, &op.State, &op.Result, &op.CreatedAt, &op.UpdatedAt)
	if e != nil {
		return op, false, e
	}
	if storedActor != actor || storedHash != hash || op.Kind != kind {
		return op, false, accounts.ErrConflict
	}
	if tag.RowsAffected() == 1 {
		if e = WriteAudit(ctx, tx, actor, "identity."+kind+".intent", targetOrID(target, id), reason, map[string]string{"operation_id": id, "issuer": issuer, "subject": subject}); e != nil {
			return op, false, e
		}
	}
	return op, tag.RowsAffected() == 1, tx.Commit(ctx)
}
func targetOrID(target, id string) string {
	if target != "" {
		return target
	}
	return id
}
func (s *Store) finishIdentityOperation(ctx context.Context, op IdentityOperation, state string, result any) (IdentityOperation, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	b, e := json.Marshal(result)
	if e != nil {
		return op, e
	}
	tx, e := s.pool.Begin(cleanup)
	if e != nil {
		return op, e
	}
	defer database.Rollback(tx)
	var actor, reason string
	e = tx.QueryRow(cleanup, `UPDATE platform_identity_operations SET state=$2,result=$3,updated_at=clock_timestamp() WHERE id=$1 RETURNING state,result,updated_at,actor_id,reason,kind,issuer,subject,COALESCE(target_id,'')`, op.ID, state, b).Scan(&op.State, &op.Result, &op.UpdatedAt, &actor, &reason, &op.Kind, &op.Issuer, &op.Subject, &op.TargetID)
	if e != nil {
		return op, e
	}
	if e = WriteAudit(cleanup, tx, actor, "identity."+op.Kind+".outcome", targetOrID(op.TargetID, op.ID), reason, map[string]any{"operation_id": op.ID, "state": state, "result": result}); e != nil {
		return op, e
	}
	return op, tx.Commit(cleanup)
}
func (s *Store) IdentityOperation(ctx context.Context, actor, id string) (IdentityOperation, error) {
	if e := s.Require(ctx, actor, "users:read"); e != nil {
		return IdentityOperation{}, e
	}
	var op IdentityOperation
	e := s.pool.QueryRow(ctx, `SELECT id,kind,issuer,subject,COALESCE(target_id,''),state,result,created_at,updated_at FROM platform_identity_operations WHERE id=$1`, id).Scan(&op.ID, &op.Kind, &op.Issuer, &op.Subject, &op.TargetID, &op.State, &op.Result, &op.CreatedAt, &op.UpdatedAt)
	if e != nil {
		return op, identityDBError(e)
	}
	if op.TargetID != "" {
		if e = s.RequireTarget(ctx, actor, op.TargetID, "identity:manage"); e != nil {
			return op, e
		}
	} else if e = s.Require(ctx, actor, "identity:manage"); e != nil {
		return op, e
	}
	return op, nil
}
func (s *Store) CreateIdentity(ctx context.Context, actor string, in CreateIdentityInput) (IdentityOperation, error) {
	if strings.TrimSpace(in.Username) == "" || len(in.Username) > 80 || len(in.Email) > 254 || utf8.RuneCountInString(in.DisplayName) > 80 {
		return IdentityOperation{}, accounts.ErrInvalid
	}
	if e := s.Require(ctx, actor, "identity:manage"); e != nil {
		return IdentityOperation{}, e
	}
	p, e := s.identityProviderFor(ctx, "create")
	if e != nil {
		return IdentityOperation{}, e
	}
	op, fresh, e := s.beginIdentityOperation(ctx, actor, in.OperationID, "create", p.Capabilities(ctx).Issuer, "", "", in.Reason, in)
	if e != nil || !fresh {
		return op, e
	}
	id, e := p.CreatePendingIdentity(ctx, op.ID, in)
	if e != nil {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "provider_result_unknown"})
	}
	if !id.Manageable || !id.Pending || !id.Disabled || id.Subject == "" || id.Issuer != op.Issuer {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "pending_identity_readback_failed"})
	}
	if _, e = s.pool.Exec(ctx, `UPDATE platform_identity_operations SET subject=$2 WHERE id=$1`, op.ID, id.Subject); e != nil {
		return op, e
	}
	op.Subject = id.Subject
	u, e := s.MaterializeIdentity(ctx, actor, MaterializeIdentityInput{Issuer: id.Issuer, Subject: id.Subject, Reason: in.Reason})
	if e != nil {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "central_materialization_pending"})
	}
	return s.finishIdentityOperation(ctx, op, "succeeded", map[string]any{"user_id": u.ID, "activation": "user_verification_required", "pending": true})
}
func (s *Store) identityForUser(ctx context.Context, p IdentityProvider, target string) (ProviderIdentity, error) {
	var issuer, subject string
	e := s.pool.QueryRow(ctx, `SELECT issuer,subject FROM identities WHERE user_id=$1 AND issuer=$2`, target, p.Capabilities(ctx).Issuer).Scan(&issuer, &subject)
	if e != nil {
		return ProviderIdentity{}, identityDBError(e)
	}
	v, e := p.ReadIdentity(ctx, issuer, subject)
	if e != nil {
		return v, e
	}
	if !v.Manageable || v.Issuer != issuer || v.Subject != subject {
		return v, accounts.ErrForbidden
	}
	v.UserID = target
	return v, nil
}
func (s *Store) UpdateIdentityDisplayName(ctx context.Context, actor, target string, in DisplayNameInput) (IdentityOperation, error) {
	if !utf8.ValidString(in.DisplayName) || utf8.RuneCountInString(in.DisplayName) > 80 {
		return IdentityOperation{}, accounts.ErrInvalid
	}
	if e := s.RequireTarget(ctx, actor, target, "identity:manage"); e != nil {
		return IdentityOperation{}, e
	}
	p, e := s.identityProviderFor(ctx, "display_name")
	if e != nil {
		return IdentityOperation{}, e
	}
	id, e := s.identityForUser(ctx, p, target)
	if e != nil {
		return IdentityOperation{}, e
	}
	op, fresh, e := s.beginIdentityOperation(ctx, actor, in.OperationID, "display_name", id.Issuer, id.Subject, target, in.Reason, in)
	if e != nil || !fresh {
		return op, e
	}
	release, e := s.identityLease(ctx)
	if e != nil {
		return op, e
	}
	defer release()
	if e = s.RequireTarget(ctx, actor, target, "identity:manage"); e != nil {
		return s.finishIdentityOperation(ctx, op, "failed", map[string]string{"code": "authority_revoked"})
	}
	if e = p.SetIdentityDisplayName(ctx, id.Issuer, id.Subject, in.DisplayName); e != nil {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "provider_result_unknown"})
	}
	read, e := p.ReadIdentity(ctx, id.Issuer, id.Subject)
	if e != nil || read.Subject != id.Subject || read.DisplayName != in.DisplayName {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "readback_required"})
	}
	if _, e = s.pool.Exec(ctx, `UPDATE users SET name=$2 WHERE id=$1`, target, read.DisplayName); e != nil {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "central_readback_pending"})
	}
	return s.finishIdentityOperation(ctx, op, "succeeded", map[string]string{"display_name": read.DisplayName})
}
func (s *Store) RequestRecovery(ctx context.Context, actor, target string, in RecoveryInput) (IdentityOperation, error) {
	return s.requestIdentityCredential(ctx, actor, target, in, "recovery")
}
func (s *Store) RequestActivation(ctx context.Context, actor, target string, in RecoveryInput) (IdentityOperation, error) {
	return s.requestIdentityCredential(ctx, actor, target, in, "activation")
}
func (s *Store) requestIdentityCredential(ctx context.Context, actor, target string, in RecoveryInput, purpose string) (IdentityOperation, error) {
	if e := s.RequireTarget(ctx, actor, target, "identity:manage"); e != nil {
		return IdentityOperation{}, e
	}
	p, e := s.identityProviderFor(ctx, purpose)
	if e != nil {
		return IdentityOperation{}, e
	}
	id, e := s.identityForUser(ctx, p, target)
	if e != nil {
		return IdentityOperation{}, e
	}
	if purpose == "recovery" && (!id.EmailVerified || id.Email == "" || id.Disabled || id.Pending) {
		return IdentityOperation{}, ErrRecoveryUnsupported
	}
	if purpose == "activation" && (!id.Pending || !id.Disabled || id.Email == "" || id.EmailVerified) {
		return IdentityOperation{}, ErrRecoveryUnsupported
	}
	op, fresh, e := s.beginIdentityOperation(ctx, actor, in.OperationID, purpose, id.Issuer, id.Subject, target, in.Reason, in)
	if e != nil || !fresh {
		return op, e
	}
	release, e := s.identityLease(ctx)
	if e != nil {
		return op, e
	}
	defer release()
	if e = s.RequireTarget(ctx, actor, target, "identity:manage"); e != nil {
		return s.finishIdentityOperation(ctx, op, "failed", map[string]string{"code": "authority_revoked"})
	}
	secret := identityID() + identityID()
	hash := identityHash(secret)
	channel := identityHash(strings.ToLower(strings.TrimSpace(id.Email)))
	if _, e = s.pool.Exec(ctx, `INSERT INTO platform_identity_recovery(id,issuer,subject,user_id,purpose,token_hash,verified_channel_hash,state,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,'delivering',now()+interval '20 minutes')`, op.ID, id.Issuer, id.Subject, target, purpose, hash, channel); e != nil {
		return s.finishIdentityOperation(ctx, op, "failed", map[string]string{"code": "recovery_storage_failed"})
	}
	if purpose == "activation" {
		adapter, ok := p.(ActivationIdentityProvider)
		if !ok {
			e = ErrIdentityUnavailable
		} else {
			e = adapter.DeliverActivation(ctx, id, secret, op.ID)
		}
	} else {
		e = p.DeliverRecovery(ctx, id, secret, op.ID)
	}
	state := "delivered"
	if e != nil {
		state = "unknown"
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, e = s.pool.Exec(cleanup, `UPDATE platform_identity_recovery SET state=$2 WHERE id=$1 AND state='delivering'`, op.ID, state); e != nil {
		return op, e
	}
	resultState := "succeeded"
	if state == "unknown" {
		resultState = "unknown"
	}
	return s.finishIdentityOperation(ctx, op, resultState, map[string]string{"request_id": op.ID, "delivery_state": state, "purpose": purpose})
}

// RedeemRecovery reserves and consumes an exact hashed credential before the
// provider write. Password material is never stored or logged. Ambiguous writes
// remain reserved and cannot be repeated by retries, concurrency or restarts.
func (s *Store) RedeemRecovery(ctx context.Context, secret, password string, version int64) (IdentityOperation, error) {
	if len(secret) != 64 || len(password) < 12 || len(password) > 512 || version != 1 {
		return IdentityOperation{}, ErrRecoveryInvalid
	}
	p := s.identityProvider
	if p == nil {
		return IdentityOperation{}, ErrIdentityUnavailable
	}
	release, e := s.identityLease(ctx)
	if e != nil {
		return IdentityOperation{}, e
	}
	defer release()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return IdentityOperation{}, e
	}
	defer database.Rollback(tx)
	var op IdentityOperation
	var channel, purpose string
	var valid, active bool
	e = tx.QueryRow(ctx, `SELECT r.id,r.issuer,r.subject,r.user_id,r.verified_channel_hash,r.purpose,r.version=$2 AND r.expires_at>now() AND r.state IN ('delivered','unknown'),u.active FROM platform_identity_recovery r JOIN users u ON u.id=r.user_id WHERE r.token_hash=$1 FOR UPDATE OF r`, identityHash(secret), version).Scan(&op.ID, &op.Issuer, &op.Subject, &op.TargetID, &channel, &purpose, &valid, &active)
	if e != nil || !valid || (purpose == "recovery" && !active) || (purpose != "recovery" && purpose != "activation") {
		return op, ErrRecoveryInvalid
	}
	id, e := p.ReadIdentity(ctx, op.Issuer, op.Subject)
	if e != nil {
		return op, ErrIdentityUnavailable
	}
	if !id.Manageable || id.Subject != op.Subject || id.Issuer != op.Issuer || identityHash(strings.ToLower(strings.TrimSpace(id.Email))) != channel {
		return op, ErrRecoveryInvalid
	}
	if purpose == "recovery" && (!p.Capabilities(ctx).Recovery || id.Disabled || !id.EmailVerified || id.Pending) {
		return op, ErrRecoveryInvalid
	}
	if purpose == "activation" && (!p.Capabilities(ctx).Activation || !id.Disabled || !id.Pending || id.EmailVerified) {
		return op, ErrRecoveryInvalid
	}
	id.UserID = op.TargetID
	if _, e = tx.Exec(ctx, `UPDATE platform_identity_recovery SET state='redeeming',consumed_at=now(),version=version+1 WHERE id=$1`, op.ID); e != nil {
		return op, e
	}
	if _, e = tx.Exec(ctx, `UPDATE users SET session_not_before=clock_timestamp() WHERE id=$1`, op.TargetID); e != nil {
		return op, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM browser_sessions WHERE issuer=$1 AND subject=$2`, op.Issuer, op.Subject); e != nil {
		return op, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO identity_revocations(id,issuer,subject,revoked_after,pending) VALUES($1,$2,$3,clock_timestamp(),true)`, "recovery-"+op.ID, op.Issuer, op.Subject); e != nil {
		return op, e
	}
	if e = WriteAudit(ctx, tx, op.TargetID, "identity.recovery.redeem", op.TargetID, "user redeemed verified recovery credential", map[string]string{"request_id": op.ID}); e != nil {
		return op, e
	}
	if e = tx.Commit(ctx); e != nil {
		return op, e
	}
	if e = p.SetRecoveryPassword(ctx, id, password, op.ID); e != nil {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "password_result_unknown", "request_id": op.ID})
	}
	if purpose == "activation" {
		adapter, ok := p.(ActivationIdentityProvider)
		if !ok {
			return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "activation_adapter_unavailable"})
		}
		if e = adapter.ActivateIdentity(ctx, id, op.ID); e != nil {
			return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "activation_unknown"})
		}
		read, err := p.ReadIdentity(ctx, id.Issuer, id.Subject)
		if err != nil || read.Disabled || read.Pending || !read.EmailVerified {
			return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "activation_readback_unknown"})
		}
	}
	if e = p.RevokeIdentitySessions(ctx, id, op.ID); e != nil {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]string{"code": "upstream_revocation_unknown", "request_id": op.ID})
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, e = s.pool.Begin(cleanup)
	if e != nil {
		return op, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(cleanup, `UPDATE platform_identity_recovery SET state='consumed' WHERE id=$1;`, op.ID); e != nil {
		return op, e
	}
	if _, e = tx.Exec(cleanup, `UPDATE users SET session_not_before=clock_timestamp(),active=CASE WHEN $2 THEN true ELSE active END,status_version=status_version+CASE WHEN $2 THEN 1 ELSE 0 END WHERE id=$1`, op.TargetID, purpose == "activation"); e != nil {
		return op, e
	}
	if _, e = tx.Exec(cleanup, `UPDATE identity_revocations SET revoked_after=clock_timestamp(),pending=false WHERE id=$1`, "recovery-"+op.ID); e != nil {
		return op, e
	}
	if e = tx.Commit(cleanup); e != nil {
		return op, e
	}
	return s.finishIdentityOperation(ctx, op, "succeeded", map[string]string{"request_id": op.ID, "recovery": "completed", "sessions": "revoked"})
}
func (s *Store) RecoveryHandler(origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(recoveryHTML))
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		u, e := url.Parse(origin)
		if e != nil || u.Scheme == "" || u.Host == "" || r.Header.Get("Origin") != strings.TrimSuffix(origin, "/") {
			http.Error(w, "invalid origin", 403)
			return
		}
		ip, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		if splitErr != nil {
			ip = r.RemoteAddr
		}
		hash := identityHash(ip)
		var hits int
		e = s.pool.QueryRow(r.Context(), `INSERT INTO platform_recovery_rate_limits(key_hash,window_start,hits) VALUES($1,date_bin(interval '15 minutes',now(),timestamptz '2000-01-01'),1) ON CONFLICT(key_hash,window_start) DO UPDATE SET hits=platform_recovery_rate_limits.hits+1 RETURNING hits`, hash).Scan(&hits)
		if e != nil {
			http.Error(w, "recovery unavailable", 503)
			return
		}
		if hits > 10 {
			http.Error(w, "too many attempts", 429)
			return
		}
		var body struct {
			Token    string `json:"token"`
			Password string `json:"password"`
			Version  int64  `json:"version"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		op, e := s.RedeemRecovery(r.Context(), body.Token, body.Password, body.Version)
		if e != nil {
			http.Error(w, "recovery unavailable or credential invalid", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if op.State != "succeeded" {
			w.WriteHeader(202)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"request_id": op.ID, "state": op.State})
	})
}

const recoveryHTML = `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>UniHub 密码恢复</title><body><main><h1>设置新密码</h1><p>请使用发送到已验证邮箱的恢复链接。新密码至少 12 个字符。</p><form><label>新密码 <input name="password" type="password" minlength="12" maxlength="512" required autocomplete="new-password"></label><button>设置密码并退出旧会话</button></form><p id="result" role="status"></p></main><script>const token=new URLSearchParams(location.hash.slice(1)).get('token');history.replaceState(null,'',location.pathname);document.querySelector('form').onsubmit=async e=>{e.preventDefault();const b=document.querySelector('button');b.disabled=true;try{const r=await fetch(location.pathname,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token,password:new FormData(e.target).get('password'),version:1})});const v=await r.json();document.querySelector('#result').textContent=r.ok&&v.state==='succeeded'?'密码已更新，请重新登录。':'恢复未完成或结果待核对。请保留请求编号，不要重复操作。';}catch{document.querySelector('#result').textContent='结果待核对，请勿重复提交。';}}</script></body></html>`

// RecoveryURL keeps credentials out of access-log URLs and referrers.
func RecoveryURL(origin, secret string) string {
	return strings.TrimSuffix(origin, "/") + "/auth/recovery#token=" + url.QueryEscape(secret)
}

// identityLease serializes an in-flight external identity write against current
// platform grant/status changes without retrying a provider operation.
func (s *Store) identityLease(ctx context.Context) (func(), error) {
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('unihub:platform-administration',0))`); e != nil {
		conn.Release()
		return nil, e
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := conn.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended('unihub:platform-administration',0))`); e != nil {
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
	}, nil
}

func centralIdentityID() string {
	s := identityID()
	return s[:8] + "-" + s[8:12] + "-4" + s[13:16] + "-a" + s[17:20] + "-" + s[20:]
}

type SessionRevocationInput struct {
	OperationID     string `json:"operation_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

func (s *Store) RevokeIdentitySessions(ctx context.Context, actor, target string, in SessionRevocationInput) (IdentityOperation, error) {
	if e := s.RequireTarget(ctx, actor, target, "users:sessions"); e != nil {
		return IdentityOperation{}, e
	}
	issuer := ""
	if s.identityProvider != nil {
		issuer = s.identityProvider.Capabilities(ctx).Issuer
	}
	op, fresh, e := s.beginIdentityOperation(ctx, actor, in.OperationID, "sessions", issuer, "", target, in.Reason, in)
	if e != nil || !fresh {
		return op, e
	}
	release, e := s.identityLease(ctx)
	if e != nil {
		return op, e
	}
	defer release()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return op, e
	}
	defer database.Rollback(tx)
	if e = s.requireTargetOn(ctx, tx, actor, target, "users:sessions"); e != nil {
		return op, e
	}
	var ver int64
	if e = tx.QueryRow(ctx, `SELECT status_version FROM users WHERE id=$1 FOR UPDATE`, target).Scan(&ver); e != nil {
		return op, identityDBError(e)
	}
	if ver != in.ExpectedVersion {
		return s.finishIdentityOperation(ctx, op, "failed", map[string]string{"code": "version_conflict"})
	}
	local, e := revokeLocalOn(ctx, tx, target, false)
	if e != nil {
		return op, e
	}
	available := s.identityProvider != nil && s.identityProvider.Capabilities(ctx).RevokeSessions
	var id ProviderIdentity
	if available {
		id, e = s.identityForUser(ctx, s.identityProvider, target)
		if e != nil {
			available = false
		}
	}
	if available {
		op.Issuer = id.Issuer
		op.Subject = id.Subject
		if _, e = tx.Exec(ctx, `UPDATE platform_identity_operations SET issuer=$2,subject=$3 WHERE id=$1`, op.ID, id.Issuer, id.Subject); e != nil {
			return op, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO identity_revocations(id,issuer,subject,revoked_after,pending) VALUES($1,$2,$3,clock_timestamp(),true)`, "sessions-"+op.ID, id.Issuer, id.Subject); e != nil {
			return op, e
		}
	}
	if e = WriteAudit(ctx, tx, actor, "identity.sessions.revoke", target, in.Reason, map[string]any{"operation_id": op.ID, "local": local, "scope": "unihub_derived"}); e != nil {
		return op, e
	}
	if e = tx.Commit(ctx); e != nil {
		return op, e
	}
	if !available {
		return s.finishIdentityOperation(ctx, op, "unsupported", map[string]any{"local": local, "upstream_status": "unsupported", "completion": "partial"})
	}
	if e = s.identityProvider.RevokeIdentitySessions(ctx, id, op.ID); e != nil {
		return s.finishIdentityOperation(ctx, op, "unknown", map[string]any{"local": local, "upstream_status": "unknown", "completion": "partial"})
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, e = s.pool.Begin(cleanup)
	if e != nil {
		return op, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(cleanup, `UPDATE identity_revocations SET revoked_after=clock_timestamp(),pending=false WHERE id=$1`, "sessions-"+op.ID); e != nil {
		return op, e
	}
	if _, e = tx.Exec(cleanup, `UPDATE users SET session_not_before=GREATEST(session_not_before,date_trunc('second',clock_timestamp())+interval '1 second') WHERE id=$1`, target); e != nil {
		return op, e
	}
	if e = tx.Commit(cleanup); e != nil {
		return op, e
	}
	return s.finishIdentityOperation(ctx, op, "succeeded", map[string]any{"local": local, "upstream_status": "revoked", "completion": "complete", "scope": "unihub_derived"})
}
