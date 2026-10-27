package main

import (
	"encoding/json"
	"testing"
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
