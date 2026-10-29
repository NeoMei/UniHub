package billing

import (
	"testing"

	"unihub/internal/metering"
)

func TestModelRatesUnambiguousAllowsDifferentOperationsButRejectsDuplicates(t *testing.T) {
	generation := Rate{ID: "generation", Model: "image", Asset: CashAsset, Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Operation: "image_generation"}}
	edit := Rate{ID: "edit", Model: "image", Asset: CashAsset, Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Operation: "image_edit"}}
	duplicate := generation
	duplicate.ID = "generation-2"
	for _, tc := range []struct {
		name  string
		rates []Rate
		want  bool
	}{
		{"generation and edit", []Rate{generation, edit}, true},
		{"duplicate generation", []Rate{generation, edit, duplicate}, false},
		{"no tariff", nil, false},
		{"retired duplicate", []Rate{generation, {ID: "retired", Model: "image", Asset: CashAsset, Status: "retired", Semantics: "metered_v2", Metered: &metering.Price{Operation: "image_generation"}}}, true},
		{"missing operation", []Rate{{ID: "bad", Model: "image", Asset: CashAsset, Status: "published", Semantics: "metered_v2"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelRatesUnambiguous(tc.rates, "image", CashAsset); got != tc.want {
				t.Fatalf("ModelRatesUnambiguous = %v, want %v", got, tc.want)
			}
		})
	}
}
