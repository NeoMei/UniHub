package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

func TestNativeCreditKeyOptInAndCatalogPreservesLegacyCash(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://points.test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, nil)
	b.ConfigureOperators([]string{u.ID})
	profiles, e := billing.ParseBillingProfiles([]byte(`[{"product_id":"superwagie","version":1,"subscription":{"enabled":false},"included_credits":{"enabled":false},"addons":{"enabled":false},"usage":{"enabled":true,"funding_modes":["credits","prepaid_cash"],"default_funding_mode":"credits"},"native_credit_conversion":{"version":"test-points-v1","numerator":"3","denominator":"2","rounding":"lr-cumulative-ceil-v1"}}]`))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.ApplyConfiguredProfiles(ctx, profiles); e != nil {
		t.Fatal(e)
	}
	s := New(p, a, b)
	s.ConfigureProductUsers(productusers.New(p, productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}}}, "https://points.test", nil))
	s.ConfigureProviderCatalog(nativeCreditCatalog{})
	request := APIKeyRequest{Name: "Points", OriginProductID: "superwagie", InstanceID: "default", ModelScope: ScopeLLMRouterAvailable, FundingMode: billing.FundingCredits}
	key, e := s.CreateAPIKey(ctx, u.ID, as[0].ID, request)
	if e != nil {
		t.Fatal(e)
	}
	if key.Asset != billing.CreditAsset || key.PricingSource != "llmrouter_native_credit" {
		t.Fatalf("bad key %+v", key)
	}
	claim, e := s.VerifyForInstance(ctx, key.Token, "model", "default")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(claim)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if string(fields["native_credit_conversion"]) == "" {
		t.Fatalf("missing tariff %s", raw)
	}
	discovery, e := s.DiscoverAPIKey(ctx, key.Token, "superwagie", "default")
	if e != nil || discovery.PricingSource != "llmrouter_native_credit" || len(discovery.Models) != 1 {
		t.Fatalf("catalog %+v %v", discovery, e)
	}
	request.FundingMode = ""
	old, e := s.CreateAPIKey(ctx, u.ID, as[0].ID, request)
	if e != nil || old.Asset != billing.CashAsset || old.PricingSource != PricingLLMRouterNative {
		t.Fatalf("implicit cash changed %+v %v", old, e)
	}
	// Scope upgrade never converts an already-issued points/cash asset.
	if _, e = s.UpgradeAPIKeyScope(ctx, u.ID, as[0].ID, key.ID, APIKeyScopeRequest{OriginProductID: "superwagie", InstanceID: "default", ModelScope: ScopeLLMRouterAvailable}); e != nil {
		t.Fatal(e)
	}
	profiles[0].Version = 2
	profiles[0].NativeCreditConversion = nil
	if e = b.ApplyConfiguredProfiles(ctx, profiles); e != nil {
		t.Fatal(e)
	}
	if _, e = s.VerifyForInstance(ctx, key.Token, "model", "default"); !errors.Is(e, ErrCredential) {
		t.Fatalf("unconfigured points admitted %v", e)
	}
	if _, e = s.VerifyForInstance(ctx, old.Token, "model", "default"); e != nil {
		t.Fatalf("cash disabled %v", e)
	}
}

type nativeCreditCatalog struct{}

func (nativeCreditCatalog) Discover(_ context.Context, r ProviderCatalogRequest) (ProviderCatalogResponse, error) {
	return ProviderCatalogResponse{ProviderCatalogRequest: r, NativeCreditProtocol: billing.NativeCreditPricingVersion, SchemaVersion: 1, CatalogVersion: "test", ModelScope: ScopeLLMRouterAvailable, PricingSource: PricingLLMRouterNative, Models: []Model{{ID: "model"}}}, nil
}

func TestNativeCreditProviderProtocolReadinessIsExplicit(t *testing.T) {
	// Old provider catalogs remain cash-compatible but cannot advertise CREDIT.
	for _, protocol := range []string{"", billing.NativeCreditPricingVersion} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ProviderCatalogRequest
				if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
					t.Error(e)
					return
				}
				if req.Purpose != "native_credit_readiness" || req.ActorID != "" || req.AccountID != "" || req.InstanceID != "" {
					t.Errorf("not a runtime-only request %+v", req)
				}
				_ = json.NewEncoder(w).Encode(ProviderCatalogResponse{ProviderCatalogRequest: req, SchemaVersion: 1, CatalogVersion: "protocol-v1", ModelScope: ScopeLLMRouterAvailable, PricingSource: PricingLLMRouterNative, NativeCreditProtocol: protocol, Models: []Model{}})
			}))
			defer server.Close()
			catalog, e := NewProviderCatalog(server.URL+"/internal/v1/provider-catalog", "test-runtime-token-at-least-32-bytes")
			if e != nil {
				t.Fatal(e)
			}
			got, e := catalog.(*providerCatalogClient).NativeCreditProtocol(context.Background(), "superwagie")
			if e != nil || got != protocol {
				t.Fatalf("protocol %s %v", got, e)
			}
		})
	}
}

func TestNativeCreditConfiguredPolicyRequiresProviderSupport(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	b := billing.New(p, a, nil)
	profiles, e := billing.ParseBillingProfiles([]byte(`[{"product_id":"superwagie","version":1,"preset":"hybrid","native_credit_conversion":{"version":"test-points-v1","numerator":"3","denominator":"2","rounding":"lr-cumulative-ceil-v1"}}]`))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.ApplyConfiguredProfiles(ctx, profiles); e != nil {
		t.Fatal(e)
	}
	s := New(p, a, b)
	ready, e := s.NativeCreditsRuntimeReadiness(ctx, "superwagie")
	if e != nil || ready.Ready || ready.Reason != "native_credit_provider_unavailable" {
		t.Fatalf("unconfigured runtime %+v %v", ready, e)
	}
	for _, protocol := range []string{"", billing.NativeCreditPricingVersion} {
		s.ConfigureProviderCatalog(creditReadinessProvider{protocol: protocol})
		ready, e = s.NativeCreditsRuntimeReadiness(ctx, "superwagie")
		if e != nil || ready.Ready != (protocol == billing.NativeCreditPricingVersion) {
			t.Fatalf("runtime %+v %v", ready, e)
		}
	}
}

type creditReadinessProvider struct{ protocol string }

func (c creditReadinessProvider) Discover(ctx context.Context, r ProviderCatalogRequest) (ProviderCatalogResponse, error) {
	return nativeCreditCatalog{}.Discover(ctx, r)
}
func (c creditReadinessProvider) NativeCreditProtocol(context.Context, string) (string, error) {
	return c.protocol, nil
}
