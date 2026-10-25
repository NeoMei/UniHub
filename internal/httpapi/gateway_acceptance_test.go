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
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
)

// Explicit local acceptance: actual llmrouter router/controller/provider adapter
// over TCP, actual UniHub HTTP, isolated PostgreSQL and real Formance. Only the
// provider response and the identity verifier are controlled test fixtures.
func TestComposableBillingActualGatewayAcceptance(t *testing.T) {
	gateway := os.Getenv("UNIHUB_LLMROUTER_PATH")
	if gateway == "" {
		t.Skip("UNIHUB_LLMROUTER_PATH unset")
	}
	if !filepath.IsAbs(gateway) {
		t.Fatal("gateway checkout must be an explicit absolute path")
	}
	binary := filepath.Join(t.TempDir(), "gateway.test")
	build := exec.Command("go", "test", "-c", "-o", binary, "./router")
	build.Dir = gateway
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("gateway fixture build: %v\n%s", err, output)
	}
	for _, mode := range []string{"legacy_member_credits", "prepaid_without_membership"} {
		t.Run(mode, func(t *testing.T) {
			f := newBillingModesFixture(t)
			cash := mode == "prepaid_without_membership"
			if cash {
				f.profile("prepaid")
			} else {
				// No profile row: preserve the original version-zero hybrid policy.
				f.recharge("superwagie", "2000000", "member-funding")
				f.offer("gateway-month", "month", "1000000")
				f.purchase("gateway-month", "gateway-membership")
			}
			f.recharge("superwagie", "1000000", "source-cash")
			f.recharge("", "7000000", "unified-cash")
			f.recharge("llmrouter", "1000000", "router-cash")
			for _, asset := range []string{billing.CreditAsset, billing.CashAsset} {
				r := billing.Rate{ID: "gateway-credit", Model: "unihub-synthetic-model", Asset: asset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "1", Denominator: "1"}, Output: billing.Rational{Numerator: "2", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "1", Denominator: "2"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "3", Denominator: "1"}}
				if asset == billing.CashAsset {
					r.ID, r.Input.Numerator, r.Output.Numerator, r.CacheRead, r.Reasoning.Numerator = "gateway-cash", "3", "5", billing.Rational{Numerator: "2", Denominator: "1"}, "7"
				}
				f.request("POST", "/ops/v1/rates", "alice", r, "", 201, nil)
			}
			var grant modelaccess.Grant
			path := "/v1/accounts/" + f.account
			f.request("POST", path+"/model-credentials", "product-alice", modelaccess.Request{Models: []string{"unihub-synthetic-model"}, TaskID: "acceptance-task"}, "", 201, &grant)
			asset, charge, rate := billing.CreditAsset, "20", "gateway-credit"
			if cash {
				asset, charge, rate = billing.CashAsset, "55", "gateway-cash"
				var subscriptions []billing.Subscription
				f.request("GET", path+"/subscriptions", "product-alice", nil, "", 200, &subscriptions)
				if len(subscriptions) != 0 || grant.FundingMode != billing.FundingPrepaidCash || grant.BillingProfileVersion != 1 {
					t.Fatal("prepaid grant unexpectedly depends on membership")
				}
			} else if grant.FundingMode != billing.FundingCredits || grant.BillingProfileVersion != 0 {
				t.Fatal("original hybrid membership compatibility changed")
			}
			if grant.Asset != asset {
				t.Fatal("credential has wrong denomination")
			}
			// Let the actual UniHub finish settlement, then lose its response once.
			// Restart must resend only the stored settlement, never the provider.
			upstream, _ := url.Parse(f.server.URL)
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			var lost atomic.Int32
			proxy.ModifyResponse = func(r *http.Response) error {
				if strings.HasSuffix(r.Request.URL.Path, "/settle") && lost.CompareAndSwap(0, 1) {
					_, _ = io.Copy(io.Discard, r.Body)
					_ = r.Body.Close()
					r.StatusCode, r.Status, r.ContentLength = 503, "503 controlled response loss", 0
					r.Body = io.NopCloser(strings.NewReader(""))
					r.Header.Set("Content-Length", "0")
				}
				return nil
			}
			bridge := httptest.NewServer(proxy)
			defer bridge.Close()
			dir := t.TempDir()
			envFile := filepath.Join(dir, "fixture.env")
			if err := os.WriteFile(envFile, []byte("MODEL_SERVICE_TOKEN="+billingModesServiceToken+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			start := func(recover bool) (string, func()) {
				t.Helper()
				ready := filepath.Join(dir, fmt.Sprintf("ready-%t", recover))
				cmd := exec.Command(binary, "-test.run=^TestPlatformLocalLiveFixture$", "-test.v")
				cmd.Dir = gateway
				cmd.Env = append(os.Environ(), "UNIHUB_PLATFORM_E2E=1", "UNIHUB_PLATFORM_E2E_ENV_FILE="+envFile, "UNIHUB_PLATFORM_E2E_ORIGIN="+bridge.URL, "UNIHUB_PLATFORM_E2E_DATABASE="+filepath.Join(dir, "gateway.db"), "UNIHUB_PLATFORM_E2E_LISTEN=127.0.0.1:0", "UNIHUB_PLATFORM_E2E_READY="+ready, fmt.Sprintf("UNIHUB_PLATFORM_E2E_RECOVERY=%t", recover), `UNIHUB_PLATFORM_E2E_ASSET_RATES={"CREDIT/6":{"unihub-synthetic-model":"gateway-credit"},"CNY/6":{"unihub-synthetic-model":"gateway-cash"}}`)
				var output bytes.Buffer
				cmd.Stdout, cmd.Stderr = &output, &output
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				stopped := false
				stop := func() {
					if stopped {
						return
					}
					stopped = true
					_ = cmd.Process.Signal(syscall.SIGTERM)
					if err := cmd.Wait(); err != nil {
						t.Errorf("gateway fixture exit: %v\n%s", err, output.Bytes())
					}
				}
				t.Cleanup(stop)
				deadline := time.Now().Add(45 * time.Second)
				for time.Now().Before(deadline) {
					if b, err := os.ReadFile(ready); err == nil {
						return string(b), stop
					}
					time.Sleep(50 * time.Millisecond)
				}
				stop()
				t.Fatalf("gateway did not become ready: %s", output.Bytes())
				return "", nil
			}
			origin, stop := start(false)
			request := func(method, origin, suffix string, body []byte, want int, out any) {
				t.Helper()
				r, err := http.NewRequest(method, origin+suffix, bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+grant.Token)
				r.Header.Set("X-UniHub-Task-ID", "acceptance-task")
				r.Header.Set("Idempotency-Key", "gateway-call")
				r.Header.Set("Content-Type", "application/json")
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
					t.Fatalf("gateway %s %s: %d %s", method, suffix, response.StatusCode, data)
				}
				if out != nil && json.Unmarshal(data, out) != nil {
					t.Fatal("invalid gateway JSON")
				}
				if method == "POST" && want == 200 && !bytes.Contains(data, []byte("synthetic local provider")) {
					t.Fatal("provider response did not cross actual gateway")
				}
			}
			body := []byte(fmt.Sprintf(`{"model":"unihub-synthetic-model","messages":[{"role":"user","content":"isolated acceptance"}],"max_tokens":50,"stream":%t}`, cash))
			request("POST", origin, "/platform/v1/chat/completions", body, 200, nil)
			var receipt struct{ Status, Charged, Asset string }
			request("GET", origin, "/platform/v1/calls/gateway-call", nil, 200, &receipt)
			if receipt.Status != "SETTLE_PENDING" || receipt.Asset != asset || lost.Load() != 1 {
				t.Fatalf("lost response not durable: %+v", receipt)
			}
			request("POST", origin, "/platform/v1/chat/completions", body, 409, nil)
			var status struct {
				ProviderCalls int `json:"provider_calls"`
				Quota         int `json:"quota"`
				UsedQuota     int `json:"used_quota"`
			}
			request("GET", origin, "/__fixture/status", nil, 200, &status)
			if status.ProviderCalls != 1 || status.Quota != 0 || status.UsedQuota != 0 {
				t.Fatalf("gateway dispatched twice or debited legacy quota: %+v", status)
			}
			stop()
			origin, _ = start(true)
			deadline := time.Now().Add(12 * time.Second)
			for time.Now().Before(deadline) {
				request("GET", origin, "/platform/v1/calls/gateway-call", nil, 200, &receipt)
				if receipt.Status == "SETTLED" {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if receipt.Status != "SETTLED" || receipt.Charged != charge || receipt.Asset != asset {
				t.Fatalf("restart receipt %+v", receipt)
			}
			request("POST", origin, "/platform/v1/chat/completions", body, 409, nil)
			request("GET", origin, "/__fixture/status", nil, 200, &status)
			if status.ProviderCalls != 0 || status.Quota != 0 || status.UsedQuota != 0 {
				t.Fatalf("recovery redispatched or debited local quota: %+v", status)
			}
			var call billing.Call
			f.request("GET", "/internal/v1/calls/gateway-call", billingModesServiceToken, nil, "", 200, &call)
			if call.Status != "SETTLED" || call.ChargeAsset != asset || call.Rate.ID != rate || call.Charged != charge || call.FundingProductID != "llmrouter" {
				t.Fatalf("actual UniHub settlement: %+v", call)
			}
			var calls int
			if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM billing_calls").Scan(&calls); err != nil || calls != 1 {
				t.Fatal("duplicate billing call", err)
			}
			if cash {
				f.balance("llmrouter", 999945)
				f.balance("superwagie", 1000000)
				f.balance("", 7000000)
				for address, want := range map[string]int64{billing.CashHoldAddress(f.account, "gateway-call"): 0, "commercial:revenue:llmrouter": 55} {
					if n, err := f.ledger.Balance(context.Background(), address, asset); err != nil || n != want {
						t.Fatalf("cash ledger %d want %d: %v", n, want, err)
					}
				}
			} else {
				var batches []billing.Batch
				f.request("GET", path+"/credits", "alice", nil, "", 200, &batches)
				if len(batches) != 1 || batches[0].Available != "999980" || batches[0].Consumed != "20" || batches[0].Reserved != "0" {
					t.Fatalf("credit ledger: %+v", batches)
				}
				f.balance("llmrouter", 1000000)
				f.balance("superwagie", 2000000)
				f.balance("", 7000000)
			}
			t.Logf("actual TCP gateway + UniHub HTTP + PostgreSQL/Formance: %s charged %s, restart receipt SETTLED, duplicate rejected, provider dispatch once, local quota untouched", asset, charge)
		})
	}
}
