package productauth

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productusers"
)

var nativeBrowserSessionID = regexp.MustCompile("^[0-9a-f]{64}$")

const maxNativeSessionLifetime = 7*24*time.Hour + time.Minute
const nativeHandoffLifetime = 2 * time.Minute

func validNativeSessionExpiry(unix int64, now time.Time) bool {
	return unix > 0 && unix <= now.Add(maxNativeSessionLifetime).Unix()
}

type NativeSessionRevocation struct {
	LocalUserID     string `json:"local_user_id"`
	NativeSessionID string `json:"native_session_id"`
	ExpiresAt       int64  `json:"expires_at"`
}

func lockNativeBrowserSession(ctx context.Context, tx pgx.Tx, binding, session string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"product-browser-native-session:"+binding+":"+session)
	return err
}

// RevokeNativeBrowserSession invalidates browser sessions issued from this
// native session. The browser SID's derived source proofs are deleted with it.
func (s *Service) RevokeNativeBrowserSession(ctx context.Context, product, instance string, in NativeSessionRevocation) error {
	if !localSubject.MatchString(in.LocalUserID) || !nativeBrowserSessionID.MatchString(in.NativeSessionID) ||
		!validNativeSessionExpiry(in.ExpiresAt, time.Now()) || !s.users.Managed(product, instance) {
		return accounts.ErrInvalid
	}

	var binding string
	err := s.pool.QueryRow(ctx, `SELECT b.id FROM product_runtime_bindings b
		JOIN product_auth_subjects p ON p.binding_id=b.id
		WHERE b.product_id=$1 AND b.instance_id=$2 AND b.local_user_id=$3`,
		product, productusers.Instance(instance), in.LocalUserID).Scan(&binding)
	if errors.Is(err, pgx.ErrNoRows) {
		return accounts.ErrNotFound
	}
	if err != nil {
		return err
	}

	tx, err := s.beginRuntimeMutation(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer database.Rollback(tx)
	if err = lockNativeBrowserSession(ctx, tx, binding, in.NativeSessionID); err != nil {
		return err
	}
	// A delayed, authenticated revocation can arrive after the JWT expires.
	// Keep the tombstone through the callback window in that case as well.
	expiry := time.Unix(in.ExpiresAt, 0)
	if _, err = tx.Exec(ctx, `INSERT INTO product_browser_native_session_revocations AS r
		(binding_id,native_session_id,expires_at) VALUES($1,$2,GREATEST($3,clock_timestamp()+$4::interval))
		ON CONFLICT(binding_id,native_session_id) DO UPDATE SET expires_at=GREATEST(r.expires_at,excluded.expires_at)`,
		binding, in.NativeSessionID, expiry, nativeHandoffLifetime); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `WITH removed AS (
			DELETE FROM browser_sessions s USING product_browser_session_origins o
			WHERE s.id_hash=o.session_id_hash AND o.binding_id=$1 AND o.native_session_id=$2
			RETURNING s.id_hash,s.expires_at
		), expired AS (
			SELECT session_id_hash FROM product_browser_native_logout_cookies
			WHERE expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 64 FOR UPDATE SKIP LOCKED
		), pruned AS (
			DELETE FROM product_browser_native_logout_cookies c USING expired e WHERE c.session_id_hash=e.session_id_hash
		), expired_revocations AS (
			SELECT binding_id,native_session_id FROM product_browser_native_session_revocations
			WHERE expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 64 FOR UPDATE SKIP LOCKED
		), pruned_revocations AS (
			DELETE FROM product_browser_native_session_revocations r USING expired_revocations e
			WHERE r.binding_id=e.binding_id AND r.native_session_id=e.native_session_id
		)
		INSERT INTO product_browser_native_logout_cookies(session_id_hash,expires_at)
		SELECT id_hash,expires_at FROM removed WHERE expires_at>clock_timestamp()`,
		binding, in.NativeSessionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Recheck the captured native epoch while holding the source rows through
// insertion. Selective sign-out takes the same session lock before deleting SIDs.
func (s *Service) lockBrowserHandoffSource(ctx context.Context, tx pgx.Tx, completion BrowserCompletion) error {
	if completion.NativeSessionID != "" {
		if err := lockNativeBrowserSession(ctx, tx, completion.BindingID, completion.NativeSessionID); err != nil {
			return err
		}
	}
	var product, instance string
	err := tx.QueryRow(ctx, `SELECT b.product_id,b.instance_id
		FROM users u JOIN product_runtime_bindings b ON b.user_id=u.id
		JOIN product_user_access a ON a.user_id=u.id AND a.product_id=b.product_id AND a.instance_id=b.instance_id
		JOIN product_auth_subjects p ON p.binding_id=b.id
		WHERE b.id=$1 AND p.auth_version=$2 AND u.id=$3 AND u.active AND a.active
		  AND NOT p.deleted AND NOT p.native_locked
		  AND ($4='' OR $5::timestamptz>clock_timestamp())
		  AND NOT EXISTS (SELECT 1 FROM product_browser_native_session_revocations r
		    WHERE r.binding_id=b.id AND r.native_session_id=$4 AND r.expires_at>clock_timestamp())
		FOR SHARE OF u,a,p`, completion.BindingID, completion.AuthVersion, completion.User.ID,
		completion.NativeSessionID, completion.NativeSessionExpiresAt).Scan(&product, &instance)
	if errors.Is(err, pgx.ErrNoRows) {
		return accounts.ErrForbidden
	}
	if err != nil {
		return err
	}
	if completion.NativeSessionID != "" {
		return s.users.RequirePermissionOn(ctx, tx, completion.User.ID, product, instance, "product:use")
	}
	return nil
}
