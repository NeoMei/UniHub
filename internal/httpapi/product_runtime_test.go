package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

type runtimeVerifier struct{}

func (runtimeVerifier) Verify(_ context.Context, token string) (auth.Claims, error) {
	if token != "central-alice" {
		return auth.Claims{}, accounts.ErrForbidden
	}
	return auth.Claims{Issuer: "https://runtime.test", Subject: "alice", Name: "Alice", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}

func TestProductRuntimeHTTPKeepsFullNativeKeyAndPriceOutsideCentralCatalog(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	reg := productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Permissions: []string{"product:use", "credit:adjust"}}}}}
	users := productusers.New(p, reg, "https://runtime.test", nil)
	l := ledger.NewFormance("http://127.0.0.1:53068", fmt.Sprintf("runtime-http-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if e := l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, l)
	services := map[string]RuntimeService{"lr": {Token: "test-only-runtime-credential-over-32-bytes", ProductID: "llmrouter", InstanceID: "default", AllowedOps: []string{"bindings:write", "billing:read", "billing:write", "credits:adjust", "budgets:read", "budgets:write"}}, "other": {Token: "test-only-other-instance-credential-32-bytes", ProductID: "llmrouter", InstanceID: "other", AllowedOps: []string{"billing:read"}}}
	services["payments"] = RuntimeService{Token: "test-only-payment-receipt-service-over-32-bytes", ProductID: "llmrouter", InstanceID: "default", AllowedOps: []string{"credits:payment"}}
	h := New(runtimeVerifier{}, a, wallet.New(p, a, l), Options{Billing: b, ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"lr": runtimeVerifier{}}, ProductAccess: map[string]products.Client{"lr": {ClientID: "lr", ProductID: "llmrouter", InstanceID: "default"}}, ProductRuntime: services})
	request := func(method, path, service, actor string, body any, want int, target any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/internal/v1/product-runtime"+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+service)
		if actor != "" {
			r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		if target != nil {
			if e := json.Unmarshal(w.Body.Bytes(), target); e != nil {
				t.Fatal(e)
			}
		}
	}
	token := services["lr"].Token
	var binding productusers.RuntimeBinding
	request("POST", "/bindings", token, "central-alice", map[string]string{"local_user_id": "42", "binding_key": "verified-bind"}, 200, &binding)
	if _, e := p.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,product_role,version) VALUES($1,'llmrouter','default','["member"]',true,100,1)`, binding.UserID); e != nil {
		t.Fatal(e)
	}
	gift := billing.RuntimeAdjustmentRequest{RequestKey: "gift", LocalUserID: "42", AccountID: binding.PersonalAccountID, Amount: "100", Reason: "legacy product grant", Asset: billing.RuntimeAsset, UnitVersion: billing.RuntimeUnitVersion}
	request("POST", "/credit-adjustments", token, "central-alice", gift, 200, nil)
	in := billing.RuntimeReserveRequest{CallID: "native-call", LocalUserID: "42", LocalKeyID: "unchanged-native-key-id", AccountID: binding.PersonalAccountID, RequestDigest: "request", Model: "native-no-central-rate", Operation: "native-no-operation-enumeration", Asset: billing.RuntimeAsset, UnitVersion: billing.RuntimeUnitVersion, PriceVersion: "original-lr-tier-expression", PricingDigest: "price", Amount: "60"}
	request("POST", "/calls/reserve", token, "", in, 200, nil)
	request("POST", "/calls/native-call/settle", token, "", billing.RuntimeSettlementRequest{Amount: "37", PricingDigest: "price", Usage: []byte(`{"requests":1}`)}, 200, nil)
	request("POST", "/calls/native-call/topup", token, "", billing.RuntimeTopUpRequest{RequestKey: "unauthorized-obligation", Amount: "1", AcceptedCall: true}, 403, nil)
	child := in
	child.CallID = "unauthorized-child"
	child.ParentCallID = "native-call"
	request("POST", "/calls/reserve", token, "", child, 403, nil)
	request("POST", "/calls/native-call/settle", token, "", billing.RuntimeSettlementRequest{Amount: "37", PricingDigest: "price", Usage: []byte(`{"requests":1}`)}, 200, nil)
	request("GET", "/calls/native-call", services["other"].Token, "", nil, 404, nil)
	request("POST", "/credit-adjustments", services["other"].Token, "central-alice", gift, 403, nil)
	var balance billing.RuntimeBalance
	request("GET", "/users/42/balance?account_id="+binding.PersonalAccountID, token, "", nil, 200, &balance)
	if balance.Available != "63" || balance.Held != "0" || balance.Consumed != "37" {
		t.Fatalf("HTTP ledger did not settle exactly once %+v", balance)
	}
	award := billing.RuntimeAwardRequest{ReceiptID: "confirmed-order", SourceKind: "payment", ReceiptDigest: "provider-confirmed-hash", Reason: "payment confirmed by trusted LR callback", LocalUserID: "42", AccountID: binding.PersonalAccountID, Amount: "25", Asset: billing.RuntimeAsset, UnitVersion: billing.RuntimeUnitVersion}
	request("POST", "/awards", token, "central-alice", award, 403, nil)
	request("POST", "/awards", services["payments"].Token, "", award, 200, nil)
	request("POST", "/awards", services["payments"].Token, "", award, 200, nil)
	award.SourceKind = "signup"
	request("POST", "/awards", services["payments"].Token, "", award, 403, nil)
	request("GET", "/users/42/balance?account_id="+binding.PersonalAccountID, token, "", nil, 200, &balance)
	if balance.Available != "88" {
		t.Fatalf("verified receipt was credited twice %+v", balance)
	}
}
func TestProductRuntimeServiceCannotChooseAnotherProductOrCentralSubject(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	users := productusers.New(p, productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Permissions: []string{"product:use"}}}}}, "https://runtime.test", nil)
	h := New(runtimeVerifier{}, a, wallet.New(p, a, noLedger{}), Options{ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"lr": runtimeVerifier{}}, ProductAccess: map[string]products.Client{"lr": {ClientID: "lr", ProductID: "llmrouter", InstanceID: "default"}}, ProductRuntime: map[string]RuntimeService{"lr": {Token: "test-only-runtime-credential-over-32-bytes", ProductID: "llmrouter", InstanceID: "default", AllowedOps: []string{"bindings:write", "access:read"}}}})
	request := func(body, service, actor string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/internal/v1/product-runtime/bindings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if service != "" {
			r.Header.Set("Authorization", "Bearer "+service)
		}
		if actor != "" {
			r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(`{"local_user_id":"42","binding_key":"link"}`, "", "central-alice"); w.Code != 401 {
		t.Fatalf("missing service %d %s", w.Code, w.Body)
	}
	if w := request(`{"local_user_id":"42","binding_key":"link","subject":"attacker"}`, "test-only-runtime-credential-over-32-bytes", "central-alice"); w.Code != 400 {
		t.Fatalf("supplied subject %d %s", w.Code, w.Body)
	}
	w := request(`{"local_user_id":"42","binding_key":"link"}`, "test-only-runtime-credential-over-32-bytes", "central-alice")
	if w.Code != 200 {
		t.Fatalf("binding %d %s", w.Code, w.Body)
	}
	var binding productusers.RuntimeBinding
	if e := json.Unmarshal(w.Body.Bytes(), &binding); e != nil {
		t.Fatal(e)
	}
	if binding.Subject != "alice" || binding.ProductID != "llmrouter" || binding.LocalUserID != "42" {
		t.Fatalf("untrusted binding %+v", binding)
	}
	if w = request(`{"local_user_id":"42","binding_key":"link"}`, "test-only-runtime-credential-over-32-bytes", ""); w.Code != 401 {
		t.Fatalf("missing actor %d", w.Code)
	}
}
