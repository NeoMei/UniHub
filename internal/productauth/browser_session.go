package productauth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productusers"
)

// SourceState is the state of a product's native authenticator binding.
type SourceState string

const (
	SourceStateActive  SourceState = "active"
	SourceStateLocked  SourceState = "locked"
	SourceStateDeleted SourceState = "deleted"
)

// SourceLifecycle optionally changes native state while advancing the auth version.
type SourceLifecycle struct {
	Lifecycle
	State SourceState `json:"state,omitempty"`
}

// SyncSourceLifecycle records a scoped native transition. A replay returns the
// original version, and the product assignment is independent of native state.
func (s *Service) SyncSourceLifecycle(ctx context.Context, product, instance string, in SourceLifecycle) (LifecycleReceipt, error) {
	if in.State == "" {
		return s.SyncLifecycle(ctx, product, instance, in.Lifecycle)
	}
	switch in.State {
	case SourceStateActive, SourceStateLocked, SourceStateDeleted:
	default:
		return LifecycleReceipt{}, accounts.ErrInvalid
	}
	if !localSubject.MatchString(in.LocalUserID) || !operation.MatchString(in.OperationID) ||
		in.PreviousAuthVersion < 1 || in.NextAuthVersion <= in.PreviousAuthVersion ||
		in.NextAuthVersion != in.PreviousAuthVersion+1 || !s.users.Managed(product, instance) {
		return LifecycleReceipt{}, accounts.ErrInvalid
	}
	binding, err := s.users.RuntimeBinding(ctx, product, instance, in.LocalUserID)
	if err != nil {
		return LifecycleReceipt{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LifecycleReceipt{}, err
	}
	defer database.Rollback(tx)

	var current int64
	var deleted bool
	err = tx.QueryRow(ctx, `SELECT auth_version,deleted FROM product_auth_subjects WHERE binding_id=$1 FOR UPDATE`,
		binding.ID).Scan(&current, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleReceipt{}, accounts.ErrNotFound
	}
	if err != nil {
		return LifecycleReceipt{}, err
	}

	receipt := LifecycleReceipt{AuthVersion: in.NextAuthVersion}
	requestDigest := digest(in)
	var previousDigest string
	err = tx.QueryRow(ctx, `SELECT request_digest FROM product_auth_events WHERE binding_id=$1 AND operation_id=$2`,
		binding.ID, in.OperationID).Scan(&previousDigest)
	if err == nil {
		if previousDigest != requestDigest {
			return LifecycleReceipt{}, accounts.ErrConflict
		}
		return receipt, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return LifecycleReceipt{}, err
	}
	if current != in.PreviousAuthVersion {
		return LifecycleReceipt{}, accounts.ErrConflict
	}
	if deleted {
		return LifecycleReceipt{}, accounts.ErrForbidden
	}
	if _, err = tx.Exec(ctx, `UPDATE product_auth_subjects SET auth_version=$2,native_locked=$3,deleted=$4 WHERE binding_id=$1`,
		binding.ID, in.NextAuthVersion, in.State == SourceStateLocked, in.State == SourceStateDeleted); err != nil {
		return LifecycleReceipt{}, err
	}
	snapshot, _ := json.Marshal(in)
	if _, err = tx.Exec(ctx, `INSERT INTO product_auth_events(binding_id,operation_id,request_digest,actor_id,event,snapshot)
		VALUES($1,$2,$3,$4,'authenticator_source_lifecycle',$5)`,
		binding.ID, in.OperationID, requestDigest, binding.UserID, snapshot); err != nil {
		return LifecycleReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return LifecycleReceipt{}, err
	}
	return receipt, nil
}

// identityBrowserSourceProofDigest separates identity-session proofs from
// generic browser proofs while keeping the opaque token format.
func identityBrowserSourceProofDigest(raw string) string {
	return browserSessionDigest("identity-session:" + raw)
}

// VerifyProductBrowserSource validates an identity session against its central
// SID and the destination authority captured when the proof was issued.
func (s *Service) VerifyProductBrowserSource(ctx context.Context, raw, product, instance, expectedClientID, centralUserID string) error {
	const prefix = "ubsp_"
	if len(raw) != len(prefix)+64 || !strings.HasPrefix(raw, prefix) || strings.ToLower(raw) != raw ||
		expectedClientID == "" || centralUserID == "" {
		return accounts.ErrForbidden
	}
	if _, err := hex.DecodeString(raw[len(prefix):]); err != nil {
		return accounts.ErrForbidden
	}
	instance = productusers.Instance(instance)
	client, err := s.identityLinkClientForSession(expectedClientID, "", true)
	if err != nil || client.ClientID != expectedClientID || client.ProductID != product ||
		productusers.Instance(client.InstanceID) != instance {
		return accounts.ErrForbidden
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return accounts.ErrForbidden
	}
	defer database.Rollback(tx)

	if _, err = s.verifyIdentityBrowserSourceOn(ctx, tx, raw, product, instance, expectedClientID, centralUserID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return accounts.ErrForbidden
	}
	return nil
}

func (s *Service) verifyIdentityBrowserSourceOn(ctx context.Context, tx pgx.Tx, raw, product, instance, clientID, centralUserID string) (string, error) {
	var sourceProduct, sourceInstance, destinationLocalUser string
	err := tx.QueryRow(ctx, `SELECT COALESCE(b.product_id,''),COALESCE(b.instance_id,''),db.local_user_id
		FROM product_browser_source_proofs f
		JOIN browser_sessions s ON s.id_hash=f.source_session_hash
		JOIN identities i ON i.issuer=s.issuer AND i.subject=s.subject AND i.user_id=f.user_id
		JOIN users u ON u.id=i.user_id
		JOIN product_runtime_bindings db ON db.id=f.destination_binding_id
		  AND db.user_id=f.user_id AND db.product_id=f.product_id AND db.instance_id=f.instance_id
		JOIN product_auth_subjects dp ON dp.binding_id=db.id
		  AND dp.auth_version=f.destination_auth_version AND NOT dp.deleted AND NOT dp.native_locked
		JOIN product_user_access da ON da.user_id=db.user_id AND da.product_id=db.product_id AND da.instance_id=db.instance_id
		  AND da.version=f.destination_assignment_version AND da.active
		LEFT JOIN product_browser_session_origins o ON o.session_id_hash=s.id_hash
		LEFT JOIN product_auth_subjects p ON p.binding_id=o.binding_id
		LEFT JOIN product_runtime_bindings b ON b.id=p.binding_id
		LEFT JOIN product_user_access a ON a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id
		WHERE f.token_hash=$1 AND f.product_id=$2 AND f.instance_id=$3 AND f.client_id=$4 AND f.user_id=$5
		  AND f.expires_at>now() AND s.expires_at>now() AND u.active AND s.created_at>=u.session_not_before
		  AND u.status_version=f.destination_status_version
		  AND NOT EXISTS (SELECT 1 FROM identity_revocations r WHERE r.issuer=s.issuer AND r.subject=s.subject
		    AND (r.pending OR s.created_at<=r.revoked_after))
		  AND f.source_binding_id IS NOT DISTINCT FROM o.binding_id
		  AND f.source_auth_version IS NOT DISTINCT FROM o.auth_version
		  AND (o.binding_id IS NULL OR (
		    p.auth_version=o.auth_version AND NOT p.deleted AND NOT p.native_locked AND a.active AND b.user_id=f.user_id))`,
		identityBrowserSourceProofDigest(raw), product, instance, clientID, centralUserID).Scan(&sourceProduct, &sourceInstance, &destinationLocalUser)
	if err != nil {
		return "", accounts.ErrForbidden
	}
	user := accounts.User{ID: centralUserID}
	access, err := s.users.ResolveOn(ctx, tx, user, product, instance)
	if err != nil || !slices.Contains(access.Permissions, "product:use") {
		return "", accounts.ErrForbidden
	}
	if sourceProduct != "" && (sourceProduct != product || sourceInstance != instance) {
		access, err = s.users.ResolveOn(ctx, tx, user, sourceProduct, sourceInstance)
		if err != nil || !slices.Contains(access.Permissions, "product:use") {
			return "", accounts.ErrForbidden
		}
	}
	return destinationLocalUser, nil
}
