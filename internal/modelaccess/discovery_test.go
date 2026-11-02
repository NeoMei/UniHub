package modelaccess

import (
	"strings"
	"testing"

	"unihub/internal/billing"
	"unihub/internal/metering"
)

func TestProjectCatalogFiltersScopeAndProjectsCapabilities(t *testing.T) {
	rates := []billing.Rate{
		{ID: "chat-rate", Model: "chat-model", Status: "published", Asset: billing.CreditAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "chat", Policy: "test", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}},
		{ID: "embed-rate", Model: "embed-model", Status: "published", Asset: billing.CreditAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "embedding", Policy: "test", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}},
		{ID: "embed-chat-rate", Model: "embed-model", Status: "published", Asset: billing.CreditAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "chat", Policy: "test", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}},
		{ID: "draft", Model: "draft-model", Status: "draft", Asset: billing.CreditAsset},
		{ID: "foreign", Model: "cash-model", Status: "published", Asset: billing.CashAsset},
	}
	out := ProjectCatalogForAsset([]string{"chat-model", "embed-model", "draft-model", "cash-model"}, rates, billing.CreditAsset)
	if len(out) != 2 || out[0].ID != "chat-model" || out[1].ID != "embed-model" {
		t.Fatalf("unexpected projection: %+v", out)
	}
	if !out[0].Probe || out[1].Probe != true || out[1].OutputModalities[0] != "embedding" || len(out[1].Operations) != 2 {
		t.Fatalf("unexpected capability metadata: %+v", out)
	}
}

func TestProjectCatalogEmptyAllowlistIsEmpty(t *testing.T) {
	out := ProjectCatalog([]string{}, []billing.Rate{{ID: "rate", Model: "model", Status: "published", Asset: billing.CreditAsset}})
	if len(out) != 0 {
		t.Fatalf("empty allowlist projected models: %+v", out)
	}
}

func TestDiscoveryTypesNeverContainBearerMaterial(t *testing.T) {
	secret := "uh_key_" + strings.Repeat("x", 64)
	for _, value := range []string{secret, "token", "bearer"} {
		for _, model := range ProjectCatalog([]string{"model"}, []billing.Rate{{ID: "rate", Model: "model", Status: "published", Asset: billing.CreditAsset}}) {
			if strings.Contains(model.ID, value) || strings.Contains(model.RateID, value) {
				t.Fatalf("secret leaked into model projection")
			}
		}
	}
}
