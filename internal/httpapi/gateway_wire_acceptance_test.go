package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/metering"
	"unihub/internal/modelaccess"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

// The two services and PostgreSQL are real; the supplier and ledger are
// controlled fixtures. This opt-in test never accesses a production account.
func TestWireOperationActualGatewayControlledAcceptance(t *testing.T) {
	gateway := os.Getenv("UNIHUB_LLMROUTER_PATH")
	if gateway == "" {
		t.Skip("UNIHUB_LLMROUTER_PATH unset")
	}
	if !filepath.IsAbs(gateway) {
		t.Fatal("explicit absolute gateway checkout required")
	}
	binary := filepath.Join(t.TempDir(), "gateway.test")
	build := exec.Command("go", "test", "-c", "-o", binary, "./router")
	build.Dir = gateway
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("gateway fixture build: %v\n%s", err, output)
	}
	for _, operation := range []string{"chat", "embedding"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			pool := testutil.Database(t)
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			a := accounts.New(pool)
			user, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://controlled-gateway", Subject: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			accountsList, err := a.ListAccounts(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			account := accountsList[0].ID
			l := &wireLedger{balances: map[string]int64{}, transactions: map[string]ledger.Transaction{}}
			b := billing.New(pool, a, l)
			b.ConfigureOperators([]string{user.ID})
			b.ConfigureTrustedServices([]string{"llmrouter"})
			_, err = b.SaveProfile(ctx, user.ID, "superwagie", billing.BillingProfile{ProductID: "superwagie", Usage: billing.UsagePolicy{Enabled: true, FundingModes: []string{billing.FundingPrepaidCash}, DefaultFundingMode: billing.FundingPrepaidCash}})
			if err != nil {
				t.Fatal(err)
			}
			model := "unihub-synthetic-model"
			rate := billing.Rate{ID: "gateway-cash", Model: model, Asset: billing.CashAsset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "3", Denominator: "1"}, Output: billing.Rational{Numerator: "5", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "2", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "7", Denominator: "1"}}
			if operation == "embedding" {
				rate = billing.Rate{ID: "native-rate", Model: model, Asset: billing.CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "embedding", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "2", Denominator: "1"}}}}}
			}
			if _, err := b.PublishRate(ctx, user.ID, rate); err != nil {
				t.Fatal(err)
			}
			l.balances[wallet.Address(account, "llmrouter")+"/"+billing.CashAsset] = 1000000
			access := modelaccess.New(pool, a, b)
			grant, err := access.Issue(ctx, user.ID, account, modelaccess.Request{OriginProductID: "superwagie", Models: []string{model}, TaskID: "acceptance-task"})
			if err != nil {
				t.Fatal(err)
			}
			const serviceKey = "test-only-router-service-credential-at-least-32-bytes"
			hub := httptest.NewServer(New(verifier{}, a, wallet.New(pool, a, l), Options{Billing: b, ModelAccess: access, ServiceToken: serviceKey}))
			defer hub.Close()
			dir := t.TempDir()
			envFile, ready := filepath.Join(dir, "fixture.env"), filepath.Join(dir, "ready")
			if err := os.WriteFile(envFile, []byte("MODEL_SERVICE_TOKEN="+serviceKey+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestPlatformLocalLiveFixture$", "-test.v")
			cmd.Dir = gateway
			cmd.Env = append(os.Environ(), "UNIHUB_PLATFORM_E2E=1", "UNIHUB_PLATFORM_E2E_ENV_FILE="+envFile, "UNIHUB_PLATFORM_E2E_ORIGIN="+hub.URL, "UNIHUB_PLATFORM_E2E_DATABASE="+filepath.Join(dir, "gateway.db"), "UNIHUB_PLATFORM_E2E_LISTEN=127.0.0.1:0", "UNIHUB_PLATFORM_E2E_READY="+ready, "UNIHUB_PLATFORM_E2E_RECOVERY=false", `UNIHUB_PLATFORM_E2E_ASSET_RATES={"CNY/6":{"unihub-synthetic-model":"gateway-cash"}}`)
			if operation == "embedding" {
				cmd.Env = append(cmd.Env, "UNIHUB_PLATFORM_E2E_NATIVE_WIRE=embeddings.create")
			}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				if err := cmd.Wait(); err != nil {
					t.Errorf("gateway exit: %v\n%s", err, output.Bytes())
				}
			}()
			origin := ""
			deadline := time.Now().Add(45 * time.Second)
			for time.Now().Before(deadline) {
				if value, err := os.ReadFile(ready); err == nil {
					origin = string(value)
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if origin == "" {
				t.Fatalf("gateway not ready: %s", output.Bytes())
			}
			request := func(method, suffix string, body []byte, want int) []byte {
				t.Helper()
				r, err := http.NewRequest(method, origin+suffix, bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+grant.Token)
				r.Header.Set("X-UniHub-Task-ID", "acceptance-task")
				r.Header.Set("Idempotency-Key", "gateway-call")
				r.Header.Set("Content-Type", "application/json")
				response, err := http.DefaultClient.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != want {
					t.Fatalf("%s %s status %d want %d: %s\n%s", method, suffix, response.StatusCode, want, data, output.Bytes())
				}
				return data
			}
			path, body, expectedCharge, wire := "/platform/v1/chat/completions", []byte(`{"model":"unihub-synthetic-model","messages":[{"role":"user","content":"isolated acceptance"}],"max_tokens":50}`), "55", "chat.completions.create"
			if operation == "embedding" {
				path, body, expectedCharge, wire = "/platform/v1/embeddings", []byte(`{"model":"unihub-synthetic-model","input":"hello"}`), "6", "embeddings.create"
			}
			result := request("POST", path, body, 200)
			if operation == "chat" && !strings.Contains(string(result), "synthetic local provider") {
				t.Fatal("missing controlled chat result")
			}
			if operation == "embedding" && !strings.Contains(string(result), "embedding") {
				t.Fatal("missing controlled embedding result")
			}
			var receipt struct{ Status, Charged, Asset string }
			if err := json.Unmarshal(request("GET", "/platform/v1/calls/gateway-call", nil, 200), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Status != "SETTLED" || receipt.Charged != expectedCharge || receipt.Asset != billing.CashAsset {
				t.Fatalf("receipt %+v", receipt)
			}
			request("POST", path, body, 409)
			var status struct {
				ProviderCalls int `json:"provider_calls"`
				Quota         int `json:"quota"`
				UsedQuota     int `json:"used_quota"`
			}
			if err := json.Unmarshal(request("GET", "/__fixture/status", nil, 200), &status); err != nil {
				t.Fatal(err)
			}
			if status.ProviderCalls != 1 || status.Quota != 0 || status.UsedQuota != 0 {
				t.Fatalf("unexpected gateway state %+v", status)
			}
			call, err := b.GetCall(ctx, "llmrouter", "gateway-call")
			if err != nil || call.Status != "SETTLED" || call.WireOperation != wire || call.InstanceID != "default" || call.TaskID != "acceptance-task" || call.Charged != expectedCharge {
				t.Fatalf("actual UniHub call %+v: %v", call, err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing_calls`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("billing calls %d: %v", count, err)
			}
			if got := l.balances[wallet.Address(account, "llmrouter")+"/"+billing.CashAsset]; got != 1000000-map[string]int64{"chat": 55, "embedding": 6}[operation] {
				t.Fatal(fmt.Sprintf("unexpected balance: %d", got))
			}
		})
	}
}
