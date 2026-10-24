package billing

import (
	"context"
)

// OperatorOrders supplies the review queue, optionally limited to one account.
// Operator identity comes from deployment configuration, never request roles.
func (s *Service) OperatorOrders(ctx context.Context, operator, account string) ([]Order, error) {
	if e := s.operator(operator); e != nil {
		return nil, e
	}
	return list[Order](ctx, s.pool, `SELECT snapshot FROM billing_orders WHERE ($1='' OR account_id=$1) ORDER BY CASE WHEN snapshot->>'refund_status'='MANUAL_REVIEW' THEN 0 ELSE 1 END,(snapshot->>'created_at')::timestamptz DESC,id LIMIT 500`, account)
}
func (s *Service) IsOperator(actor string) bool { return s.operator(actor) == nil }
