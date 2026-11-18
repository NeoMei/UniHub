package billing

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"strings"
	"time"

	"unihub/internal/accounts"
)

type ProductCreditWallet struct {
	ProductID             string     `json:"product_id"`
	CreditScope           string     `json:"credit_scope"`
	Available             string     `json:"available"`
	Reserved              string     `json:"reserved"`
	ActiveMembership      bool       `json:"active_membership"`
	MembershipPaidThrough *time.Time `json:"membership_paid_through,omitempty"`
	Batches               []Batch    `json:"batches"`
}

func productCreditBatchesOn(ctx context.Context, q dbq, account, product string) ([]Batch, error) {
	return list[Batch](ctx, q, `SELECT snapshot FROM billing_batches
		WHERE account_id=$1 AND snapshot->>'product_id'=$2 AND snapshot->>'credit_scope'='product_v1'
		ORDER BY CASE WHEN snapshot->>'kind'='addon' THEN 1 ELSE 0 END,
		(snapshot->>'expires_at')::timestamptz,(snapshot->>'created_at')::timestamptz,id`, account, product)
}

func activeSubscriptionOn(ctx context.Context, q accounts.Querier, account, product string, now time.Time) (Subscription, error) {
	sub, e := load[Subscription](ctx, q, `SELECT snapshot FROM billing_subscriptions WHERE account_id=$1 AND product_id=$2`, account, product)
	if errors.Is(e, accounts.ErrNotFound) {
		return sub, ErrSubscriptionRequired
	}
	if e != nil {
		return sub, e
	}
	if sub.Status != "active" || !now.Before(sub.PaidThrough) {
		return sub, ErrSubscriptionRequired
	}
	return sub, nil
}

func (s *Service) RequireProductCreditMembershipOn(ctx context.Context, q accounts.Querier, account, product string) error {
	_, e := activeSubscriptionOn(ctx, q, account, product, s.now().UTC())
	return e
}

func (s *Service) ProductCreditBatches(ctx context.Context, actor, account, product string) ([]Batch, error) {
	if e := s.member(ctx, s.pool, actor, account); e != nil {
		return nil, e
	}
	if !billingProductID.MatchString(product) {
		return nil, ErrInvalid
	}
	batches, e := productCreditBatchesOn(ctx, s.pool, account, product)
	if e != nil {
		return nil, e
	}
	for i := range batches {
		if e = s.quantities(ctx, &batches[i]); e != nil {
			return nil, e
		}
	}
	return batches, nil
}

func (s *Service) ProductCreditWallet(ctx context.Context, actor, account, product string) (ProductCreditWallet, error) {
	out := ProductCreditWallet{ProductID: product, CreditScope: ProductCreditScopeV1}
	var e error
	out.Batches, e = s.ProductCreditBatches(ctx, actor, account, product)
	if e != nil {
		return out, e
	}
	now := s.now().UTC()
	sub, e := activeSubscriptionOn(ctx, s.pool, account, product, now)
	if e != nil && !errors.Is(e, ErrSubscriptionRequired) {
		return out, e
	}
	out.ActiveMembership = e == nil
	if !sub.PaidThrough.IsZero() {
		out.MembershipPaidThrough = &sub.PaidThrough
	}
	var available, reserved big.Int
	for _, b := range out.Batches {
		n, _ := integer(b.Reserved, false)
		reserved.Add(&reserved, big.NewInt(n))
		if out.ActiveMembership && !b.Frozen && !now.Before(b.StartsAt) && now.Before(b.ExpiresAt) {
			n, _ = integer(b.Available, false)
			available.Add(&available, big.NewInt(n))
		}
	}
	out.Available, out.Reserved = available.String(), reserved.String()
	return out, nil
}

func batchFundsCall(b Batch, call Call) bool {
	if b.AccountID != call.AccountID || b.CreditScope != call.CreditScope {
		return false
	}
	return call.CreditScope == "" || call.CreditScope == ProductCreditScopeV1 && b.OrderID != "" && b.ProductID == call.OriginProductID && b.ProductID == call.FundingProductID
}

// A top-up can add a new subscription grant after the original hold took add-on
// credits. Settlement must still consume subscription credits first.
func settlementAllocations(ctx context.Context, q accounts.Querier, call Call) ([]Allocation, map[string]Batch, error) {
	batches := make(map[string]Batch, len(call.Allocations))
	for _, a := range call.Allocations {
		if _, loaded := batches[a.BatchID]; loaded {
			continue
		}
		b, e := load[Batch](ctx, q, `SELECT snapshot FROM billing_batches WHERE id=$1`, a.BatchID)
		if e != nil {
			return nil, nil, e
		}
		if !batchFundsCall(b, call) {
			return nil, nil, ErrConflict
		}
		batches[a.BatchID] = b
	}
	out := slices.Clone(call.Allocations)
	slices.SortStableFunc(out, func(a, b Allocation) int {
		left, right := batches[a.BatchID], batches[b.BatchID]
		if (left.Kind == "addon") != (right.Kind == "addon") {
			if left.Kind == "addon" {
				return 1
			}
			return -1
		}
		if n := left.ExpiresAt.Compare(right.ExpiresAt); n != 0 {
			return n
		}
		if n := left.CreatedAt.Compare(right.CreatedAt); n != 0 {
			return n
		}
		return strings.Compare(left.ID, right.ID)
	})
	return out, batches, nil
}
