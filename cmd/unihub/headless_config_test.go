package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitProductRegistryDoesNotElevateLegacyOrShareAudience(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	write := func(body string) {
		t.Helper()
		if e := os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(`[{"product_id":"anotherapp","client_id":"anotherapp-public","scopes":["identity:read","wallets:read"]}]`)
	env := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:53068", "OIDC_ISSUER": "http://localhost:58000", "OIDC_AUDIENCE": "unihub", "PRODUCT_CLIENTS_FILE": path}
	c, e := readConfig(func(k string) string { return env[k] })
	if e != nil || c.ProductAccess["anotherapp-public"].ProductID != "anotherapp" {
		t.Fatalf("registry config %v", e)
	}
	env["OIDC_PRODUCT_CLIENTS"] = "anotherapp=anotherapp-public"
	if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("duplicate legacy/new audience accepted")
	}
	delete(env, "OIDC_PRODUCT_CLIENTS")
	write(`[{"product_id":"anotherapp","client_id":"unihub","scopes":["wallets:read"]}]`)
	if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("console audience accepted")
	}
	write(`[{"product_id":"anotherapp","client_id":"anotherapp-public","scopes":["all:admin"]}]`)
	if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("unknown permission accepted")
	}
	write(`[{"product_id":"anotherapp","client_id":"anotherapp-public","scopes":[],"password_login":true,"application":"app-anotherapp","organization":"unihub","redirect_uris":["http://localhost:9999/callback"]}]`)
	if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("identity enabled without key/private adapter gate")
	}
	env["HEADLESS_IDENTITY_KEY"] = "independent-test-identity-key-at-least-32bytes"
	env["HEADLESS_MANAGEMENT_CLIENT_ID"] = "unihub-identity-service"
	env["HEADLESS_MANAGEMENT_CLIENT_SECRET"] = "private-management-credential"
	env["HEADLESS_AUDIT_REDACTION_VERIFIED"] = "true"
	env["HEADLESS_TRUST_LOOPBACK_PROXY"] = "true"
	if _, e = readConfig(func(k string) string { return env[k] }); e != nil {
		t.Fatal(e)
	}
	env["HEADLESS_TRUST_LOOPBACK_PROXY"] = "yes"
	if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("unknown proxy boolean accepted")
	}
	delete(env, "HEADLESS_TRUST_LOOPBACK_PROXY")
	env["HEADLESS_AUDIT_REDACTION_VERIFIED"] = "yes"
	if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("unknown audit boolean accepted")
	}
}
