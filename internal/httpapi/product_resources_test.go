package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unihub/internal/billing"
)

func TestPublicCreditBatchesClassifyOnlyOwnSources(t *testing.T) {
	start := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	batches := []billing.Batch{
		{ID: "opaque-base", ProductID: "superwagie", Kind: "base", Original: "1000000000", Available: "900000000", Reserved: "50000000", StartsAt: start, ExpiresAt: start.AddDate(0, 1, 0)},
		{ID: "opaque-upgrade", ProductID: "superwagie", Kind: "upgrade"},
		{ID: "opaque-addon", ProductID: "superwagie", Kind: "addon"},
		{ID: "opaque-shared", ProductID: "private-product", Kind: "addon", OrderID: "private-order", SubscriptionID: "private-subscription", SourceKey: "private-source"},
	}
	// The projection is shared by the authenticated product credits endpoint.
	view := publicCreditBatches(batches, "superwagie")
	for i, kind := range []string{"subscription", "subscription", "addon", "other"} {
		if view[i]["kind"] != kind {
			t.Fatalf("batch %d classification=%v want=%s", i, view[i]["kind"], kind)
		}
	}
	if view[0]["original"] != "1000000000" || view[0]["available"] != "900000000" || view[0]["reserved"] != "50000000" || view[0]["expires_at"] != start.AddDate(0, 1, 0) {
		t.Fatal("projection changed real batch quantities or expiration")
	}
	for _, row := range view[:3] {
		if row["product_id"] != "superwagie" || row["source_scope"] != "product" {
			t.Fatalf("own batch lost product attribution: %+v", row)
		}
	}
	if _, exists := view[3]["product_id"]; exists || view[3]["source_scope"] != "shared" {
		t.Fatalf("shared batch misattributed: %+v", view[3])
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "order_id") || strings.Contains(string(raw), "subscription_id") || strings.Contains(string(raw), "source_key") {
		t.Fatalf("projection disclosed private source data: %s", raw)
	}
	for _, row := range publicCreditBatches(batches, "") {
		if row["kind"] != "other" || row["source_scope"] != "shared" {
			t.Fatalf("missing verified origin exposed source classification: %+v", row)
		}
		if _, exists := row["product_id"]; exists {
			t.Fatalf("missing verified origin exposed product: %+v", row)
		}
	}
}
