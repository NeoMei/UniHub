package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/payments"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestCapabilitiesAdvertiseOnlyConfiguredPaymentProviders(t *testing.T) {
	b := billing.New(nil, nil, nil)
	wechat := &checkoutHTTPProvider{}
	b.ConfigurePayments([]payments.Provider{wechat})
	s := &api{wallet: wallet.New(nil, nil, noLedger{}), options: Options{Billing: b}}

	w := httptest.NewRecorder()
	if err := s.capabilities(w, httptest.NewRequest("GET", "/v1/capabilities", nil), accounts.User{ID: "account"}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Providers []string `json:"payment_providers"`
		AlipayQR  bool     `json:"alipay_checkout_qr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Providers) != 1 || response.Providers[0] != "wechat" || response.AlipayQR {
		t.Fatalf("capabilities advertised unavailable payment channel: %s", w.Body)
	}
	b.ConfigurePayments([]payments.Provider{wechat, &alipayQRHTTPProvider{}})
	w = httptest.NewRecorder()
	if err := s.capabilities(w, httptest.NewRequest("GET", "/v1/capabilities", nil), accounts.User{ID: "account"}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Providers) != 2 || !response.AlipayQR {
		t.Fatalf("capabilities hid configured payment channels: %s", w.Body)
	}
	s.options.Sandbox = true
	w = httptest.NewRecorder()
	if err := s.capabilities(w, httptest.NewRequest("GET", "/v1/capabilities", nil), accounts.User{ID: "account"}); err != nil {
		t.Fatal(err)
	}
	if string(w.Body.Bytes()) == "" || !strings.Contains(w.Body.String(), `"payment_providers":[]`) {
		t.Fatalf("sandbox capabilities returned an invalid provider shape: %s", w.Body)
	}
}

func TestProductUserAPIRevocationAndAdministrationBoundary(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	admin, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	r, e := productusers.Parse([]byte(`[{"product_id":"notes","name":"Notes","entry_url":"https://notes.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"operator","name":"Operator","permissions":["product:use","channel:read"]}]}]`))
	if e != nil {
		t.Fatal(e)
	}
	users := productusers.New(p, r, "https://hub.test", []string{admin.ID})
	policy := products.Client{ProductID: "notes", ClientID: "notes-client", Scopes: []string{"access:read", "accounts:read"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"notes-client": productAccessVerifier{}}, ProductAccess: map[string]products.Client{"notes-client": policy}})
	w := call(h, "GET", "/v1/me/product-access", "product-alice", "", "")
	var access productusers.Access
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &access) != nil || access.Subject == "" || len(access.Roles) != 1 || access.Roles[0] != "member" {
		t.Fatalf("access %d %s", w.Code, w.Body)
	}
	user := access.Subject
	for _, path := range []string{"/v1/user-management", "/v1/user-management/users", "/v1/user-management/products"} {
		if w = call(h, "GET", path, "product-alice", "", ""); w.Code != 403 {
			t.Fatalf("product admin %s %d", path, w.Code)
		}
	}
	if w = call(h, "GET", "/v1/me/product-access?product_id=foreign", "product-alice", "", ""); w.Code != 403 {
		t.Fatal("selected foreign product", w.Code)
	}
	if w = call(h, "PUT", "/v1/user-management/users/"+user+"/access/notes", "bob", `{"roles":["operator"],"active":true}`, ""); w.Code != 400 {
		t.Fatal("missing version", w.Code)
	}
	if w = call(h, "PUT", "/v1/user-management/users/"+user+"/access/notes", "bob", `{"roles":["operator"],"active":true,"version":0}`, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = call(h, "PUT", "/v1/user-management/users/"+user+"/access/notes", "bob", `{"roles":[],"active":true,"version":0}`, ""); w.Code != 409 {
		t.Fatal("stale", w.Code)
	}
	if w = call(h, "PUT", "/v1/user-management/users/"+user+"/status", "alice", `{"active":false,"version":1}`, ""); w.Code != 403 {
		t.Fatal("self admin escalation", w.Code)
	}
	if w = call(h, "PUT", "/v1/user-management/users/"+user+"/status", "bob", `{"active":false,"version":1}`, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, token := range []string{"alice", "product-alice"} {
		for _, path := range []string{"/v1/me", "/v1/accounts", "/v1/me/product-access"} {
			if w = call(h, "GET", path, token, "", ""); w.Code != 403 {
				t.Fatalf("old bearer %s %s %d", token, path, w.Code)
			}
		}
	}
}

type productAccessVerifier struct{}

func (productAccessVerifier) Verify(ctx context.Context, token string) (auth.Claims, error) {
	c, e := (productVerifier{}).Verify(ctx, token)
	c.ExpiresAt = time.Now().Add(time.Hour).Unix()
	return c, e
}

func TestProductBlockStopsManagedProductBusinessBearer(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	admin, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(`[{"product_id":"notes","name":"Notes","entry_url":"https://notes.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	users := productusers.New(p, registry, "https://hub.test", []string{admin.ID})
	policy := products.Client{ProductID: "notes", ClientID: "notes-client", Scopes: []string{"access:read", "accounts:read"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"notes-client": productAccessVerifier{}}, ProductAccess: map[string]products.Client{"notes-client": policy}})
	w := call(h, "GET", "/v1/me/product-access", "product-alice", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var access productusers.Access
	if err = json.Unmarshal(w.Body.Bytes(), &access); err != nil {
		t.Fatal(err)
	}
	if _, err = users.SetAccess(ctx, admin.ID, access.Subject, "notes", "default", []string{"member"}, false, 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/me", "/v1/accounts", "/v1/me/product-access"} {
		w = call(h, "GET", path, "product-alice", "", "")
		if w.Code != 403 {
			t.Fatalf("blocked product bearer %s returned %d: %s", path, w.Code, w.Body)
		}
	}
	// Product disable does not disable the same person's independent console.
	if w = call(h, "GET", "/v1/accounts", "alice", "", ""); w.Code != 200 {
		t.Fatal("console denied", w.Code, w.Body)
	}
}
