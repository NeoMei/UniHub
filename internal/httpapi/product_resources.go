package httpapi

import (
	"net/http"
	"unihub/internal/accounts"
	"unihub/internal/billing"
)

func (s *api) authorizeOffer(r *http.Request, id string) error {
	if originProduct(r) == "" {
		return nil
	}
	offers, e := s.options.Billing.Catalog(r.Context())
	if e != nil {
		return e
	}
	for _, o := range offers {
		if o.ID == id {
			return requireBillingProduct(r, o.ProductID)
		}
	}
	return accounts.ErrNotFound
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
