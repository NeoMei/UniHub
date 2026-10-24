package billing

import "context"

// GetQuote/GetOrder/GetSubscription provide read-only authorization checks before
// an HTTP product client invokes a command that may resume existing ledger work.
func (s *Service) GetQuote(ctx context.Context, actor, account, id string) (Quote, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Quote{}, e
	}
	return load[Quote](ctx, s.pool, `SELECT snapshot FROM billing_quotes WHERE account_id=$1 AND id=$2`, account, id)
}
func (s *Service) GetOrder(ctx context.Context, actor, account, id string) (Order, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return Order{}, e
	}
	return load[Order](ctx, s.pool, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND id=$2`, account, id)
}
func (s *Service) GetSubscription(ctx context.Context, actor, account, id string) (Subscription, error) {
	if e := s.member(ctx, s.pool, actor, account); e != nil {
		return Subscription{}, e
	}
	return load[Subscription](ctx, s.pool, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND id=$2`, account, id)
}

// ProductOrders filters before applying the legacy bounded-list size.
func (s *Service) ProductOrders(ctx context.Context, actor, account string, products []string) ([]Order, error) {
	if e := s.finance(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	return list[Order](ctx, s.pool, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND snapshot->>'product_id'=ANY($2) ORDER BY (snapshot->>'created_at')::timestamptz DESC,id LIMIT 200`, account, products)
}
