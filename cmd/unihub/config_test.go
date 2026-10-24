package main

import "testing"

func TestProductionCannotEnableSandboxOrHTTPIdentity(t *testing.T) {
	env := map[string]string{"APP_ENV": "production", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://ledger:3068", "OIDC_ISSUER": "https://auth.example.com", "OIDC_AUDIENCE": "unihub", "PAYMENT_MODE": "sandbox", "PAYMENT_MERCHANT": "test", "PAYMENT_SIGNING_KEY": "abcdefghijklmnopqrstuvwxyz123456"}
	if _, e := readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("production sandbox accepted")
	}
	env["PAYMENT_MODE"] = "disabled"
	env["OIDC_ISSUER"] = "http://localhost:8000"
	if _, e := readConfig(func(k string) string { return env[k] }); e == nil {
		t.Fatal("HTTP issuer accepted in production")
	}
	env["APP_ENV"] = "development"
	if c, e := readConfig(func(k string) string { return env[k] }); e != nil || c.Listen != "127.0.0.1:8088" {
		t.Fatalf("dev config: %+v %v", c, e)
	}
}

func TestSandboxRequiresLoopbackListener(t *testing.T) {
	env := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:53068", "OIDC_ISSUER": "http://localhost:58000", "OIDC_AUDIENCE": "unihub", "PAYMENT_MODE": "sandbox", "PAYMENT_MERCHANT": "sandbox", "PAYMENT_SIGNING_KEY": "abcdefghijklmnopqrstuvwxyz123456", "LISTEN_ADDR": "0.0.0.0:8088"}
	if _, err := readConfig(func(k string) string { return env[k] }); err == nil {
		t.Fatal("sandbox exposed on all interfaces")
	}
	env["LISTEN_ADDR"] = "127.0.0.1:8088"
	if _, err := readConfig(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
}
