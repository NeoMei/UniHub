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
	batchByDestination := make(map[string]Batch, len(batches))
	for _, b := range batches {
		batchByDestination[BatchAddress(b.ID, "consumed")] = b
	}
	usedByOrder := make(map[string]int64)
	usedBatches := make(map[string][]Batch)
	for _, p := range creditPosts {
		if b, ok := batchByDestination[p.Destination]; ok {
			usedByOrder[b.OrderID] += p.Amount
			usedBatches[b.OrderID] = append(usedBatches[b.OrderID], b)
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
		if o.CreditScope == "" {
			// Adopted customer credits have already been paid under a legacy
			// order. The durable adoption authorizes their original lot to be
			// spent without a second cash posting.
			for _, b := range usedBatches[orderID] {
				if e = requireLegacyCreditAdoption(ctx, q, o, b); e != nil {
					return nil, e
				}
			}
			continue
		}
		compute, e := integer(o.ComputeAmount, true)
		if e != nil || compute > math.MaxInt64/productCreditsPerCNY || o.ProductID != call.OriginProductID || o.CreditScope != call.CreditScope || o.PaymentStatus != "PAID" || o.FulfillmentStatus != "FULFILLED" {
			return nil, ErrConflict
		}
		bs, e := list[Batch](ctx, q, `SELECT snapshot FROM billing_batches WHERE account_id=$1 AND order_id=$2`, call.AccountID, orderID)
		if e != nil {
			return nil, e
		}
		paidLimit := compute * productCreditsPerCNY
		limit := paidLimit
		if hasLegacyNetUpgrade(o.ProductCreditUpgrade) {
			nominal, e := integer(o.Offer.ComputeAmount, true)
			if e != nil || nominal < compute || nominal > math.MaxInt64/productCreditsPerCNY {
				return nil, ErrConflict
			}
			limit = nominal * productCreditsPerCNY
		}
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
		// Only a whole cash atom moves, up to the new order's paid compute.
		// The remaining grant is covered by the applied upgrade credit.
		before := min(previouslyUsed, paidLimit)
		after := min(previouslyUsed+used, paidLimit)
		delta := after/productCreditsPerCNY - before/productCreditsPerCNY
		if delta > 0 {
			posts = append(posts, ledger.Posting{Source: productComputePrepay(o), Destination: "commercial:compute_recognized:" + o.ProductID, Asset: CashAsset, Amount: delta})
		}
	}
	return posts, nil
}
