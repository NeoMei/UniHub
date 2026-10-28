package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/platformadmin"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestPlatformAdminCentralOnlyAndZeroProducts(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	bob, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	admin := platformadmin.New(p, nil)
	if e = admin.Bootstrap(ctx, "deployment-cli", bob.ID, "explicit local bootstrap"); e != nil {
		t.Fatal(e)
	}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{PlatformAdmin: admin, ProductVerifiers: map[string]auth.Verifier{"notes-client": productAccessVerifier{}}})
	for _, path := range []string{"/admin/v1/me", "/admin/v1/users", "/admin/v1/admins", "/admin/v1/access-products"} {
		w := call(h, "GET", path, "bob", "", "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(path, w.Code, w.Body)
		}
		for _, token := range []string{"alice", "product-alice"} {
			w = call(h, "GET", path, token, "", "")
			want := 403
			if path == "/admin/v1/me" && token == "alice" {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("%s %s returned %d %s", path, token, w.Code, w.Body)
			}
		}
	}
	w := call(h, "GET", "/admin/v1/users?limit=1", "bob", "", "")
	var page platformadmin.Page[platformadmin.User]
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != 2 || page.NextCursor == "" {
		t.Fatal(w.Code, w.Body)
	}
}
func TestLegacyCompatibilityDoesNotRegrantRawAllowlist(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	bob, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	alice, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	users := productusers.New(p, productusers.Registry{}, "https://hub.test", []string{alice.ID})
	admin := platformadmin.New(p, users)
	if e := admin.Bootstrap(ctx, "deployment-cli", bob.ID, "bootstrap"); e != nil {
		t.Fatal(e)
	}
	if e := admin.ImportLegacy(ctx, "deployment-cli", []string{alice.ID}, "legacy import"); e != nil {
		t.Fatal(e)
	}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{PlatformAdmin: admin, ProductUsers: users})
	w := call(h, "GET", "/v1/user-management/users", "alice", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if _, e := admin.SetRoles(ctx, bob.ID, alice.ID, []string{}, 1, "revoke legacy"); e != nil {
		t.Fatal(e)
	}
	if !users.CanManage(alice.ID) {
		t.Fatal("fixture raw allowlist unexpectedly gone")
	}
	w = call(h, "GET", "/v1/user-management/users", "alice", "", "")
	if w.Code != 403 {
		t.Fatal("raw list regained HTTP access", w.Code, w.Body)
	}
	if _, e := users.SetStatus(ctx, alice.ID, bob.ID, false, 1); e == nil {
		t.Fatal("raw list regained direct store access")
	}
	w = call(h, "POST", "/admin/v1/users/status-batch", "bob", fmt.Sprintf(`{"items":[{"user_id":%q,"active":false,"version":1}],"reason":"batch check"}`, bob.ID), "")
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
		t.Fatal(w.Code, w.Body)
	}
	var v map[string][]map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil || v["items"][0]["error"] != "forbidden" {
		t.Fatal(w.Body, e)
	}
}
