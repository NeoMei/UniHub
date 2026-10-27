package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
	"unihub/internal/products"
	"unihub/internal/productusers"
)

type managedGatewayProductVerifier struct{}

func (managedGatewayProductVerifier) Verify(ctx context.Context, raw string) (auth.Claims, error) {
	if raw != "managed-product-alice" {
		return auth.Claims{}, fmt.Errorf("invalid fixture product session")
	}
	claims, err := (verifier{}).Verify(ctx, "alice")
	claims.ExpiresAt = time.Now().Add(time.Hour).Unix()
	return claims, err
}

// Actual production handlers over TCP with isolated real PostgreSQL/Formance.
// OIDC verification and the upstream supplier are controlled; this is local
// integration acceptance, not live Casdoor, a paid supplier or deployed proof.
func TestManagedAPIKeyActualStandardGatewayAcceptance(t *testing.T) {
	gateway := os.Getenv("UNIHUB_LLMROUTER_PATH")
	if gateway == "" {
		t.Skip("UNIHUB_LLMROUTER_PATH unset")
	}
	if !filepath.IsAbs(gateway) {
		t.Fatal("explicit absolute gateway checkout required")
	}
	binary := filepath.Join(t.TempDir(), "managed-gateway.test")
	build := exec.Command("go", "test", "-c", "-o", binary, "./router")
	build.Dir = gateway
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("gateway build: %v\n%s", err, output)
	}
	f := newBillingModesFixture(t)
	profile, err := billing.BillingPreset("llmrouter", "prepaid")
	if err != nil {
		t.Fatal(err)
	}
	profile.Usage.Models = []string{"unihub-synthetic-model"}
	if _, err = f.billing.SaveProfile(context.Background(), f.actor.ID, "llmrouter", profile); err != nil {
		t.Fatal(err)
	}
	f.recharge("llmrouter", "1000000", "managed-router-fund")
	f.recharge("superwagie", "2000000", "other-product-fund")
	f.recharge("", "3000000", "unified-fund")
	rate := billing.Rate{ID: "managed-gateway-cash", Model: "unihub-synthetic-model", Asset: billing.CashAsset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "3", Denominator: "1"}, Output: billing.Rational{Numerator: "5", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "2", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "7", Denominator: "1"}}
	if _, err = f.billing.PublishRate(context.Background(), f.actor.ID, rate); err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(`[{"product_id":"llmrouter","instance_id":"default","name":"LLMRouter","entry_url":"https://product.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use","log:read"]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	users := productusers.New(f.pool, registry, "https://hub.test", nil)
	clientPolicy := products.Client{ClientID: "lr", ProductID: "llmrouter", InstanceID: "default", Origins: []string{"https://product.test"}, Scopes: []string{"access:read", "identity:read", "accounts:read", "catalog:read", "usage:read", "usage:all-products", "model-credentials:write", "settings:write"}}
	server := httptest.NewServer(New(verifier{}, accounts.New(f.pool), f.wallet, Options{Billing: f.billing, ModelAccess: modelaccess.New(f.pool, accounts.New(f.pool), f.billing), ServiceToken: billingModesServiceToken, ProductVerifiers: map[string]auth.Verifier{"lr": managedGatewayProductVerifier{}}, ProductAccess: map[string]products.Client{"lr": clientPolicy}, ProductUsers: users}))
	t.Cleanup(server.Close)
	f.server, f.client = server, server.Client()
	f.client.Timeout = 30 * time.Second
	path := "/v1/accounts/" + f.account
	var key modelaccess.APIKey
	f.request("POST", path+"/api-keys", "alice", modelaccess.APIKeyRequest{Name: "Actual standard gateway", OriginProductID: "llmrouter", InstanceID: "default", Models: []string{"unihub-synthetic-model"}, MaxTotal: "1000000"}, "", 201, &key)
	budgetPath := path + "/usage-budget?product_id=llmrouter&instance_id=default&funding_mode=prepaid_cash"
	f.request("PUT", budgetPath, "alice", billing.UsageBudgetRequest{MaxTotal: "1000000"}, "", 200, nil)
	upstream, _ := url.Parse(f.server.URL)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var lost atomic.Int32
	proxy.ModifyResponse = func(response *http.Response) error {
		if strings.HasSuffix(response.Request.URL.Path, "/settle") && lost.CompareAndSwap(0, 1) {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			response.StatusCode, response.Status, response.ContentLength = 503, "503 controlled response loss", 0
			response.Body = io.NopCloser(strings.NewReader(""))
			response.Header.Set("Content-Length", "0")
		}
		return nil
	}
	bridge := httptest.NewServer(proxy)
	defer bridge.Close()
	dir := t.TempDir()
	envFile := filepath.Join(dir, "fixture.env")
	if err = os.WriteFile(envFile, []byte("MODEL_SERVICE_TOKEN="+billingModesServiceToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start := func(recover bool) (string, func()) {
		t.Helper()
		ready := filepath.Join(dir, fmt.Sprintf("ready-%t", recover))
		cmd := exec.Command(binary, "-test.run=^TestPlatformLocalLiveFixture$", "-test.v")
		cmd.Dir = gateway
		cmd.Env = append(os.Environ(), "UNIHUB_PLATFORM_E2E=1", "UNIHUB_PLATFORM_E2E_ENV_FILE="+envFile, "UNIHUB_PLATFORM_E2E_ORIGIN="+bridge.URL, "UNIHUB_PLATFORM_E2E_DATABASE="+filepath.Join(dir, "gateway.db"), "UNIHUB_PLATFORM_E2E_LISTEN=127.0.0.1:0", "UNIHUB_PLATFORM_E2E_READY="+ready, fmt.Sprintf("UNIHUB_PLATFORM_E2E_RECOVERY=%t", recover), `UNIHUB_PLATFORM_E2E_ASSET_RATES={"CNY/6":{"unihub-synthetic-model":"managed-gateway-cash"}}`, "UNIHUB_PLATFORM_E2E_PRODUCT_CLIENT_ID=lr", "UNIHUB_PLATFORM_E2E_PUBLIC_ORIGIN=https://hub.test", "UNIHUB_PLATFORM_E2E_REDIRECT_URI=https://product.test/login/callback")
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			if err := <-done; err != nil {
				t.Errorf("gateway exit: %v\n%s", err, output.Bytes())
			}
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-done:
				stopped = true
				t.Fatalf("gateway exited before readiness: %v\n%s", err, output.Bytes())
			default:
			}
			if data, err := os.ReadFile(ready); err == nil {
				return string(data), stop
			}
			time.Sleep(50 * time.Millisecond)
		}
		stop()
		t.Fatalf("gateway readiness: %s", output.Bytes())
		return "", nil
	}
	origin, stop := start(false)
	callIDs := map[string]string{}
	request := func(method, suffix, id string, body []byte, want int, out any) []byte {
		t.Helper()
		r, err := http.NewRequest(method, origin+suffix, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(suffix, "/__fixture/") {
			r.Header.Set("Authorization", "Bearer "+key.Token)
		}
		r.Header.Set("Content-Type", "application/json")
		if id != "" {
			r.Header.Set("Idempotency-Key", id)
		}
		response, err := f.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("managed gateway %s %s: %d %s", method, suffix, response.StatusCode, data)
		}
		if method == http.MethodPost && want == 200 {
			callIDs[id] = response.Header.Get("X-UniHub-Call-ID")
		}
		if out != nil {
			if err = json.Unmarshal(data, out); err != nil {
				t.Fatalf("gateway JSON: %v %s", err, data)
			}
		}
		return data
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	request("GET", "/v1/models", "", nil, 200, &models)
	if len(models.Data) != 1 || models.Data[0].ID != "unihub-synthetic-model" {
		t.Fatalf("managed model scope: %+v", models)
	}
	body := []byte(`{"model":"unihub-synthetic-model","messages":[{"role":"user","content":"isolated managed API acceptance"}],"max_tokens":50}`)
	data := request("POST", "/v1/chat/completions", "managed-standard-call", body, 200, nil)
	if !bytes.Contains(data, []byte("synthetic local provider")) {
		t.Fatal("actual provider response absent")
	}
	standardID := callIDs["managed-standard-call"]
	if !strings.HasPrefix(standardID, "uk_") {
		t.Fatal("standard API did not return generated central call ID")
	}
	var receipt struct{ Status, Charged, Asset string }
	request("GET", "/platform/v1/calls/"+standardID, "", nil, 200, &receipt)
	if receipt.Status != "SETTLE_PENDING" || lost.Load() != 1 {
		t.Fatalf("lost durable receipt: %+v", receipt)
	}
	request("POST", "/v1/chat/completions", "managed-standard-call", body, 409, nil)
	var status struct {
		ProviderCalls    int `json:"provider_calls"`
		Quota            int `json:"quota"`
		UsedQuota        int `json:"used_quota"`
		NativeQuota      int `json:"native_quota"`
		NativeUsed       int `json:"native_used_quota"`
		NativeRequests   int `json:"native_requests"`
		NativeTokenQuota int `json:"native_token_quota"`
		NativeTokenUsed  int `json:"native_token_used_quota"`
	}
	request("GET", "/__fixture/status", "", nil, 200, &status)
	if status.ProviderCalls != 1 || status.Quota != 0 || status.UsedQuota != 0 {
		t.Fatalf("dispatch/native state: %+v", status)
	}
	if status.NativeQuota != 12345 || status.NativeUsed != 7 || status.NativeRequests != 3 || status.NativeTokenQuota != 54321 || status.NativeTokenUsed != 11 {
		t.Fatalf("native compatibility marker changed: %+v", status)
	}
	stop()
	origin, _ = start(true)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		request("GET", "/platform/v1/calls/"+standardID, "", nil, 200, &receipt)
		if receipt.Status == "SETTLED" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if receipt.Status != "SETTLED" || receipt.Charged != "55" || receipt.Asset != billing.CashAsset {
		t.Fatalf("recovered durable receipt: %+v", receipt)
	}
	request("POST", "/v1/chat/completions", "managed-standard-call", body, 409, nil)
	streamBody := []byte(`{"model":"unihub-synthetic-model","messages":[{"role":"user","content":"isolated managed streaming acceptance"}],"max_tokens":50,"stream":true}`)
	data = request("POST", "/v1/chat/completions", "managed-stream-call", streamBody, 200, nil)
	if !bytes.Contains(data, []byte("synthetic local provider")) || !bytes.Contains(data, []byte("[DONE]")) {
		t.Fatal("actual stream absent")
	}
	streamID := callIDs["managed-stream-call"]
	request("GET", "/platform/v1/calls/"+streamID, "", nil, 200, &receipt)
	if receipt.Status != "SETTLED" || receipt.Charged != "55" {
		t.Fatalf("stream settlement: %+v", receipt)
	}
	request("POST", "/v1/chat/completions", "managed-stream-call", streamBody, 409, nil)
	var budget billing.UsageBudget
	f.request("GET", budgetPath, "alice", nil, "", 200, &budget)
	if budget.Held != "0" || budget.Used != "110" || budget.Remaining == nil || *budget.Remaining != "999890" {
		t.Fatalf("central budget: %+v", budget)
	}
	var call billing.Call
	f.request("GET", "/internal/v1/calls/"+standardID, billingModesServiceToken, nil, "", 200, &call)
	if call.Status != "SETTLED" || call.Charged != "55" || call.CredentialID != key.ID || call.InstanceID != "default" || call.TaskID == "" {
		t.Fatalf("central call scope: %+v", call)
	}
	var count int
	if err = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing_calls WHERE account_id=$1`, f.account).Scan(&count); err != nil || count != 2 {
		t.Fatalf("logical calls %d: %v", count, err)
	}
	var summary billing.UsageSummary
	f.request("GET", path+"/model-pool/usage/summary?asset=CNY%2F6", "managed-product-alice", nil, "", 200, &summary)
	if summary.Charged != "110" {
		t.Fatalf("central dashboard readback: %+v", summary)
	}
	// Exchange a verified central product token through the real LLMRouter
	// session route, then use its actual JWT on the established dashboard/log API.
	login, err := http.NewRequest("POST", origin+"/api/unihub/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Authorization", "Bearer managed-product-alice")
	login.Header.Set("Origin", origin)
	response, err := f.client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	loginData, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if response.StatusCode != 200 || json.Unmarshal(loginData, &session) != nil || !session.Success || session.Data.AccessToken == "" {
		t.Fatalf("actual LR projected session: %d %s", response.StatusCode, loginData)
	}
	readProduct := func(suffix string) []byte {
		t.Helper()
		r, err := http.NewRequest("GET", origin+suffix, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+session.Data.AccessToken)
		resp, err := f.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Success bool `json:"success"`
		}
		if resp.StatusCode != 200 || json.Unmarshal(data, &envelope) != nil || !envelope.Success {
			t.Fatalf("LR product read %s: %d %s", suffix, resp.StatusCode, data)
		}
		return data
	}
	logs := readProduct("/api/log/self?p=1&page_size=100")
	if !bytes.Contains(logs, []byte(standardID)) || !bytes.Contains(logs, []byte(streamID)) || !bytes.Contains(logs, []byte("funding_owner")) || !bytes.Contains(logs, []byte("central_status")) {
		t.Fatalf("actual product logs missing central technical calls: %s", logs)
	}
	var dashboard struct {
		Data []struct {
			Count     int `json:"count"`
			TokenUsed int `json:"token_used"`
			Quota     int `json:"quota"`
		} `json:"data"`
	}
	data = readProduct("/api/data/self")
	if json.Unmarshal(data, &dashboard) != nil {
		t.Fatal("invalid dashboard JSON")
	}
	requests, tokens := 0, 0
	for _, point := range dashboard.Data {
		requests += point.Count
		tokens += point.TokenUsed
		if point.Quota != 0 {
			t.Fatal("dashboard invented a native charge")
		}
	}
	if requests != 2 || tokens != 30 {
		t.Fatalf("actual dashboard requests=%d tokens=%d body=%s", requests, tokens, data)
	}
	_ = readProduct("/api/data/flow/self")
	f.balance("llmrouter", 999890)
	f.balance("superwagie", 2000000)
	f.balance("", 3000000)
	f.request("DELETE", path+"/api-keys/"+key.ID+"?product_id=llmrouter&instance_id=default", "alice", nil, "", 204, nil)
	request("POST", "/v1/chat/completions", "revoked-managed-call", body, 403, nil)
	request("GET", "/platform/v1/calls/"+standardID, "", nil, 404, nil)
	request("GET", "/__fixture/status", "", nil, 200, &status)
	if status.ProviderCalls != 1 || status.Quota != 0 || status.UsedQuota != 0 {
		t.Fatalf("restart/revoke dispatched or charged native: %+v", status)
	}
	if status.NativeQuota != 12345 || status.NativeUsed != 7 || status.NativeRequests != 3 || status.NativeTokenQuota != 54321 || status.NativeTokenUsed != 11 {
		t.Fatalf("native compatibility marker changed after restart: %+v", status)
	}
	t.Log("actual standard /v1/models + ordinary/streaming /v1/chat/completions with durable UniHub key; PostgreSQL/Formance settled 110 once; response-loss restart recovered without redispatch; scope/budget/readback/revocation passed; native quota and other wallets unchanged")
}
