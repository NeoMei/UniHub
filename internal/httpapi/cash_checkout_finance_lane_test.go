package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/products"
)

// TestCashCheckoutOnProductFinanceLane guards the LLMRouter real-money topup
// path: the runtime-actor lane must expose the same cash-checkouts actions the
// public API has, otherwise the product BFF relays a 404 to ordinary users.
func TestCashCheckoutOnProductFinanceLane(t *testing.T) {
	s := &api{options: Options{ProductRuntime: map[string]RuntimeService{
		"lr": {Token: strings.Repeat("s", 40), ProductID: "llmrouter", InstanceID: "default", FinanceClientID: "lr-client", AllowedOps: []string{"finance:read", "finance:write"}},
	}}}
	actions := map[string]action{}
	for _, pattern := range productFinanceRoutes {
		actions[pattern] = func(w http.ResponseWriter, _ *http.Request, u accounts.User) error {
			return respond(w, http.StatusOK, map[string]string{"user": u.ID})
		}
	}
	mux := http.NewServeMux()
	s.productFinanceRoutes(mux, actions)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/internal/v1/product-finance/accounts/acct-1/cash-checkouts"},
		{"GET", "/internal/v1/product-finance/accounts/acct-1/cash-checkouts"},
		{"GET", "/internal/v1/product-finance/accounts/acct-1/cash-checkouts/ct_1"},
		{"POST", "/internal/v1/product-finance/accounts/acct-1/cash-checkouts/ct_1/reconcile"},
		{"POST", "/internal/v1/product-finance/accounts/acct-1/cash-checkouts/ct_1/close"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		_, pattern := mux.Handler(r)
		if pattern == "" {
			t.Fatalf("%s %s not registered on internal product-finance lane", tc.method, tc.path)
		}
	}
}

func TestCashCheckoutProductScope(t *testing.T) {
	c := products.Client{
		ClientID: "lr-finance", ProductID: "llmrouter", InstanceID: "default",
		Scopes: []string{"recharges:read", "recharges:write"},
	}
	for _, pattern := range []string{
		"POST /v1/accounts/{account}/cash-checkouts",
		"GET /v1/accounts/{account}/cash-checkouts",
		"GET /v1/accounts/{account}/cash-checkouts/{checkout}",
		"POST /v1/accounts/{account}/cash-checkouts/{checkout}/reconcile",
		"POST /v1/accounts/{account}/cash-checkouts/{checkout}/close",
	} {
		if !productScopeAllowed(pattern, c) {
			t.Fatalf("scope denied for %s", pattern)
		}
	}
}
