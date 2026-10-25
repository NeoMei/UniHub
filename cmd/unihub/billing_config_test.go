package main

import (
	"os"
	"path/filepath"
	"testing"
	"unihub/internal/billing"
)

func TestBillingConfigurationIsIndependentAndStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	env := map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:53068", "OIDC_ISSUER": "http://localhost:58000", "OIDC_AUDIENCE": "unihub", "PRODUCT_BILLING_FILE": path}
	write := func(raw string) {
		t.Helper()
		if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(`[{"product_id":"a","version":1,"preset":"prepaid"},{"product_id":"b","version":3,"preset":"subscription"}]`)
	c, e := readConfig(func(k string) string { return env[k] })
	if e != nil || len(c.BillingProfiles) != 2 || c.BillingProfiles[0].Usage.DefaultFundingMode != billing.FundingPrepaidCash || c.IdentityAuditVerified {
		t.Fatalf("separate billing config %v", e)
	}
	for _, raw := range []string{`null`, `{}`, `[{"product_id":"a","version":0,"preset":"prepaid"}]`, `[{"product_id":"a","version":1,"preset":"other"}]`, `[{"product_id":"a","version":1,"preset":"prepaid","client_secret":"x"}]`, `[{"product_id":"a","version":1,"preset":"prepaid"},{"product_id":"a","version":2,"preset":"subscription"}]`, `[{"product_id":"a","version":1,"subscription":{"enabled":false,"other":true}}]`, `[] {}`} {
		write(raw)
		if _, e = readConfig(func(k string) string { return env[k] }); e == nil {
			t.Fatalf("invalid profile accepted %s", raw)
		}
	}
}
