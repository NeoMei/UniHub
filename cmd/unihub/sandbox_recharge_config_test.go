package main

import (
	"slices"
	"testing"
)

func sandboxRechargeEnvironment() map[string]string {
	return map[string]string{"APP_ENV": "development", "DATABASE_URL": "postgres://unused", "LEDGER_URL": "http://localhost:53068", "OIDC_ISSUER": "http://localhost:58000", "OIDC_AUDIENCE": "unihub"}
}

func TestSandboxRechargeAllowlistRejectsMalformedIDs(t *testing.T) {
	for _, raw := range []string{"neomei", "*", "alice@example.test", "123456781234123412341234567890ab", "{12345678-1234-1234-1234-1234567890ab}", "12345678-1234-1234-1234-1234567890ab,", ",12345678-1234-1234-1234-1234567890ab", "12345678-1234-1234-1234-1234567890ab,,aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"} {
		t.Run(raw, func(t *testing.T) {
			env := sandboxRechargeEnvironment()
			env["SANDBOX_RECHARGE_USER_IDS"] = raw
			if _, err := readConfig(func(k string) string { return env[k] }); err == nil {
				t.Fatal("invalid sandbox recharge user configuration accepted")
			}
		})
	}
}

func TestSandboxRechargeAllowlistNormalizesDeduplicatesAndDeniesEmpty(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{"", nil}, {" \t", nil},
		{" ABCDEF12-1234-1234-1234-1234567890AB , abcdef12-1234-1234-1234-1234567890ab , 22222222-2222-2222-2222-222222222222 ", []string{"abcdef12-1234-1234-1234-1234567890ab", "22222222-2222-2222-2222-222222222222"}},
	} {
		env := sandboxRechargeEnvironment()
		env["SANDBOX_RECHARGE_USER_IDS"] = tc.raw
		c, err := readConfig(func(k string) string { return env[k] })
		if err != nil || !slices.Equal(c.SandboxRechargeUserIDs, tc.want) {
			t.Fatalf("allowlist normalization: ids=%v err=%v", c.SandboxRechargeUserIDs, err)
		}
	}
}
