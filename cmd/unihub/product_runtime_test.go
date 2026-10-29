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
