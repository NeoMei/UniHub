package productauth

import (
	"context"

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
	// Promote the newly issued proof to the identity namespace and capture
	// the exact versions used by verification and lookup in this snapshot.
	result, err := tx.Exec(ctx, `UPDATE product_browser_source_proofs f SET
		token_hash=$2,destination_binding_id=b.id,destination_assignment_version=a.version,
		destination_status_version=u.status_version,destination_auth_version=p.auth_version
		FROM product_runtime_bindings b JOIN product_auth_subjects p ON p.binding_id=b.id
		JOIN product_user_access a USING(user_id,product_id,instance_id)
		JOIN users u ON u.id=b.user_id
		WHERE f.token_hash=$1 AND f.user_id=$3 AND f.product_id=$4 AND f.instance_id=$5 AND f.client_id=$6
		  AND b.user_id=f.user_id AND b.product_id=f.product_id AND b.instance_id=f.instance_id`,
		browserSessionDigest(raw), identityBrowserSourceProofDigest(raw), userID, product, instance, in.ClientID)
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
	if err = tx.Commit(ctx); err != nil {
		return ProductIdentitySession{}, err
	}
	return ProductIdentitySession{
		Identity: identity, SourceProofToken: grant.SourceProofToken, SourceProofExpiresAt: grant.SourceProofExpiresAt,
	}, nil
}
