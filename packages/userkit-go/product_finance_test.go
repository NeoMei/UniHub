package userkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProductFinanceTransportIsFiniteAndForwardsOnce(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/v1/product-finance/accounts/acct-1/recharges" || r.URL.Query().Get("product_id") != "llmrouter" {
			t.Errorf("wrong target: %s", r.URL.String())
		}
		if r.Header.Values("Authorization")[0] != "Bearer service-token-long-enough-for-test" || r.Header.Values("X-UniHub-Actor-Authorization")[0] != "Bearer upa_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
			t.Error("credentials not bound to their headers")
		}
		if r.Header.Get("Idempotency-Key") != "recharge-1" {
			t.Error("idempotency key lost")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"re_1"}`))
	}))
	defer srv.Close()
	c, err := NewProductFinanceClient(ProductFinanceConfig{BaseURL: srv.URL, ProductID: "llmrouter", ServiceToken: "service-token-long-enough-for-test"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Do(context.Background(), "POST", "/accounts/acct-1/recharges", url.Values{"product_id": {"llmrouter"}}, "upa_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", []byte(`{"amount":"1000"}`), "recharge-1")
	if err != nil || got.StatusCode != 201 || string(got.Body) != `{"id":"re_1"}` || calls != 1 {
		t.Fatalf("result=%#v err=%v calls=%d", got, err, calls)
	}
}

func TestProductFinanceTransportRejectsUnboundedInputsAndDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/accounts", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, err := NewProductFinanceClient(ProductFinanceConfig{BaseURL: srv.URL, ProductID: "llmrouter", ServiceToken: "service-token-long-enough-for-test"})
	if err != nil {
		t.Fatal(err)
	}
	actor := "upa_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		method, path, actor string
		query               url.Values
	}{
		{"GET", "https://attacker.test/accounts", actor, nil},
		{"GET", "/accounts/../admin", actor, nil},
		{"GET", "/accounts", "upb_wrong", nil},
		{"PATCH", "/accounts/acct/api-keys/key", actor, nil},
		{"GET", "/accounts", actor, url.Values{"admin": {"true"}}},
	} {
		if _, err = c.Do(context.Background(), tc.method, tc.path, tc.query, tc.actor, nil, ""); err == nil {
			t.Errorf("unsafe request accepted: %#v", tc)
		}
	}
	if _, err = c.Do(context.Background(), "GET", "/accounts", nil, actor, nil, ""); err == nil {
		t.Fatal("redirect should be rejected")
	}
	if calls != 1 {
		t.Fatalf("request replayed or redirect followed: %d calls", calls)
	}
	if strings.Contains(c.config.ServiceToken, "upa_") {
		t.Fatal("service token accidentally resembles actor")
	}
}
