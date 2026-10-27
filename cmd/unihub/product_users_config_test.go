package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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

func TestPreparedAccessOnlyClientRegistersWithoutCommercialDependency(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	registry, planPath := filepath.Join(dir, "clients.json"), filepath.Join(dir, "plan.json")
	cmd := exec.Command("python3", "../../scripts/product_client.py", "prepare",
		"--registry", registry, "--output", planPath,
		"--product-id", "notes", "--instance-id", "default",
		"--client-id", "notes-local", "--application", "app-notes-local",
		"--redirect-uri", "http://127.0.0.1:58120/auth/callback",
		"--origin", "http://127.0.0.1:58120", "--scope", "access:read", "--browser-sso")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare: %v: %s", err, out)
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct{ Client json.RawMessage }
	if err = json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	// Consume the exact prepared client, as publication would, without an IdP write.
	raw, err = json.Marshal([]json.RawMessage{plan.Client})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registry, raw, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "users.json")
	if err = os.WriteFile(manifest, []byte(`[{"product_id":"notes","name":"Notes","entry_url":"http://127.0.0.1:58120","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:53068", "OIDC_ISSUER": "http://localhost:58000", "OIDC_AUDIENCE": "unihub", "OIDC_CLIENT_SECRET": "test", "PUBLIC_URL": "http://localhost:8088", "PRODUCT_CLIENTS_FILE": registry, "PRODUCT_USER_MANIFESTS_FILE": manifest}
	cfg, err := readConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	for _, existingDisabled := range []bool{false, true} {
		name := "no-commercial-row"
		if existingDisabled {
			name = "disabled-same-slug-row"
		}
		t.Run(name, func(t *testing.T) {
			pool := testutil.Database(t)
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			want := 0
			if existingDisabled {
				want = 1
				if _, err := pool.Exec(ctx, `INSERT INTO products(id,name,enabled) VALUES('notes','Notes',false)`); err != nil {
					t.Fatal(err)
				}
			}
			if err := ensureConfiguredProducts(ctx, pool, cfg.ProductAccess); err != nil {
				t.Fatalf("identity-only registration acquired commercial startup dependency: %v", err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM products WHERE id='notes'`).Scan(&count); err != nil || count != want {
				t.Fatalf("identity-only registration inserted commercial row: %d %v", count, err)
			}
			if existingDisabled {
				var enabled bool
				if err := pool.QueryRow(ctx, `SELECT enabled FROM products WHERE id='notes'`).Scan(&enabled); err != nil || enabled {
					t.Fatalf("identity-only registration enabled commercial row: %t %v", enabled, err)
				}
			}
		})
	}
}
