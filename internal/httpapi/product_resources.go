package httpapi

import (
	"net/http"
	"unihub/internal/billing"
)

func (s *api) authorizeOffer(r *http.Request, id string) error {
	if originProduct(r) == "" {
		return nil
	}
	product, e := s.options.Billing.OfferProduct(r.Context(), id)
	if e != nil {
		return e
	}
	return requireBillingProduct(r, product)
}

// Credits are one account-wide llmrouter pool. Return each real source batch's
// quantities and validity without unrelated product/order/subscription IDs.
func publicCreditBatches(in []billing.Batch) []map[string]any {
	out := []map[string]any{}
	for _, b := range in {
		out = append(out, map[string]any{"id": b.ID, "account_id": b.AccountID, "asset": billing.CreditAsset, "pool": "llmrouter", "original": b.Original, "available": b.Available, "reserved": b.Reserved, "consumed": b.Consumed, "expired": b.Expired, "withdrawn": b.Withdrawn, "starts_at": b.StartsAt, "expires_at": b.ExpiresAt, "created_at": b.CreatedAt, "frozen": b.Frozen})
	}
	return out
}
