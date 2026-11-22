package productauth

import (
	"context"

	"unihub/internal/accounts"
)

// ProductIdentitySession carries an existing local binding and the proof
// required to recheck the source browser session from the product backend.
type ProductIdentitySession struct {
	Identity             Identity `json:"identity"`
	SourceProofToken     string   `json:"source_proof_token"`
	SourceProofExpiresAt int64    `json:"source_proof_expires_at"`
}

func (s *Service) ExchangeIdentityBrowser(ctx context.Context, product, instance string, in BrowserExchange) (ProductIdentitySession, error) {
	if _, err := s.identityLinkClient(in.ClientID, in.RedirectURI); err != nil || in.RedirectURI == "" {
		return ProductIdentitySession{}, accounts.ErrForbidden
	}
	grant, err := s.ExchangeBrowser(ctx, product, instance, in)
	if err != nil {
		return ProductIdentitySession{}, err
	}
	if err = s.VerifyProductBrowserSource(ctx, grant.SourceProofToken, product, instance, in.ClientID, grant.Access.User.ID); err != nil {
		return ProductIdentitySession{}, err
	}
	identity, err := s.IdentityLinkByCanonicalUser(ctx, product, instance, grant.Access.User.ID)
	if err != nil {
		return ProductIdentitySession{}, err
	}
	if identity.Binding.UserID != grant.Access.User.ID {
		return ProductIdentitySession{}, accounts.ErrForbidden
	}
	if err = s.ConsumeBrowserProof(ctx, grant.ProofToken); err != nil {
		return ProductIdentitySession{}, err
	}
	return ProductIdentitySession{
		Identity: identity, SourceProofToken: grant.SourceProofToken, SourceProofExpiresAt: grant.SourceProofExpiresAt,
	}, nil
}
