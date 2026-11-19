package billing

import "context"

func (s *Service) PublishedOffer(ctx context.Context, id string) (Offer, error) {
	return load[Offer](ctx, s.pool, `SELECT snapshot FROM billing_offers WHERE id=$1 AND status='published'`, id)
}
