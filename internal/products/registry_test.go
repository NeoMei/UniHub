package products

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRejectsEscalationAndUnsafeURLs(t *testing.T) {
	for _, raw := range []string{
		`[{"product_id":"new-product","client_id":"client","origins":["https://%ZZ"]}]`,
		`[{"product_id":"new-product","client_id":"client","scopes":["root:all"]}]`,
		`[{"product_id":"Bad","client_id":"client"}]`,
		`[{"product_id":"new-product","client_id":"client","client_secret":"secret"}]`,
		`[{"product_id":"new-product","client_id":"client","origins":["https://app.test/path"]}]`,
		`[{"product_id":"new-product","client_id":"client","origins":["https://*.test"]}]`,
		`[{"product_id":"new-product","client_id":"client","redirect_uris":["http://app.test/callback"]}]`,
		`[{"product_id":"new-product","client_id":"client","redirect_uris":["https://app.test/%2A"]}]`,
		`[{"product_id":"new-product","client_id":"client"},{"product_id":"other","client_id":"client"}]`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("accepted unsafe config: %s", raw)
		}
	}
	clients, err := Parse([]byte(`[{"product_id":"new-product","client_id":"native","redirect_uris":["http://127.0.0.1:8080/callback"],"origins":["https://app.test"],"scopes":["accounts:read","wallets:read"],"billing_products":["new-product","llmrouter"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	c := clients["native"]
	if c.ProductID != "new-product" || c.Registration || c.PasswordLogin || !c.HasScope("wallets:read") || c.HasScope("orders:write") || !c.AllowsBillingProduct("llmrouter") || c.AllowsBillingProduct("other") {
		t.Fatalf("unsafe client %#v", c)
	}
}

func TestCORSExactOriginsHeadersAndNoCookies(t *testing.T) {
	nextCalls := 0
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nextCalls++; w.WriteHeader(200) }), map[string]Client{"c": {ProductID: "p", Origins: []string{"https://app.test"}}})
	for _, tc := range []struct {
		origin, headers string
		status          int
	}{
		{"https://app.test", "Authorization, Content-Type, Idempotency-Key", 204},
		{"https://app.test.evil", "Authorization", 403},
		{"https://app.test", "X-CSRF-Token", 403},
		{"null", "Authorization", 403},
	} {
		r := httptest.NewRequest("OPTIONS", "/v1/accounts", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", tc.headers)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("cors %s => %d %#v", tc.origin, w.Code, w.Header())
		}
		if tc.status == 204 && (w.Header().Get("Access-Control-Allow-Origin") != "https://app.test" || w.Header().Get("Vary") != "Origin") {
			t.Fatalf("missing exact origin %#v", w.Header())
		}
	}
	if nextCalls != 0 {
		t.Fatalf("preflight reached handler %d", nextCalls)
	}
}

func TestCORSDoesNotGrantCookiesOrAffectConsoleAuth(t *testing.T) {
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), map[string]Client{"c": {Origins: []string{"https://app.test"}}})
	for _, tc := range []struct{ path, token, origin, allow string }{
		{"/auth/session", "", "https://app.test", ""},
		{"/v1/accounts", "", "https://app.test", ""},
		{"/v1/accounts", "Bearer product", "https://app.test", "https://app.test"},
		{"/v1/accounts", "Bearer product", "https://other.test", ""},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Authorization", tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Access-Control-Allow-Origin") != tc.allow || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("CORS %s %#v", tc.path, w.Header())
		}
	}
}

func TestCORSIdentityJSONWithoutBearer(t *testing.T) {
	reached := false
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(202) }), map[string]Client{"c": {Origins: []string{"https://app.test"}}})
	for _, path := range []string{"/v1/identity/authorize/password", "/v1/identity/registrations", "/v1/identity/token"} {
		r := httptest.NewRequest("OPTIONS", path, nil)
		r.Header.Set("Origin", "https://app.test")
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "Content-Type, Idempotency-Key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if reached || w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.test" {
			t.Fatalf("identity preflight %d %#v", w.Code, w.Header())
		}
		for _, origin := range []string{"https://app.test", "https://evil.test"} {
			r = httptest.NewRequest("POST", path, nil)
			r.Header.Set("Origin", origin)
			r.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := ""
			if origin == "https://app.test" {
				want = origin
			}
			if !reached || w.Code != 202 || w.Header().Get("Access-Control-Allow-Origin") != want || w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatalf("identity actual %s %d %#v", origin, w.Code, w.Header())
			}
		}
		reached = false
	}
}

func TestAllProductsUsageScopeIsLlmrouterOnly(t *testing.T) {
	for _, product := range []string{"llmrouter", "superwagie", "agentwiki"} {
		_, err := Parse([]byte(`[{"product_id":"` + product + `","client_id":"pool","scopes":["usage:all-products"]}]`))
		if (err == nil) != (product == "llmrouter") {
			t.Errorf("product %s: %v", product, err)
		}
	}
}
