package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"unihub/internal/modelaccess"
)

func TestNativeCatalogFollowsRouterUpdatesWithoutAnotherKeyMutation(t *testing.T) {
	var count atomic.Int32
	count.Store(45)
	var fail atomic.Bool
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/provider-catalog" || r.Header.Get("Authorization") != "Bearer catalog-only-test-token-at-least-32-bytes" {
			w.WriteHeader(403)
			return
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		var request modelaccess.ProviderCatalogRequest
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			w.WriteHeader(400)
			return
		}
		models := []modelaccess.Model{}
		for i := 0; i < int(count.Load()); i++ {
			models = append(models, modelaccess.Model{ID: fmt.Sprintf("router-model-%d", i), Name: "Router", Operations: []string{"chat"}, CapabilityMetadata: json.RawMessage(`{"operations":[{"id":"chat.completions.create","available":true,"executable":true}]}`)})
		}
		_ = json.NewEncoder(w).Encode(modelaccess.ProviderCatalogResponse{ProviderCatalogRequest: request, SchemaVersion: 1, CatalogVersion: fmt.Sprint(count.Load()), ModelScope: modelaccess.ScopeLLMRouterAvailable, PricingSource: modelaccess.PricingLLMRouterNative, Models: models})
	}))
	defer router.Close()
	catalog, e := modelaccess.NewProviderCatalog(router.URL+"/internal/v1/provider-catalog", "catalog-only-test-token-at-least-32-bytes")
	if e != nil {
		t.Fatal(e)
	}
	f := newAPIKeyModelsFixture(t, catalog)
	key := createScopeTestKey(t, f, f.account, "product-alice")
	path := "/v1/accounts/" + f.account
	var before modelaccess.Discovery
	f.request("POST", path+"/model-discovery", "product-alice", map[string]string{"model_scope": "llmrouter_available"}, "", 200, &before)
	if len(before.Models) != 45 || before.Models[44].ID != "router-model-44" || len(before.Models[0].CapabilityMetadata) == 0 {
		t.Fatalf("catalog truncated or metadata lost: %d", len(before.Models))
	}
	var updated modelaccess.APIKey
	f.request("POST", path+"/api-keys/"+key.ID+"/scope", "product-alice", map[string]string{"model_scope": "llmrouter_available"}, "", 200, &updated)
	if updated.ID != key.ID || updated.CredentialVersion != 2 || updated.Token != "" {
		t.Fatalf("scope upgrade %+v", updated)
	}
	count.Store(46)
	var after modelListResponse
	f.request("GET", "/v1/models", key.Token, nil, "", 200, &after)
	if len(after.Data) != 46 || after.Data[45].ID != "router-model-45" {
		t.Fatalf("catalog froze at key snapshot: %d", len(after.Data))
	}
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, map[string]string{"token": key.Token, "model": "router-model-45", "instance_id": "default", "wire_operation": "chat.completions.create"}, "", 200, nil)
	f.request("POST", path+"/api-keys/"+key.ID+"/scope", "product-alice", map[string]string{"model_scope": "llmrouter_available", "instance_id": "wrong"}, "", 403, nil)
	f.request("POST", path+"/api-keys/"+key.ID+"/scope", "product-alice", map[string]string{"model_scope": "selected"}, "", 400, nil)
	f.request("POST", path+"/api-keys/"+key.ID+"/scope", "product-alice", map[string]string{"model_scope": "llmrouter_available"}, "", 200, &updated)
	if updated.CredentialVersion != 2 {
		t.Fatal("idempotent upgrade changed version")
	}
	fail.Store(true)
	f.request("GET", "/v1/models", key.Token, nil, "", 503, nil)
	f.request("POST", path+"/model-discovery", "product-alice", map[string]string{"model_scope": "llmrouter_available"}, "", 503, nil)
	// The selected discovery lane retains its old local profile and tariffs.
	f.request("POST", path+"/model-discovery", "product-alice", nil, "", 200, &before)
	if len(before.Models) != 6 {
		t.Fatalf("legacy discovery changed: %d", len(before.Models))
	}
}
