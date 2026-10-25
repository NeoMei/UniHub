package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestProductBillingProfilesReadScopeAndOperatorWrite(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	client := products.Client{ProductID: "superwagie", ClientID: "sw", Scopes: []string{"catalog:read", "subscriptions:read", "accounts:read"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"sw": productVerifier{}}, ProductAccess: map[string]products.Client{"sw": client}})
	w := call(h, "GET", "/v1/products/superwagie/billing-profile", "product-alice", "", "")
	var profile billing.BillingProfile
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &profile) != nil || profile.Version != 0 || profile.Usage.DefaultFundingMode != "credits" {
		t.Fatalf("profile %d %s", w.Code, w.Body)
	}
	w = call(h, "GET", "/v1/products/agentwiki/billing-profile", "product-alice", "", "")
	if w.Code != 403 {
		t.Fatalf("foreign profile %d", w.Code)
	}
	w = call(h, "GET", "/v1/billing-presets", "product-alice", "", "")
	if w.Code != 200 {
		t.Fatalf("presets %d %s", w.Code, w.Body)
	}
	w = call(h, "PUT", "/ops/v1/products/superwagie/billing-profile", "product-alice", `{"product_id":"superwagie","version":0}`, "")
	if w.Code != 401 {
		t.Fatalf("product changed commercial policy %d", w.Code)
	}
	policy, _ := billing.BillingPreset("superwagie", "prepaid")
	policy.Version = 1
	if e := b.ApplyConfiguredProfiles(ctx, []billing.BillingProfile{policy}); e != nil {
		t.Fatal(e)
	}
	w = call(h, "GET", "/v1/accounts", "product-alice", "", "")
	var as []accounts.Account
	json.Unmarshal(w.Body.Bytes(), &as)
	w = call(h, "GET", "/v1/accounts/"+as[0].ID+"/resources", "product-alice", "", "")
	var resource struct {
		BillingProfile billing.BillingProfile `json:"billing_profile"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resource) != nil || resource.BillingProfile.Usage.DefaultFundingMode != "prepaid_cash" {
		t.Fatalf("resources %d %s", w.Code, w.Body)
	}
}

func TestOperatorCannotAccidentallyDisableBillingWithPartialProfile(t *testing.T) {
	f := newBillingModesFixture(t)
	f.request("PUT", "/ops/v1/products/superwagie/billing-profile", "alice", map[string]any{"product_id": "superwagie", "version": 0}, "", 400, nil)
	var current billing.BillingProfile
	f.request("GET", "/v1/products/superwagie/billing-profile", "product-alice", nil, "", 200, &current)
	if current.Version != 0 || !current.Subscription.Enabled || current.Usage.DefaultFundingMode != billing.FundingCredits {
		t.Fatalf("partial write changed policy %+v", current)
	}
}
