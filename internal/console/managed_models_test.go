package console

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/httpapi"
	"unihub/internal/testutil"
)

func TestPublicManagedModelsThroughProductionConsole(t *testing.T) {
	ctx := t.Context()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	b := billing.New(pool, nil, nil)
	profile, err := billing.BillingPreset("superwagie", "hybrid")
	if err != nil {
		t.Fatal(err)
	}
	profile.Version = 4
	if err := b.ApplyConfiguredProfiles(ctx, []billing.BillingProfile{profile}); err != nil {
		t.Fatal(err)
	}

	s := &Server{opts: Options{Production: true}}
	h := s.Handler(httpapi.New(nil, nil, nil, httpapi.Options{Billing: b}))
	server := httptest.NewServer(h)
	defer server.Close()

	const route = "/v1/products/superwagie/managed-models"
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), method, server.URL+route, nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-cache" {
				t.Fatalf("anonymous %s = %d, cache-control %q, body %s", method, res.StatusCode, res.Header.Get("Cache-Control"), body)
			}
			if method == http.MethodHead {
				if len(body) != 0 {
					t.Fatalf("HEAD returned a response body: %s", body)
				}
				return
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(body, &got); err != nil || len(got) != 3 ||
				string(got["product_id"]) != `"superwagie"` || string(got["version"]) != "4" ||
				string(got["managed_models"]) != "null" {
				t.Fatalf("public policy response = %s, error = %v", body, err)
			}
		})
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, route},
		{http.MethodPut, route},
		{http.MethodOptions, route},
		{http.MethodGet, "/v1/products/superwagie/billing-profile"},
		{http.MethodHead, "/v1/products/superwagie/billing-profile"},
		{http.MethodGet, route + "/"},
		{http.MethodGet, route + "/extra"},
		{http.MethodGet, "/v1/products//managed-models"},
		{http.MethodGet, "/v1/products/a/b/managed-models"},
		{http.MethodGet, "/v1/products/a%2Fb/managed-models"},
		{http.MethodGet, "/v1/products/SuperWagie/managed-models"},
		{http.MethodGet, "/v1/products/" + strings.Repeat("a", 65) + "/managed-models"},
	} {
		w := request(h, tc.method, tc.path, nil, "", "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("protected %s %s = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}
