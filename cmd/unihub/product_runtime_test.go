package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

func TestRuntimeServicesAreExplicitScopedAndDoNotReuseBillingCredential(t *testing.T) {
	registry := productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default"}}
	if out, e := parseRuntimeServices("", registry, "old-billing-token-over-thirty-two-bytes"); e != nil || len(out) != 0 {
		t.Fatal("runtime enabled without configuration")
	}
	valid := `{"lr":{"token":"new-runtime-test-token-over-thirty-two-bytes","product_id":"llmrouter","instance_id":"default","allowed_ops":["access:read","billing:write"]}}`
	out, e := parseRuntimeServices(valid, registry, "old-billing-token-over-thirty-two-bytes")
	if e != nil || out["lr"].ProductID != "llmrouter" || len(out["lr"].AllowedOps) != 2 {
		t.Fatalf("scoped runtime config: %v", e)
	}
	for _, change := range []string{"token", "product", "operation", "duplicate-token", "empty-ops", "unknown-field"} {
		var in map[string]map[string]any
		if e = json.Unmarshal([]byte(valid), &in); e != nil {
			t.Fatal(e)
		}
		switch change {
		case "token":
			in["lr"]["token"] = "old-billing-token-over-thirty-two-bytes"
		case "product":
			in["lr"]["product_id"] = "other"
		case "operation":
			in["lr"]["allowed_ops"] = []string{"*"}
		case "duplicate-token":
			in["second"] = in["lr"]
		case "empty-ops":
			in["lr"]["allowed_ops"] = []string{}
		case "unknown-field":
			in["lr"]["trusted_subject"] = "admin"
		}
		raw, _ := json.Marshal(in)
		if _, e = parseRuntimeServices(string(raw), registry, "old-billing-token-over-thirty-two-bytes"); e == nil {
			t.Errorf("unsafe config accepted: %s", change)
		}
	}
}

func TestFinanceRuntimeRequiresOneExactProductClientAndScopes(t *testing.T) {
	registry := productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default"}}
	clients, err := products.Parse([]byte(`[{"product_id":"llmrouter","instance_id":"default","client_id":"lr-finance","scopes":["accounts:read","wallets:read","recharges:read","recharges:write","operations:read","model-credentials:write","usage:read","settings:write","catalog:read","subscriptions:read","orders:read"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"lr":{"token":"new-runtime-test-token-over-thirty-two-bytes","product_id":"llmrouter","instance_id":"default","finance_client_id":"lr-finance","allowed_ops":["finance:read","finance:write"]}}`
	if _, err = parseRuntimeServicesWithClients(valid, registry, "", clients); err != nil {
		t.Fatalf("valid finance runtime rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"missing-client": strings.Replace(valid, `,"finance_client_id":"lr-finance"`, "", 1),
		"no-finance-op":  strings.Replace(valid, `"finance:read","finance:write"`, `"identity:browser"`, 1),
		"wrong-instance": strings.Replace(valid, `"instance_id":"default"`, `"instance_id":"other"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseRuntimeServicesWithClients(raw, registry, "", clients); e == nil {
				t.Fatal("invalid finance configuration accepted")
			}
		})
	}
	clients["lr-finance"] = products.Client{ProductID: "llmrouter", InstanceID: "default", ClientID: "lr-finance", Scopes: []string{"accounts:read"}}
	readOnly := strings.Replace(valid, `,"finance:write"`, "", 1)
	if _, err = parseRuntimeServicesWithClients(readOnly, registry, "", clients); err != nil {
		t.Fatalf("legitimate read-only client rejected: %v", err)
	}
	writeOnly := strings.Replace(valid, `"finance:read",`, "", 1)
	if _, err = parseRuntimeServicesWithClients(writeOnly, registry, "", clients); err != nil {
		t.Fatalf("legitimate write-only runtime rejected: %v", err)
	}
}

func TestRuntimeServiceConfigAllowsExplicitCashCapabilities(t *testing.T) {
	registry, err := productusers.Parse([]byte(`[{"product_id":"llmrouter","name":"Router","entry_url":"https://router.test","version":1,"roles":[{"id":"member","name":"Member","permissions":["product:use"]}],"default_roles":["member"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"cash:charge", "cash:payment"} {
		raw := `{"lr":{"token":"test-runtime-credential-over-thirty-two-bytes","product_id":"llmrouter","allowed_ops":["` + op + `"]}}`
		out, err := parseRuntimeServices(raw, registry, "")
		if err != nil || len(out) != 1 {
			t.Fatalf("explicit %s capability rejected: %v", op, err)
		}
	}
}

func TestRuntimeBootstrapValidatesIdentityLinkRole(t *testing.T) {
	clients, err := products.Parse([]byte(`[{"product_id":"agentwiki","instance_id":"wiki","client_id":"aw","browser_sso":true,"redirect_uris":["https://wiki.test/return"],"identity_link_role":"member"}]`))
	if err != nil {
		t.Fatal(err)
	}
	const runtime = `{"aw":{"token":"test-runtime-credential-over-thirty-two-bytes","product_id":"agentwiki","instance_id":"wiki","identity_client_id":"aw","allowed_ops":["identity:link"]}}`
	for _, tt := range []struct {
		name, instance, role string
		wantError            bool
	}{
		{"least privilege", "wiki", `"id":"member","permissions":["product:use"]`, false},
		{"missing exact instance", "other", `"id":"member","permissions":["product:use"]`, true},
		{"missing exact role", "wiki", `"id":"other","permissions":["product:use"]`, true},
		{"privileged role", "wiki", `"id":"member","privileged":true,"permissions":["product:use"]`, true},
		{"elevated role", "wiki", `"id":"member","product_role":10,"permissions":["product:use"]`, true},
		{"native baseline", "wiki", `"id":"member","product_role":10,"native_baseline":true,"permissions":["product:use"]`, true},
		{"missing product use", "wiki", `"id":"member","permissions":["user:read"]`, true},
		{"extra permissions", "wiki", `"id":"member","permissions":["product:use","user:read"]`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := productusers.Parse([]byte(`[{"product_id":"agentwiki","instance_id":"` + tt.instance + `","name":"AgentWiki","entry_url":"https://wiki.test","version":1,"roles":[{"name":"Candidate",` + tt.role + `}]}]`))
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{"", runtime} {
				if _, err := parseRuntimeServicesWithClients(raw, registry, "", clients); (err != nil) != tt.wantError {
					t.Errorf("runtime configured=%t: error = %v, want error = %t", raw != "", err, tt.wantError)
				}
			}
		})
	}
}

func TestRuntimeBootstrapRejectsDefaultProductUseForOptedInInstance(t *testing.T) {
	const client = `[{"product_id":"agentwiki","client_id":"aw","browser_sso":true,"redirect_uris":["https://wiki.test/return"]`
	optedIn, err := products.Parse([]byte(client + `,"identity_link_role":"member"}]`))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := products.Parse([]byte(client + `}]`))
	if err != nil {
		t.Fatal(err)
	}
	const unrelatedRuntime = `{"aw":{"token":"test-runtime-credential-over-thirty-two-bytes","product_id":"agentwiki","allowed_ops":["access:read"]}}`
	for _, tt := range []struct {
		defaultRole string
		wantError   bool
	}{
		{"member", true},
		{"other-member", true},
		{"reader", false},
	} {
		t.Run(tt.defaultRole, func(t *testing.T) {
			registry, err := productusers.Parse([]byte(`[{
				"product_id":"agentwiki","name":"AgentWiki","entry_url":"https://wiki.test","version":1,
				"default_roles":["` + tt.defaultRole + `"],
				"roles":[
					{"id":"member","name":"Member","permissions":["product:use"]},
					{"id":"other-member","name":"Other Member","permissions":["product:use","user:read"]},
					{"id":"reader","name":"Reader","permissions":["user:read"]}
				]
			}]`))
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{"", unrelatedRuntime} {
				if _, err := parseRuntimeServicesWithClients(raw, registry, "", optedIn); (err != nil) != tt.wantError {
					t.Errorf("runtime configured=%t: error = %v, want error = %t", raw != "", err, tt.wantError)
				}
				if _, err := parseRuntimeServicesWithClients(raw, registry, "", legacy); err != nil {
					t.Errorf("client without opt-in rejected: %v", err)
				}
			}
		})
	}
}
