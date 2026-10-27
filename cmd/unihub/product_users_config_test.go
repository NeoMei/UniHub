package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/testutil"
)

func TestProductUserManifestConfigurationIndependentFromBilling(t *testing.T) {
	file := filepath.Join(t.TempDir(), "users.json")
	raw := `[{"product_id":"notes","name":"Notes","entry_url":"https://notes.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`
	os.WriteFile(file, []byte(raw), 0600)
	clients := filepath.Join(t.TempDir(), "clients.json")
	os.WriteFile(clients, []byte(`[{"product_id":"notes","client_id":"notes-client","scopes":["access:read"]}]`), 0600)
	env := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:53068", "OIDC_ISSUER": "http://localhost:58000", "OIDC_AUDIENCE": "unihub", "PUBLIC_URL": "http://localhost:8088", "OIDC_CLIENT_SECRET": "test", "PRODUCT_CLIENTS_FILE": clients, "PRODUCT_USER_MANIFESTS_FILE": file, "USER_ADMIN_IDS": "11111111-1111-4111-8111-111111111111"}
	c, e := readConfig(func(k string) string { return env[k] })
	if e != nil || len(c.ProductUserManifests) != 1 || len(c.UserAdminIDs) != 1 {
		t.Fatalf("manifest config %v", e)
	}
	env["USER_ADMIN_IDS"] = "admin@example.test"
	if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("email admin configured")
	}
	env["USER_ADMIN_IDS"] = ""
	os.WriteFile(clients, []byte(`[{"product_id":"unknown","client_id":"notes-client","scopes":["access:read"]}]`), 0600)
	if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("unknown product instance allowed")
	}
}

func TestAccessOnlyProductDoesNotCreateCommercialDirectoryEntry(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e := ensureConfiguredProducts(ctx, p, map[string]products.Client{"notes-client": {ProductID: "notes", ClientID: "notes-client", Scopes: []string{"access:read", "accounts:read"}}}); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := p.QueryRow(ctx, `SELECT count(*) FROM products WHERE id='notes'`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("identity-only product became billing product: %d %v", count, e)
	}
}
