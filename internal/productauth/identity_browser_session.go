package productauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productusers"
)

// ProductIdentitySession carries an existing local binding and the proof
// required to recheck the source browser session from the product backend.
type ProductIdentitySession struct {
	Identity             Identity `json:"identity"`
	SourceProofToken     string   `json:"source_proof_token"`
	SourceProofExpiresAt int64    `json:"source_proof_expires_at"`
}

// currentIdentityBrowserCodeDestination checks the pinned destination of code c.
const currentIdentityBrowserCodeDestination = `SELECT 1
	FROM product_runtime_bindings b
	JOIN product_auth_subjects p ON p.binding_id=b.id AND p.auth_version=c.destination_auth_version
	JOIN product_user_access a ON a.user_id=b.user_id AND a.product_id=b.product_id AND a.instance_id=b.instance_id
	JOIN users u ON u.id=b.user_id
	WHERE b.id=c.destination_binding_id AND b.user_id=c.user_id AND u.status_version=c.destination_status_version
	  AND a.version=c.destination_assignment_version AND a.active AND u.active AND NOT p.deleted AND NOT p.native_locked`

// MatchesIdentityBrowser compares the browser code to an authenticated
// local subject without redeeming the code or disclosing another subject.
func (s *Service) MatchesIdentityBrowser(ctx context.Context, product, instance, local string, in BrowserExchange) (bool, error) {
	instance = productusers.Instance(instance)
	client, err := s.identityLinkClient(in.ClientID, in.RedirectURI)
	if err != nil || in.RedirectURI == "" || client.ProductID != product ||
		productusers.Instance(client.InstanceID) != instance || !localSubject.MatchString(local) ||
		len(in.Verifier) < 43 || len(in.Verifier) > 128 || !validBrowserState(in.Nonce) {
		return false, accounts.ErrForbidden
	}
	hash := sha256.Sum256([]byte(in.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	var userID, sessionHash string
	err = s.pool.QueryRow(ctx, `SELECT c.user_id, c.source_session_hash
		FROM product_browser_codes c
		WHERE c.code_hash=$1 AND c.client_id=$2 AND c.pkce_challenge=$3 AND c.nonce_hash=$4
		  AND c.expires_at>now() AND NOT c.consumed AND c.source_session_hash IS NOT NULL
		  AND EXISTS (`+currentIdentityBrowserCodeDestination+`
		    AND b.product_id=$5 AND b.instance_id=$6 AND b.local_user_id=$7)`,
		digest(in.Code), in.ClientID, challenge, digest(in.Nonce), product, instance, local).Scan(&userID, &sessionHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = currentBrowserSourceSession(ctx, s.pool, sessionHash, userID); err != nil {
		return false, err
	}
	if !s.BrowserSessionAllowed(ctx, sessionHash) {
		return false, nil
	}
	identity, err := s.IdentityLinkByCanonicalUser(ctx, product, instance, userID)
	return err == nil && identity.Binding.LocalUserID == local, err
}

func (s *Service) ExchangeIdentityBrowser(ctx context.Context, product, instance string, in BrowserExchange) (ProductIdentitySession, error) {
	instance = productusers.Instance(instance)
	client, err := s.identityLinkClient(in.ClientID, in.RedirectURI)
	if err != nil || in.RedirectURI == "" || client.ClientID != in.ClientID ||
		client.ProductID != product || productusers.Instance(client.InstanceID) != instance {
		return ProductIdentitySession{}, accounts.ErrForbidden
	}
	grant, err := s.ExchangeBrowser(ctx, product, instance, in)
	if err != nil {
		return ProductIdentitySession{}, err
	}
	if grant.SourceProofToken == "" {
		return ProductIdentitySession{}, accounts.ErrForbidden
	}
	if err = s.ConsumeBrowserProof(ctx, grant.ProofToken); err != nil {
		return ProductIdentitySession{}, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ProductIdentitySession{}, err
	}
	defer database.Rollback(tx)

	userID, raw := grant.Access.User.ID, grant.SourceProofToken
	// Carry the authorization epoch forward. The verification below rejects
	// versions that changed while the code was being redeemed.
	result, err := tx.Exec(ctx, `UPDATE product_browser_source_proofs f SET
		token_hash=$2,destination_binding_id=c.destination_binding_id,destination_assignment_version=c.destination_assignment_version,
		destination_status_version=c.destination_status_version,destination_auth_version=c.destination_auth_version
		FROM product_browser_codes c
		WHERE f.token_hash=$1 AND f.user_id=$3 AND f.product_id=$4 AND f.instance_id=$5 AND f.client_id=$6
		  AND c.code_hash=$7 AND c.consumed AND c.client_id=f.client_id AND c.user_id=f.user_id
		  AND c.source_session_hash=f.source_session_hash AND c.destination_binding_id IS NOT NULL`,
		browserSessionDigest(raw), identityBrowserSourceProofDigest(raw), userID, product, instance, in.ClientID, digest(in.Code))
	if err != nil {
		return ProductIdentitySession{}, err
	}
	if result.RowsAffected() != 1 {
		return ProductIdentitySession{}, accounts.ErrForbidden
	}
	local, err := s.verifyIdentityBrowserSourceOn(ctx, tx, raw, product, instance, in.ClientID, userID)
	if err != nil {
		return ProductIdentitySession{}, err
	}
	binding, err := s.users.RuntimeBindingOn(ctx, tx, product, instance, local)
	if err != nil {
		return ProductIdentitySession{}, err
	}
	identity, err := s.lookupBindingOn(ctx, tx, binding)
	if err != nil {
		return ProductIdentitySession{}, err
	}
	if s.browserOverlay != nil {
		var issuedAt time.Time
		if err = tx.QueryRow(ctx, `SELECT expires_at-interval '120 seconds' FROM product_browser_codes WHERE code_hash=$1`, digest(in.Code)).Scan(&issuedAt); err != nil {
			return ProductIdentitySession{}, accounts.ErrForbidden
		}
		if err = s.acceptBrowserWork(ctx, tx, client.ClientID, issuedAt, RecordOutbound); err != nil {
			return ProductIdentitySession{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return ProductIdentitySession{}, err
	}
	return ProductIdentitySession{
		Identity: identity, SourceProofToken: grant.SourceProofToken, SourceProofExpiresAt: grant.SourceProofExpiresAt,
	}, nil
}
