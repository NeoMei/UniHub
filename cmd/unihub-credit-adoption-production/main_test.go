package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"unihub/internal/billing"
)

func TestProductionAdoptionApprovalBindsPlanBytesAndSelection(t *testing.T) {
	request := billing.LegacyCreditAdoptionRequest{ActorID: "actor", AccountID: "payer", ProductID: "superwagie", BatchIDs: []string{"batch_a", "batch_b"}}
	plan := billing.LegacyCreditAdoptionPlan{Request: request, Asset: billing.CreditAsset,
		CanApply: true, ProfileVersion: 1, Activity: &billing.LegacyCreditAdoptionActivity{}}
	raw, e := json.Marshal(plan)
	if e != nil {
		t.Fatal(e)
	}
	plan.Digest = bytesDigest(raw)
	raw, e = json.Marshal(plan)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "approval.json")
	if e = os.WriteFile(path, raw, 0o600); e != nil {
		t.Fatal(e)
	}
	digest := bytesDigest(raw)
	if p, e := readApproval(path, digest, request); e != nil || p.Digest != plan.Digest {
		t.Fatalf("could not read exact approved plan: %+v %v", p, e)
	}
	other := request
	other.BatchIDs = []string{"batch_a"}
	if _, e = readApproval(path, digest, other); e == nil {
		t.Fatal("approval accepted different explicit selection")
	}
	if _, e = readApproval(path, strings.Repeat("0", 64), request); e == nil {
		t.Fatal("approval accepted mismatched file digest")
	}
	if e = os.WriteFile(path, append(raw, '\n'), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e = readApproval(path, digest, request); e == nil {
		t.Fatal("approval accepted different file bytes")
	}
	plan.Activity.Calls = 1
	tampered, e := json.Marshal(plan)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, tampered, 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e = readApproval(path, bytesDigest(tampered), request); e == nil {
		t.Fatal("approval accepted a different plan with an original digest")
	}
}

func TestProductionAdoptionRequiresLocalProductionConfiguration(t *testing.T) {
	env := map[string]string{
		"APP_ENV":                      "production",
		"UNIHUB_ADOPTION_DATABASE_URL": "postgres://unihub@127.0.0.1/unihub?sslmode=disable",
		"UNIHUB_ADOPTION_LEDGER_URL":   "http://127.0.0.1:53478",
		"UNIHUB_ADOPTION_LEDGER_NAME":  "unihub",
	}
	get := func(name string) string { return env[name] }
	preview, _, _, e := productionConfig(get, "preview")
	if e != nil || preview.ConnConfig.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatalf("preview did not force read-only: %v", e)
	}
	env["UNIHUB_ADOPTION_DATABASE_URL"] += "&search_path=legacy_credit_fixture_test"
	if _, _, _, e = productionConfig(get, "apply"); e == nil {
		t.Fatal("production entry point accepted a fixture schema")
	}
	env["UNIHUB_ADOPTION_DATABASE_URL"] = "postgres://unihub@127.0.0.1/unihub?sslmode=disable"
	env["APP_ENV"] = "development"
	if _, _, _, e = productionConfig(get, "apply"); e == nil {
		t.Fatal("production command accepted development")
	}
}
