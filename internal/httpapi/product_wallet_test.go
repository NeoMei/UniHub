package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

func TestProductWalletRequiresNegotiatedDownstreamProduct(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/accounts/example/product-wallet", nil)
	request.Header.Set(productWalletHeader, "product_v1")
	if productWalletRequested(request) {
		t.Fatal("an unregistered client opted into the product wallet")
	}
	ctx := context.WithValue(request.Context(), productIdentityKey{}, "superwagie")
	ctx = context.WithValue(ctx, productPolicyKey{}, products.Client{
		ClientID: "sw", ProductID: "superwagie", BillingProducts: []string{"superwagie", "agentwiki", "llmrouter"},
	})
	request = request.WithContext(ctx)
	if !productWalletRequested(request) || !sharedWalletProduct(request, "agentwiki") {
		t.Fatal("a registered downstream wallet could not show an authorized product")
	}
	if sharedWalletProduct(request, "llmrouter") || sharedWalletProduct(request, "other_product") {
		t.Fatal("an upstream or out-of-scope product appeared in the shared wallet")
	}
	request.Header.Add(productWalletHeader, "product_v1")
	if productWalletRequested(request) {
		t.Fatal("an ambiguous capability header opted into the product wallet")
	}
}

type scopedWalletCatalog struct{}

func (scopedWalletCatalog) Discover(ctx context.Context, request modelaccess.ProviderCatalogRequest) (modelaccess.ProviderCatalogResponse, error) {
	out, err := (creditAcceptanceCatalog{}).Discover(ctx, request)
	out.CreditScopes = []string{billing.ProductCreditScopeV1}
	return out, err
}

type scopedWalletProductVerifier struct{}

func (scopedWalletProductVerifier) Verify(ctx context.Context, raw string) (auth.Claims, error) {
	claims, err := (anyProductVerifier{}).Verify(ctx, raw)
	claims.ExpiresAt = time.Now().Add(time.Hour).Unix()
	return claims, err
}

func TestNegotiatedProductWalletCheckoutAndTrustedSettlement(t *testing.T) {
	gateway := os.Getenv("UNIHUB_PRODUCT_CREDIT_GATEWAY_PATH")
	var binary string
	if gateway != "" {
		if !filepath.IsAbs(gateway) {
			t.Fatal("the gateway checkout must be an absolute path")
		}
		binary = filepath.Join(t.TempDir(), "gateway.test")
		build := exec.Command("go", "test", "-c", "-o", binary, "./router")
		build.Dir = gateway
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("gateway fixture build: %v\n%s", err, output)
		}
	}
	f := newBillingModesFixture(t, scopedWalletCatalog{})
	ctx := context.Background()
	for _, product := range []string{"superwagie", "agentwiki"} {
		profile, err := f.billing.Profile(ctx, product)
		if err != nil {
			t.Fatal(err)
		}
		profile.Subscription.Enabled = true
		profile.IncludedCredits.Enabled = true
		profile.Addons.Enabled = true
		profile.Usage = billing.UsagePolicy{
			Enabled: true, RequireSubscriptionForCredits: true,
			DefaultFundingMode: billing.FundingCredits, FundingModes: []string{billing.FundingCredits},
		}
		if product == "superwagie" {
			legacyConversion := &billing.NativeConversion{Version: "http-legacy-conversion", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}
			profile.NativeCreditConversion = legacyConversion
			profile, err = f.billing.SaveProfile(ctx, f.actor.ID, product, profile)
			if err != nil {
				t.Fatal(err)
			}
		}
		profile.CreditScope = billing.ProductCreditScopeV1
		profile.NativeCreditConversion = &billing.NativeConversion{
			Version: "http-scope-fixture", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1",
		}
		if _, err = f.billing.SaveProfile(ctx, f.actor.ID, product, profile); err != nil {
			t.Fatal(err)
		}
		offer := billing.Offer{ID: product + "-monthly", ProductID: product, Name: product + " monthly",
			AccountKind: "personal", Kind: "membership", Period: "month", Price: "2000000",
			MonthlyCredit: "10000000", CreditScope: billing.ProductCreditScopeV1,
			ServiceAmount: "1000000", ComputeAmount: "1000000",
		}
		if _, err = f.billing.SaveOffer(ctx, f.actor.ID, offer); err != nil {
			t.Fatal(err)
		}
		if _, err = f.billing.PublishOffer(ctx, f.actor.ID, offer.ID); err != nil {
			t.Fatal(err)
		}
	}
	f.recharge("", "6000000", "scoped-wallet-balance")
	f.billing.ConfigureCheckoutReadiness(func(ctx context.Context, _, _, product string) (billing.NativeCreditReadiness, error) {
		return f.billing.NativeCreditsReadiness(ctx, product)
	})
	f.billing.ConfigureScopedCheckoutReadiness(func(ctx context.Context, _, _, product, scope string) (billing.NativeCreditReadiness, error) {
		return f.billing.NativeCreditsReadinessForScopeOn(ctx, f.pool, product, scope)
	})
	if _, err := f.billing.Purchase(ctx, f.actor.ID, f.account, billing.PurchaseRequest{OfferID: "agentwiki-monthly", Key: "agentwiki-buy"}); err != nil {
		t.Fatal(err)
	}

	a := accounts.New(f.pool)
	modelAccess := modelaccess.New(f.pool, a, f.billing)
	modelAccess.ConfigureProviderCatalog(scopedWalletCatalog{})
	registry := productusers.Registry{
		"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1,
			DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}},
	}
	runtime := RuntimeService{Token: "isolated-superwagie-runtime-test-token-1234", ProductID: "superwagie", InstanceID: "default", AllowedOps: []string{productModelProofOperation}}
	signingKey := []byte("isolated-only-product-wallet-test-signing-key")
	h := New(verifier{}, a, f.wallet, Options{
		Billing: f.billing, ModelAccess: modelAccess, ProductModelSigningKey: signingKey,
		ServiceToken: billingModesServiceToken, ProductRuntime: map[string]RuntimeService{"sw": runtime},
		ProductUsers:     productusers.New(f.pool, registry, "https://hub.test", nil),
		ProductVerifiers: map[string]auth.Verifier{"sw": scopedWalletProductVerifier{}},
		ProductAccess: map[string]products.Client{"sw": {
			ClientID: "sw", ProductID: "superwagie", InstanceID: "default",
			BillingProducts: []string{"superwagie", "agentwiki", "llmrouter"},
			Scopes:          []string{"accounts:read", "catalog:read", "credits:read", "wallets:read", "orders:write", "model-credentials:write", "settings:write"},
		}},
	})
	do := func(method, path, token, key string, negotiated bool, payload, out any, want int) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		if negotiated {
			r.Header.Set(productWalletHeader, billing.ProductCreditScopeV1)
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if path == "/internal/v1/product-runtime/model-proofs" && token == runtime.Token {
			r.Header.Set("X-UniHub-Actor-Authorization", "Bearer product-alice")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got HTTP %d, want %d", method, path, w.Code, want)
		}
		if out != nil && json.Unmarshal(w.Body.Bytes(), out) != nil {
			t.Fatalf("%s %s returned an invalid response", method, path)
		}
	}
	base := "/v1/accounts/" + f.account
	var flags struct {
		Enabled bool                          `json:"product_credit_wallet"`
		Native  billing.NativeCreditReadiness `json:"native_credits"`
	}
	do("GET", "/v1/capabilities", "product-alice", "", false, nil, &flags, 200)
	if flags.Enabled || flags.Native.CreditScope != "" || flags.Native.Conversion == nil || flags.Native.Conversion.Numerator != "3" {
		t.Fatal("legacy client received a product-credit price conversion")
	}
	do("GET", "/v1/capabilities", "product-alice", "", true, nil, &flags, 200)
	if !flags.Enabled || flags.Native.CreditScope != billing.ProductCreditScopeV1 || flags.Native.Conversion == nil || flags.Native.Conversion.Numerator != "1" {
		t.Fatal("product client did not receive the negotiated microcredit conversion")
	}
	var catalog []billing.CatalogOffer
	do("GET", "/v1/catalog", "product-alice", "", false, nil, &catalog, 200)
	if len(catalog) != 0 {
		t.Fatal("legacy product read saw new offers")
	}
	payload := map[string]string{"offer_id": "superwagie-monthly"}
	do("POST", base+"/orders", "product-alice", "sw-buy", false, payload, nil, 403)
	do("POST", base+"/orders", "product-alice", "sw-buy", true, payload, nil, 202)
	do("POST", base+"/orders", "product-alice", "sw-buy", true, payload, nil, 202)
	var summary struct {
		Cash     map[string]string      `json:"cash"`
		Products []productWalletProduct `json:"products"`
	}
	do("GET", base+"/product-wallet", "product-alice", "", true, nil, &summary, 200)
	if summary.Cash["available"] != "2000000" || len(summary.Products) != 2 || summary.Products[0].ProductID != "agentwiki" {
		t.Fatal("shared wallet did not show the two downstream orders and one cash balance")
	}
	var key modelaccess.APIKey
	request := modelaccess.APIKeyRequest{Name: "scoped test Key", ModelScope: modelaccess.ScopeLLMRouterAvailable,
		FundingMode: billing.FundingCredits, CreditScope: billing.ProductCreditScopeV1}
	do("POST", base+"/api-keys", "product-alice", "", true, request, &key, 201)
	quote := billing.NativeQuote{PricingVersion: billing.NativeCreditPricingVersion, PricingDigest: strings.Repeat("a", 64),
		BillingModel: "native-model", WireOperation: "chat.completions.create", RouteID: "0", ChannelID: "1", Group: "default",
		NativeQuotaMax: "200", Conversion: billing.NativeConversion{Version: "http-scope-fixture", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}, Asset: billing.CreditAsset, MaxAmount: "200"}
	reserve := map[string]any{"credential": key.Token, "instance_id": "default", "task_id": "scope-task",
		"call_id": "scoped-http-call", "request_digest": "scoped-http-digest", "model": "native-model",
		"wire_operation": quote.WireOperation, "credit_scope": billing.ProductCreditScopeV1, "quote": quote}
	do("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, "", false, reserve, nil, 403)
	var issuedProof struct {
		Token string `json:"token"`
	}
	do("POST", "/internal/v1/product-runtime/model-proofs", "product-alice", "", false, map[string]string{"token": key.Token}, nil, 401)
	do("POST", "/internal/v1/product-runtime/model-proofs", runtime.Token, "", false, map[string]string{"token": key.Token}, &issuedProof, 201)
	reserve["product_authorization"] = issuedProof.Token
	var call billing.Call
	do("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, "", false, reserve, &call, 200)
	do("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, "", false, reserve, nil, 200)
	if call.FundingProductID != "superwagie" || len(call.Allocations) != 1 {
		t.Fatal("the reserve did not select the originating product")
	}
	settle := billing.NativeSettlementRequest{PricingDigest: quote.PricingDigest, NativeQuota: "100", Amount: "100"}
	do("POST", "/internal/v1/calls/scoped-http-call/native-settle", billingModesServiceToken, "", false, settle, nil, 200)
	do("POST", "/internal/v1/calls/scoped-http-call/native-settle", billingModesServiceToken, "", false, settle, nil, 200)
	sw, err := f.billing.ProductCreditWallet(ctx, f.actor.ID, f.account, "superwagie")
	if err != nil || sw.Available != "9999900" {
		t.Fatal("same-call replay consumed SuperWagie credits more than once")
	}
	aw, err := f.billing.ProductCreditWallet(ctx, f.actor.ID, f.account, "agentwiki")
	if err != nil || aw.Available != "10000000" {
		t.Fatal("SuperWagie call affected AgentWiki credits")
	}
	if binary != "" {
		server := httptest.NewServer(h)
		t.Cleanup(server.Close)
		run := exec.Command(binary, "-test.run=^TestProductCreditRelayFreezesPublicPriceAndDoesNotReplayUnknownProvider$", "-test.count=1")
		run.Dir = gateway
		run.Env = append(os.Environ(),
			"PRODUCT_CREDIT_INTEGRATION_ORIGIN="+server.URL,
			"PRODUCT_CREDIT_INTEGRATION_SERVICE_TOKEN="+billingModesServiceToken,
			"PRODUCT_CREDIT_INTEGRATION_KEY="+key.Token,
			"PRODUCT_CREDIT_INTEGRATION_PROOF="+issuedProof.Token,
		)
		if output, err := run.CombinedOutput(); err != nil {
			t.Fatalf("isolated gateway fixture: %v\n%s", err, output)
		}
		sw, err = f.billing.ProductCreditWallet(ctx, f.actor.ID, f.account, "superwagie")
		if err != nil || sw.Available != "9998850" {
			t.Fatal("the actual router did not settle exactly 1050 product microcredits")
		}
		aw, err = f.billing.ProductCreditWallet(ctx, f.actor.ID, f.account, "agentwiki")
		if err != nil || aw.Available != "10000000" {
			t.Fatal("actual SuperWagie router call affected AgentWiki credits")
		}
	}
	if _, err = f.pool.Exec(ctx, `UPDATE billing_subscriptions SET snapshot=jsonb_set(snapshot,'{paid_through}',to_jsonb($3::text))
		WHERE account_id=$1 AND product_id=$2`, f.account, "superwagie", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	do("POST", base+"/api-keys", "product-alice", "", true, request, nil, 403)
	do("POST", "/internal/v1/product-runtime/model-proofs", runtime.Token, "", false, map[string]string{"token": key.Token}, &issuedProof, 201)
	reserve["product_authorization"] = issuedProof.Token
	var replay billing.Call
	do("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, "", false, reserve, &replay, 200)
	if replay.CallID != call.CallID || replay.Status != "SETTLED" {
		t.Fatal("membership lapse masked an existing settled call")
	}
	reserve["call_id"] = "scoped-http-after-lapse"
	var refused map[string]string
	do("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, "", false, reserve, &refused, 403)
	if refused["error"] != "product_membership_expired_no_hold" {
		t.Fatal("new admission did not give a definitive membership refusal")
	}
}
