package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

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
