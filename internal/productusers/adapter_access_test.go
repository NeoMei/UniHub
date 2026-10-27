package productusers

import (
	"context"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestAdapterRoleAndExplicitDenialAreTheOnlyProductAuthority(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	u, e := accounts.New(pool).EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "native-admin"})
	if e != nil {
		t.Fatal(e)
	}
	r, _ := Parse([]byte(manifests))
	s := New(pool, r, "https://hub.test", nil)
	_, e = pool.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version,product_role,permission_overrides) VALUES($1,'notes','default','["operator"]',true,1,10,'{"channel":{"read":false}}')`, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.Resolve(ctx, u, "notes", "default")
	if e != nil {
		t.Fatal(e)
	}
	if a.ProductRole != 10 {
		t.Fatal("product administrator role was lost")
	}
	if e = s.RequirePermissionOn(ctx, pool, u.ID, "notes", "default", "channel:read"); e == nil {
		t.Fatal("central explicit deny was unioned with role grant")
	}
	if e = s.RequirePermissionOn(ctx, pool, u.ID, "notes", "default", "product:use"); e != nil {
		t.Fatal(e)
	}
}

func TestDelegatedViewerHasPageRoleWithoutNativeAdministratorUnion(t *testing.T) {
	r, e := Parse([]byte(`[{"product_id":"notes","name":"Notes","entry_url":"https://notes.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"viewer","name":"Viewer","privileged":true,"product_role":10,"permissions":["product:use","channel:read"]},{"id":"native_admin","name":"Native admin","privileged":true,"product_role":10,"native_baseline":true,"permissions":["product:use","channel:read","channel:write"]}]}]`))
	if e != nil {
		t.Fatal(e)
	}
	m, _ := r.Get("notes", "default")
	permissions, e := EffectivePermissions(m, []string{"viewer"}, nil, baselineProductRole(m, []string{"viewer"}))
	if e != nil {
		t.Fatal(e)
	}
	if EffectiveProductRole(m, []string{"viewer"}, 1) != 10 {
		t.Fatal("viewer original page role missing")
	}
	for _, p := range permissions {
		if p == "channel:write" {
			t.Fatal("viewer acquired native admin write baseline")
		}
	}
	permissions, e = EffectivePermissions(m, []string{"member"}, nil, 10)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range permissions {
		found = found || p == "channel:write"
	}
	if !found {
		t.Fatal("imported native administrator baseline was reduced")
	}
	if _, e = EffectivePermissions(m, []string{"member"}, map[string]map[string]bool{"channel": {"unknown": true}}, 1); e == nil {
		t.Fatal("unknown permission override accepted")
	}
}
