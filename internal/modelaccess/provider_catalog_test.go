package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderCatalogFailsClosedOnWrongOwnerAndMalformedResponse(t *testing.T) {
	for _, test := range []string{"owner", "empty", "duplicate", "large", "redirect"} {
		t.Run(test, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var scope ProviderCatalogRequest
				_ = json.NewDecoder(r.Body).Decode(&scope)
				out := ProviderCatalogResponse{ProviderCatalogRequest: scope, SchemaVersion: 1, CatalogVersion: "1", ModelScope: ScopeLLMRouterAvailable, PricingSource: PricingLLMRouterNative, Models: []Model{{ID: "m"}}}
				switch test {
				case "owner":
					out.AccountID = "wrong"
				case "empty":
					out.Models = nil
				case "duplicate":
					out.Models = append(out.Models, out.Models[0])
				case "large":
					_, _ = w.Write([]byte(strings.Repeat("x", (2<<20)+1)))
					return
				case "redirect":
					http.Redirect(w, r, "https://other.test", http.StatusFound)
					return
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			client, e := NewProviderCatalog(server.URL+"/internal/v1/provider-catalog", strings.Repeat("t", 32))
			if e != nil {
				t.Fatal(e)
			}
			_, e = client.Discover(context.Background(), ProviderCatalogRequest{OriginProductID: "superwagie", InstanceID: "default", ActorID: "actor", AccountID: "account"})
			if !errors.Is(e, ErrProviderCatalog) {
				t.Fatalf("unsafe catalog accepted %v", e)
			}
		})
	}
}

func TestScopedProviderCatalogRejectsLegacyPricingAndPreservesTariffRevision(t *testing.T) {
	for _, pricingSource := range []string{PricingLLMRouterNative, PricingLLMRouterNativeCredit} {
		t.Run(pricingSource, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ProviderCatalogRequest
				if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
					t.Error(e)
					return
				}
				_ = json.NewEncoder(w).Encode(ProviderCatalogResponse{
					ProviderCatalogRequest: req, SchemaVersion: 1, CatalogVersion: "model-tariff-revision", ModelScope: ScopeLLMRouterAvailable, PricingSource: pricingSource,
					NativeCreditProtocol: "lr-native-credit-v1", CreditScopes: []string{"product_v1"},
					Models: []Model{{ID: "m", CapabilityMetadata: json.RawMessage(`{"operations":[{"public_tariff":{"asset":"CREDIT/6","revision":"current-revision"}}]}`)}},
				})
			}))
			defer server.Close()
			client, e := NewProviderCatalog(server.URL+"/internal/v1/provider-catalog", strings.Repeat("t", 32))
			if e != nil {
				t.Fatal(e)
			}
			wantScope := ProviderCatalogRequest{CreditScope: "product_v1", OriginProductID: "agentwiki", InstanceID: "default", ActorID: "actor", AccountID: "account"}
			got, e := client.Discover(context.Background(), wantScope)
			if pricingSource == PricingLLMRouterNative {
				if !errors.Is(e, ErrProviderCatalog) {
					t.Fatalf("accepted legacy catalog for scoped request: %+v %v", got, e)
				}
				return
			}
			if e != nil || len(got.Models) != 1 || !strings.Contains(string(got.Models[0].CapabilityMetadata), "current-revision") {
				t.Fatalf("scoped provider tariff was lost: %+v %v", got, e)
			}
		})
	}
}
