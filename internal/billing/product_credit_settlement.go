package billing

import (
	"context"
	"maps"
	"math"
	"slices"

	"unihub/internal/ledger"
)

// Recognize the customer value from the grant's original prepaid order. The
// supplier amount is separate evidence on the settled call, and may be unknown.
func (s *Service) productComputeRecognition(ctx context.Context, q dbq, call Call, creditPosts []ledger.Posting) ([]ledger.Posting, error) {
	_, batches, e := settlementAllocations(ctx, q, call)
	if e != nil {
		return nil, e
	}
	orderByDestination := make(map[string]string, len(batches))
	for _, b := range batches {
		orderByDestination[BatchAddress(b.ID, "consumed")] = b.OrderID
	}
	usedByOrder := make(map[string]int64)
	for _, p := range creditPosts {
		if orderID := orderByDestination[p.Destination]; orderID != "" {
			usedByOrder[orderID] += p.Amount
		}
	}
	if len(usedByOrder) == 0 {
		return nil, ErrConflict
	}
	posts := make([]ledger.Posting, 0, len(usedByOrder))
	for _, orderID := range slices.Sorted(maps.Keys(usedByOrder)) {
		o, e := load[Order](ctx, q, `SELECT snapshot FROM billing_orders WHERE account_id=$1 AND id=$2`, call.AccountID, orderID)
		if e != nil {
			return nil, e
		}
		compute, e := integer(o.ComputeAmount, true)
		if e != nil || compute > math.MaxInt64/productCreditsPerCNY || o.ProductID != call.OriginProductID || o.CreditScope != call.CreditScope || o.PaymentStatus != "PAID" || o.FulfillmentStatus != "FULFILLED" {
			return nil, ErrConflict
		}
		bs, e := list[Batch](ctx, q, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND order_id=$2`, call.AccountID, orderID)
		if e != nil {
			return nil, e
		}
		limit := compute * productCreditsPerCNY
		var previouslyUsed int64
		for _, b := range bs {
			if !batchFundsCall(b, call) {
				return nil, ErrConflict
			}
			used, e := s.ledger.Balance(ctx, BatchAddress(b.ID, "consumed"), CreditAsset)
			if e != nil {
				return nil, e
			}
			if used < 0 || used > limit-previouslyUsed {
				return nil, ErrConflict
			}
			previouslyUsed += used
		}
		used := usedByOrder[orderID]
		if used > limit-previouslyUsed {
			return nil, ErrConflict
		}
		// Only a whole cash atom moves. The consumed ledger retains the
		// remainder across calls and months, including after command recovery.
		delta := (previouslyUsed+used)/productCreditsPerCNY - previouslyUsed/productCreditsPerCNY
		if delta > 0 {
			posts = append(posts, ledger.Posting{Source: productComputePrepay(o), Destination: "commercial:compute_recognized:" + o.ProductID, Asset: CashAsset, Amount: delta})
		}
	}
	return posts, nil
}
