package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/modelaccess"
	"unihub/internal/platformadmin"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestManagedModelsProductProjectionAndNativeAdmission(t *testing.T) {
	ctx := t.Context()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(pool)
	actor, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice", Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := a.ListAccounts(ctx, actor.ID)
	if err != nil || len(as) != 1 {
		t.Fatalf("personal account: %v %v", as, err)
	}
	account := as[0].ID
	if _, err := pool.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,'["finance_admin"]','test',$1)`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO billing_subscriptions(id,account_id,product_id,snapshot) VALUES('managed-model-membership',$1,'superwagie',jsonb_build_object('id','managed-model-membership','status','active','paid_through',now()+interval '1 hour'))`, account); err != nil {
		t.Fatal(err)
	}
	b := billing.New(pool, a, noLedger{})
	b.ConfigureOperators([]string{actor.ID})
	b.ConfigureTrustedServices([]string{"llmrouter"})
	access := modelaccess.New(pool, a, b)
	access.ConfigureProviderCatalog(scopedWalletCatalog{})
	registry := productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1,
		DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}}}
	runtime := RuntimeService{Token: "isolated-superwagie-runtime-test-token-1234", ProductID: "superwagie", InstanceID: "default", AllowedOps: []string{productModelProofOperation}}
	signingKey := []byte("isolated-managed-model-policy-signing-key")
	h := New(verifier{}, a, wallet.New(pool, a, noLedger{}), Options{
		Billing: b, ModelAccess: access, PlatformAdmin: platformadmin.New(pool, nil),
		ServiceToken: billingModesServiceToken, OperatorVerifier: verifier{},
		ProductUsers: productusers.New(pool, registry, "https://hub.test", nil), ProductModelSigningKey: signingKey,
		ProductRuntime: map[string]RuntimeService{"sw": runtime}, ProductVerifiers: map[string]auth.Verifier{"sw": scopedWalletProductVerifier{}},
		ProductAccess: map[string]products.Client{"sw": {ClientID: "sw", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"catalog:read", "access:read"}}},
	})
	request := func(method, path, token string, scoped bool, body, out any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if scoped {
			r.Header.Set(productWalletHeader, billing.ProductCreditScopeV1)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		if out != nil && json.Unmarshal(w.Body.Bytes(), out) != nil {
			t.Fatalf("invalid response: %s", w.Body)
		}
		return w
	}
	const public = "/v1/products/superwagie/managed-models"
	const profilePath = "/v1/products/superwagie/billing-profile"
	const verify = "/internal/v1/model-credentials/verify"
	const reserve = "/internal/v1/calls/native-reserve"
	assertPublic := func(path string, version int, policy any) {
		t.Helper()
		var got map[string]any
		request("GET", path, "", false, nil, &got, http.StatusOK)
		productID := strings.Split(path, "/")[3]
		if want := map[string]any{"product_id": productID, "version": float64(version), "managed_models": policy}; !reflect.DeepEqual(got, want) {
			t.Fatalf("public policy = %v, want %v", got, want)
		}
	}
	assertPublic(public, 0, nil)
	assertPublic("/v1/products/agentwiki/managed-models", 0, nil)
	request("GET", "/v1/products/missing/managed-models", "", false, nil, nil, http.StatusNotFound)
	request("GET", profilePath, "", false, nil, nil, http.StatusUnauthorized)
	legacy := request("GET", profilePath, "product-alice", false, nil, nil, http.StatusOK)
	if strings.Contains(legacy.Body.String(), `"managed_models"`) {
		t.Fatalf("legacy profile has managed models: %s", legacy.Body)
	}
	p, _ := billing.BillingPreset("superwagie", "hybrid")
	p.CreditScope = billing.ProductCreditScopeV1
	p.NativeCreditConversion = &billing.NativeConversion{Version: "managed-model-fixture", Numerator: "1", Denominator: "1", Rounding: "lr-cumulative-ceil-v1"}
	p.Usage.FundingModes = append(p.Usage.FundingModes, billing.FundingPrepaidCash)
	request("PUT", "/ops"+profilePath, "alice", false, p, &p, http.StatusOK)
	newKey := func(funding, scope string) modelaccess.APIKey {
		t.Helper()
		key, err := access.CreateAPIKey(ctx, actor.ID, account, modelaccess.APIKeyRequest{Name: "fixture", OriginProductID: "superwagie", InstanceID: "default", ModelScope: modelaccess.ScopeLLMRouterAvailable, FundingMode: funding, CreditScope: scope})
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	key := newKey(billing.FundingCredits, billing.ProductCreditScopeV1)
	legacyKey := newKey(billing.FundingPrepaidCash, "")
	grant, err := access.VerifyReceiptForInstance(ctx, key.Token, "", "default")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := signProductModelProof(signingKey, runtime.Token, modelPrincipal(grant), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	quote := billing.NativeQuote{PricingVersion: billing.NativeCreditPricingVersion, PricingDigest: strings.Repeat("a", 64), BillingModel: "native-model", WireOperation: "chat.completions.create", RouteID: "0", ChannelID: "1", Group: "fixture", NativeQuotaMax: "0", Conversion: *p.NativeCreditConversion, Asset: billing.CreditAsset, MaxAmount: "0"}
	reserveBody := map[string]any{"credential": key.Token, "instance_id": "default", "task_id": "fixture", "call_id": "before-policy", "request_digest": "digest", "model": "native-model", "wire_operation": quote.WireOperation, "credit_scope": billing.ProductCreditScopeV1, "product_authorization": proof, "quote": quote}
	verifyBody := map[string]any{"token": key.Token, "instance_id": "default", "model": "native-model", "wire_operation": quote.WireOperation, "product_authorization": proof}
	service := billingModesServiceToken
	request("POST", verify, service, false, verifyBody, nil, http.StatusOK)
	request("POST", reserve, service, false, reserveBody, nil, http.StatusOK)

	p.Usage.ManagedModels = billing.ManagedModels{"chat": {"deepseek-flash", "fixture-fallback"}, "vision": {}}
	adminPath := "/admin/v1/billing-profiles/superwagie"
	edit := map[string]any{"profile": p, "version": p.Version, "reason": "managed model fixture"}
	request("PUT", adminPath, "alice", false, edit, nil, http.StatusOK)
	request("PUT", adminPath, "alice", false, edit, nil, http.StatusConflict)
	var read billing.BillingProfile
	request("GET", profilePath, "product-alice", false, nil, &read, http.StatusOK)
	if read.Version != p.Version+1 || !reflect.DeepEqual(read.Usage.ManagedModels, p.Usage.ManagedModels) {
		t.Fatalf("authenticated policy mismatch: %+v", read)
	}
	assertPublic(public, int(read.Version), map[string]any{"chat": []any{"deepseek-flash", "fixture-fallback"}, "vision": []any{}})

	identityVerify := map[string]string{"token": key.Token, "instance_id": "default", "product_authorization": proof}
	request("POST", verify, service, false, identityVerify, nil, http.StatusOK)

	verifyBody["managed_operation"] = "chat"
	w := request("POST", verify, service, false, verifyBody, nil, http.StatusForbidden)
	if !strings.Contains(w.Body.String(), "managed_model_not_allowed") {
		t.Fatal(w.Body.String())
	}
	verifyBody["model"] = "fixture-fallback"
	request("POST", verify, service, false, verifyBody, nil, http.StatusOK)
	verifyBody["managed_operation"] = "vision"
	request("POST", verify, service, false, verifyBody, nil, http.StatusForbidden)
	verifyBody["managed_operation"] = ""
	request("POST", verify, service, false, verifyBody, nil, http.StatusForbidden)
	verifyBody["token"], verifyBody["model"], verifyBody["product_authorization"] = legacyKey.Token, "native-model", ""
	request("POST", verify, service, false, verifyBody, nil, http.StatusOK)
	verifyBody["token"], verifyBody["product_authorization"], verifyBody["purpose"] = key.Token, proof, "receipt"
	request("POST", verify, service, false, verifyBody, nil, http.StatusOK)

	reserveBody["managed_operation"] = "chat"
	request("POST", reserve, service, false, reserveBody, nil, http.StatusOK)
	reserveBody["request_digest"] = "different-digest"
	request("POST", reserve, service, false, reserveBody, nil, http.StatusConflict)
	reserveBody["request_digest"] = "digest"
	settle := billing.NativeSettlementRequest{PricingDigest: quote.PricingDigest, NativeQuota: "0", Amount: "0"}
	request("POST", "/internal/v1/calls/before-policy/native-settle", service, false, settle, nil, http.StatusOK)
	var previous billing.Call
	request("GET", "/internal/v1/calls/before-policy", service, false, nil, &previous, http.StatusOK)
	if previous.Status != "SETTLED" || previous.Rate.Model != "native-model" {
		t.Fatalf("historical call changed: %+v", previous)
	}
	reserveBody["call_id"] = "denied-after-policy"
	request("POST", reserve, service, false, reserveBody, nil, http.StatusForbidden)
	request("GET", "/internal/v1/calls/denied-after-policy", service, false, nil, nil, http.StatusNotFound)
	reserveBody["call_id"], reserveBody["model"] = "allowed-after-policy", "deepseek-flash"
	delete(reserveBody, "managed_operation")
	request("POST", reserve, service, false, reserveBody, nil, http.StatusForbidden)
	reserveBody["managed_operation"] = "chat"
	request("POST", reserve, service, false, reserveBody, nil, http.StatusOK)

	for _, scoped := range []bool{true, false} {
		var discovery struct {
			modelaccess.Discovery
			ManagedModels        billing.ManagedModels `json:"managed_models"`
			ManagedModelsVersion int64                 `json:"managed_models_version"`
		}
		request("POST", "/v1/accounts/"+account+"/model-discovery", "product-alice", scoped, map[string]string{"model_scope": modelaccess.ScopeLLMRouterAvailable, "funding_mode": map[bool]string{true: billing.FundingCredits, false: billing.FundingPrepaidCash}[scoped]}, &discovery, http.StatusOK)
		if len(discovery.Models) != 2 || discovery.Models[0].ID != "native-model" {
			t.Fatalf("provider catalog changed: %+v", discovery.Models)
		}
		if scoped && (discovery.ManagedModelsVersion != read.Version || !reflect.DeepEqual(discovery.ManagedModels, read.Usage.ManagedModels)) || !scoped && (discovery.ManagedModels != nil || discovery.ManagedModelsVersion != 0) {
			t.Fatalf("model discovery policy scope = %v: %+v", scoped, discovery)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE products SET enabled=false WHERE id='superwagie'`); err != nil {
		t.Fatal(err)
	}
	request("GET", public, "", false, nil, nil, http.StatusNotFound)
}
