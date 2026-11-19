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
	offer, e := s.options.Billing.PublishedOffer(r.Context(), id)
	if e != nil {
		return e
	}
	if offer.CreditScope == billing.ProductCreditScopeV1 {
		if offer.ProductID != originProduct(r) {
			return accounts.ErrForbidden
		}
		return s.requireProductWallet(r)
	}
	if productWalletRequested(r) {
		return accounts.ErrForbidden
	}
	return requireBillingProduct(r, offer.ProductID)
}

// Credits are one account-wide llmrouter pool. Return each real source batch's
// quantities and validity. Only the calling product's batches expose their
// product and benefit category; other sources stay anonymous shared resources.
func publicCreditBatches(in []billing.Batch, origin string) []map[string]any {
	out := []map[string]any{}
	for _, b := range in {
		if b.CreditScope != "" {
			continue
		}
		row := map[string]any{"id": b.ID, "account_id": b.AccountID, "asset": billing.CreditAsset, "pool": "llmrouter", "original": b.Original, "available": b.Available, "reserved": b.Reserved, "consumed": b.Consumed, "expired": b.Expired, "withdrawn": b.Withdrawn, "starts_at": b.StartsAt, "expires_at": b.ExpiresAt, "created_at": b.CreatedAt, "frozen": b.Frozen, "kind": "other", "source_scope": "shared"}
		if origin != "" && b.ProductID == origin {
			row["product_id"] = origin
			row["source_scope"] = "product"
			switch b.Kind {
			case "base", "upgrade":
				row["kind"] = "subscription"
			case "addon":
				row["kind"] = "addon"
			}
		}
		out = append(out, row)
	}
	return out
}
