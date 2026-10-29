package main

import "testing"

func TestProductAudiencesRemainDistinctFromConsoleAndOperators(t *testing.T) {
	env := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:53068", "OIDC_ISSUER": "http://localhost:58000", "OIDC_AUDIENCE": "unihub", "OPS_OIDC_AUDIENCE": "operators"}
	for _, input := range []string{"superwagie", "=product", "superwagie=", "superwagie=unihub", "superwagie=operators", "superwagie=product,agentwiki=product", "superwagie=one,superwagie=two", "other/client=product"} {
		env["OIDC_PRODUCT_CLIENTS"] = input
		if _, err := readConfig(func(k string) string { return env[k] }); err == nil {
			t.Errorf("ambiguous product config accepted %q", input)
		}
	}
	env["OIDC_PRODUCT_CLIENTS"] = "superwagie=sw-client,agentwiki=wiki-client"
	if _, err := readConfig(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
}

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

func TestBrowserAndServiceConfigurationRejectUnsafeBoundaries(t *testing.T) {
	env := map[string]string{"APP_ENV": "production", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://ledger:3068", "OIDC_ISSUER": "https://auth.example.test", "OIDC_AUDIENCE": "unihub", "PUBLIC_URL": "http://localhost:8088", "OIDC_CLIENT_SECRET": "test-secret"}
	read := func() error { _, err := readConfig(func(k string) string { return env[k] }); return err }
	if read() == nil {
		t.Fatal("production accepted insecure browser cookies")
	}
	env["APP_ENV"] = "development"
	for _, u := range []string{"http://example.test", "https://user:password@example.test", "https://console.example.test/path", "https://console.example.test?next=evil", "https://console.example.test#fragment"} {
		env["PUBLIC_URL"] = u
		if read() == nil {
			t.Fatalf("unsafe public origin accepted: %s", u)
		}
	}
	env["PUBLIC_URL"] = "http://localhost:8088"
	delete(env, "OIDC_CLIENT_SECRET")
	if read() == nil {
		t.Fatal("browser enabled without client credential")
	}
	env["OIDC_CLIENT_SECRET"] = "test-secret"
	env["MODEL_SERVICE_TOKEN"] = "short"
	if read() == nil {
		t.Fatal("weak service credential accepted")
	}
	env["MODEL_SERVICE_TOKEN"] = "separate-trusted-router-credential-at-least-32-bytes"
	if err := read(); err != nil {
		t.Fatal(err)
	}
	env["APP_ENV"] = "production"
	env["PUBLIC_URL"] = "https://console.example.test"
	env["SANDBOX_CATALOG"] = "true"
	if read() == nil {
		t.Fatal("production test catalog accepted")
	}
}

func TestPasswordResetVerificationRequiresRecoveryMailRelay(t *testing.T) {
	env := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:53068", "OIDC_ISSUER": "http://localhost:58000", "OIDC_AUDIENCE": "unihub", "ADMIN_PASSWORD_RESET_VERIFIED": "true"}
	if _, err := readConfig(func(k string) string { return env[k] }); err == nil {
		t.Fatal("password reset verified without recovery mail relay")
	}
	env["RECOVERY_SMTP_ADDR"] = "127.0.0.1:25"
	if _, err := readConfig(func(k string) string { return env[k] }); err == nil {
		t.Fatal("password reset verified without recovery sender address")
	}
	env["RECOVERY_SMTP_FROM"] = "recovery@quukk.com"
	c, err := readConfig(func(k string) string { return env[k] })
	if err != nil || c.RecoverySMTPAddr != "127.0.0.1:25" || c.RecoverySMTPFrom != "recovery@quukk.com" {
		t.Fatalf("configured relay rejected: %+v %v", c, err)
	}
}
