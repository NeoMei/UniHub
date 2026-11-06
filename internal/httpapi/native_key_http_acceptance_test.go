package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"unihub/internal/billing"
	"unihub/internal/modelaccess"
	"unihub/internal/wallet"
)

// This uses the real UniHub HTTP handler, migration, accounts, billing services
// and a test-owned Formance ledger. Only identity verification and the remote
// provider-catalog response use controlled fixtures. No provider inference runs.
func TestNativeManagedKeyHTTPAcceptance(t *testing.T) {
	ctx := context.Background()
	var catalogReads atomic.Int32
	const catalogToken = "native-http-catalog-only-credential-over-32-bytes"
	catalogServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/provider-catalog" || r.Header.Get("Authorization") != "Bearer "+catalogToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var scope modelaccess.ProviderCatalogRequest
		if err := json.NewDecoder(r.Body).Decode(&scope); err != nil || scope.OriginProductID != "superwagie" || scope.InstanceID != "default" || scope.ActorID == "" || scope.AccountID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		catalogReads.Add(1)
		_ = json.NewEncoder(w).Encode(modelaccess.ProviderCatalogResponse{
			ProviderCatalogRequest: scope, SchemaVersion: 1, CatalogVersion: "native-http-fixture-v1",
			ModelScope: modelaccess.ScopeLLMRouterAvailable, PricingSource: modelaccess.PricingLLMRouterNative,
			Models: []modelaccess.Model{{ID: "new-model", Name: "Native model", Operations: []string{"chat"}, InputModalities: []string{"text"}, OutputModalities: []string{"text"}, Probe: true,
				CapabilityMetadata: json.RawMessage(`{"id":"new-model","operations":[{"id":"chat.completions.create","available":true,"executable":true}]}`)}},
		})
	}))
	defer catalogServer.Close()
	catalog, err := modelaccess.NewProviderCatalog(catalogServer.URL+"/internal/v1/provider-catalog", catalogToken)
	if err != nil {
		t.Fatal(err)
	}
	f := newBillingModesFixture(t, catalog)
	f.profile("prepaid")
	f.rate("native-http-selected-rate", billing.CashAsset, "1")
	f.recharge("llmrouter", "1000000", "native-http-product-funding")
	f.recharge("", "2000000", "native-http-unified-funding")
	path := "/v1/accounts/" + f.account
	var key modelaccess.APIKey
	f.request("POST", path+"/api-keys", "product-alice", modelaccess.APIKeyRequest{Name: "Existing HTTP Key", Models: []string{"acceptance-model"}, MaxTotal: "60", MaxPerCall: "50"}, "", http.StatusCreated, &key)
	if key.ModelScope != modelaccess.ScopeSelected || key.CredentialVersion != 1 {
		t.Fatal("original finite-scope Key was not created")
	}
	var originalHash string
	if err = f.pool.QueryRow(ctx, `SELECT token_hash FROM model_credentials WHERE id=$1`, key.ID).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	var legacy billing.Call
	legacyBody := map[string]string{"credential": key.Token, "instance_id": "default", "task_id": "legacy-task", "call_id": "native-http-legacy", "request_digest": "native-http-legacy-request", "rate_id": "native-http-selected-rate", "amount": "10"}
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, legacyBody, "", http.StatusOK, &legacy)
	var upgraded modelaccess.APIKey
	f.request("POST", path+"/api-keys/"+key.ID+"/scope", "product-alice", modelaccess.APIKeyScopeRequest{ModelScope: modelaccess.ScopeLLMRouterAvailable}, "", http.StatusOK, &upgraded)
	var upgradedHash string
	if err = f.pool.QueryRow(ctx, `SELECT token_hash FROM model_credentials WHERE id=$1`, key.ID).Scan(&upgradedHash); err != nil {
		t.Fatal(err)
	}
	if upgraded.ID != key.ID || upgradedHash != originalHash || upgraded.ActorID != key.ActorID || upgraded.AccountID != key.AccountID || upgraded.Token != "" || upgraded.MaxTotal != "60" || upgraded.MaxPerCall != "50" || upgraded.CredentialVersion != 2 {
		t.Fatal("in-place upgrade changed the original Key or its financial contract")
	}
	t.Log("same_key=true same_token_hash=true credential_version=2 original_budget=60/50")

	// A tariff reservation accepted before the upgrade retains its old tariff.
	f.request("POST", "/internal/v1/calls/"+legacy.CallID+"/topup", billingModesServiceToken, map[string]string{"amount": "5"}, "legacy-after-upgrade", http.StatusOK, &legacy)
	legacy = f.settle(legacy.CallID, "2")
	if legacy.Held != "15" || legacy.Charged != "2" || legacy.NativePricing != nil || legacy.Rate.ID != "native-http-selected-rate" {
		t.Fatal("scope upgrade changed an already accepted tariff call")
	}

	var discovery modelaccess.Discovery
	f.request("POST", path+"/model-discovery", "product-alice", map[string]string{"model_scope": modelaccess.ScopeLLMRouterAvailable}, "", http.StatusOK, &discovery)
	var models modelListResponse
	f.request("GET", "/v1/models", key.Token, nil, "", http.StatusOK, &models)
	if catalogReads.Load() != 2 || discovery.ModelScope != modelaccess.ScopeLLMRouterAvailable || discovery.AccountID != key.AccountID || len(models.Data) != 1 || models.Data[0].ID != "new-model" || len(models.Data[0].CapabilityMetadata) == 0 {
		t.Fatal("dynamic catalog did not reach the authenticated provider endpoint")
	}
	var grant modelaccess.Claims
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, map[string]string{"token": key.Token, "instance_id": "default", "model": "new-model", "wire_operation": "chat.completions.create"}, "", http.StatusOK, &grant)
	if grant.ID != key.ID || grant.ModelScope != modelaccess.ScopeLLMRouterAvailable || grant.PricingSource != modelaccess.PricingLLMRouterNative || grant.CredentialVersion != 2 {
		t.Fatal("native introspection did not retain the original Key")
	}
	var nativeRates int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM billing_rates WHERE snapshot->>'model'='new-model'`).Scan(&nativeRates); err != nil || nativeRates != 0 {
		t.Fatalf("native test depends on a duplicate central tariff: %d, %v", nativeRates, err)
	}
	t.Log("catalog_reads=2 new_model_central_rates=0 native_grant=true")

	// Matches LLMRouter's native_pricing_test.go fixture: ceil(10*5/2)=25,
	// then actual quota 3 costs 8. These are test prices, not product prices.
	snapshot := `{"native_price":"existing"}`
	snapshotDigest := sha256.Sum256([]byte(snapshot))
	quote := billing.NativeQuote{PricingVersion: "lr-native-v1", PricingDigest: hex.EncodeToString(snapshotDigest[:]), BillingModel: "billing-model", WireOperation: "chat.completions.create", RouteID: "0", ChannelID: "1", Group: "default", NativeQuotaMax: "10", Conversion: billing.NativeConversion{Version: "lr-quota-usd-cny-v1", Numerator: "5", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"}, Asset: "CNY/6", MaxAmount: "25"}
	reserveBody := func(id string, q billing.NativeQuote) map[string]any {
		return map[string]any{"credential": key.Token, "instance_id": "default", "task_id": "native-http-task", "call_id": id, "request_digest": "digest-" + id, "model": "new-model", "wire_operation": q.WireOperation, "quote": q}
	}
	checkNativeReceipt := func(call billing.Call, id, held string) {
		t.Helper()
		if call.CallID != id || call.CredentialID != key.ID || call.CredentialVersion != 2 || call.ActorID != key.ActorID || call.AccountID != key.AccountID || call.OriginProductID != "superwagie" || call.InstanceID != "default" || call.TaskID != "native-http-task" || call.FundingMode != "prepaid_cash" || call.FundingProductID != "llmrouter" || call.ChargeAsset != "CNY/6" || call.CashHoldAddress != billing.CashHoldAddress(key.AccountID, id) || call.Rate.Model != "new-model" || call.Rate.ID != "" || call.Rate.Asset != "CNY/6" || call.Rate.Semantics != "llmrouter_native" || call.Held != held {
			t.Fatal("native receipt does not match the LLMRouter RemoteCall contract")
		}
	}
	var native billing.Call
	body := reserveBody("native-http-positive", quote)
	f.request("POST", "/internal/v1/calls/native-reserve", "product-alice", body, "", http.StatusUnauthorized, nil)
	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, body, "native-http-positive", http.StatusOK, &native)
	checkNativeReceipt(native, "native-http-positive", "25")
	if native.Status != "RESERVED" || native.NativePricing == nil || !reflect.DeepEqual(*native.NativePricing, quote) {
		t.Fatal("native quote was not frozen in the reserve response")
	}
	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, body, "native-http-positive", http.StatusOK, &native)
	settle := map[string]any{"pricing_digest": quote.PricingDigest, "native_quota": "3", "amount": "8", "usage": map[string]string{"input": "2", "output": "1", "cache_read": "0", "cache_write": "0", "reasoning": "0"}}
	f.request("POST", "/internal/v1/calls/native-http-positive/native-settle", billingModesServiceToken, settle, "native-http-positive", http.StatusOK, &native)
	f.request("POST", "/internal/v1/calls/native-http-positive/native-settle", billingModesServiceToken, settle, "native-http-positive", http.StatusOK, &native)
	if native.Status != "SETTLED" || native.Charged != "8" || native.NativeQuota != "3" || native.PlatformDifference != "0" {
		t.Fatal("native actual charge or idempotent settlement changed")
	}
	f.balance("llmrouter", 999990)
	f.balance("", 2000000)
	t.Log("positive_hold=25 quota=3 charged=8 replay_no_extra_charge=true unified_wallet_unchanged=true")

	zero := quote
	zero.NativeQuotaMax, zero.MaxAmount = "0", "0"
	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, reserveBody("native-http-zero", zero), "native-http-zero", http.StatusOK, &native)
	f.request("POST", "/internal/v1/calls/native-http-zero/native-settle", billingModesServiceToken, map[string]string{"pricing_digest": zero.PricingDigest, "native_quota": "0", "amount": "0"}, "native-http-zero", http.StatusOK, &native)
	var zeroPosts int
	if err = f.pool.QueryRow(ctx, `SELECT COALESCE(sum(jsonb_array_length(command->'postings')),0) FROM billing_commands WHERE effect#>>'{call,call_id}'='native-http-zero'`).Scan(&zeroPosts); err != nil || zeroPosts != 0 || native.Status != "SETTLED" || native.Charged != "0" {
		t.Fatalf("free call did not preserve a zero-posting receipt: posts=%d err=%v", zeroPosts, err)
	}
	t.Log("zero_call=SETTLED zero_ledger_postings=0")

	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, reserveBody("native-http-topup", quote), "native-http-topup", http.StatusOK, &native)
	aboveHold := map[string]string{"pricing_digest": quote.PricingDigest, "native_quota": "12", "amount": "30"}
	f.request("POST", "/internal/v1/calls/native-http-topup/native-settle", billingModesServiceToken, aboveHold, "native-http-topup", http.StatusConflict, nil)
	f.request("POST", "/internal/v1/calls/native-http-topup/topup", billingModesServiceToken, map[string]string{"amount": "5"}, "native-http-topup:native-topup:30", http.StatusOK, &native)
	f.request("POST", "/internal/v1/calls/native-http-topup/native-settle", billingModesServiceToken, aboveHold, "native-http-topup", http.StatusOK, &native)
	if native.Charged != "30" || native.PlatformDifference != "0" {
		t.Fatal("above-held charge was truncated")
	}
	var budgetProblem map[string]string
	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, reserveBody("native-http-over-budget", quote), "native-http-over-budget", http.StatusPaymentRequired, &budgetProblem)
	if len(budgetProblem) != 1 || budgetProblem["error"] != "usage_budget_exceeded" {
		t.Fatal("native budget denial is not a definite no-hold rejection")
	}
	var deniedCalls int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM billing_calls WHERE id='native-http-over-budget'`).Scan(&deniedCalls); err != nil || deniedCalls != 0 {
		t.Fatal("budget denial created a call or charge")
	}
	var deniedCommands int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE effect#>>'{call,call_id}'='native-http-over-budget'`).Scan(&deniedCommands); err != nil || deniedCommands != 0 {
		t.Fatal("budget rejection persisted a financial command")
	}
	f.balance("llmrouter", 999960)
	noHold, err := f.ledger.Balance(ctx, billing.CashHoldAddress(key.AccountID, "native-http-over-budget"), billing.CashAsset)
	if err != nil || noHold != 0 {
		t.Fatal("budget rejection created a ledger hold")
	}
	t.Log("above_hold_pending=true topup=5 final_charge=30 budget_denial=402 usage_budget_exceeded=true denied_call_rows=0")

	beforeRevoke := quote
	beforeRevoke.NativeQuotaMax, beforeRevoke.MaxAmount = "4", "10"
	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, reserveBody("native-http-before-revoke", beforeRevoke), "native-http-before-revoke", http.StatusOK, &native)
	f.request("DELETE", path+"/api-keys/"+key.ID, "product-alice", nil, "", http.StatusNoContent, nil)
	f.request("POST", "/internal/v1/calls/native-reserve", billingModesServiceToken, reserveBody("native-http-after-revoke", zero), "native-http-after-revoke", http.StatusForbidden, nil)
	f.request("GET", "/v1/models", key.Token, nil, "", http.StatusUnauthorized, nil)
	f.request("POST", "/internal/v1/calls/native-http-before-revoke/native-settle", billingModesServiceToken, map[string]string{"pricing_digest": quote.PricingDigest, "native_quota": "2", "amount": "5"}, "native-http-before-revoke", http.StatusOK, &native)
	checkNativeReceipt(native, "native-http-before-revoke", "10")
	f.balance("llmrouter", 999955)
	f.balance("", 2000000)
	revenue, err := f.ledger.Balance(ctx, "commercial:revenue:llmrouter", wallet.Asset)
	if err != nil || revenue != 45 {
		t.Fatalf("revenue=%d err=%v", revenue, err)
	}
	held, used, err := billing.UsageTotalsOn(ctx, f.pool, key.AccountID, "superwagie", "default", billing.CashAsset, key.ID)
	if err != nil || held != "0" || used != "45" {
		t.Fatalf("final Key usage=%s/%s err=%v", held, used, err)
	}
	t.Log("revoked_new_reserve=403 revoked_models=401 accepted_call_settled=true final_key_held=0 final_key_used=45 product_wallet=999955 unified_wallet=2000000 revenue=45")
}
