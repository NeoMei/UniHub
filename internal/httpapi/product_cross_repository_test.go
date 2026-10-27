package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/console"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

// Explicit opt-in keeps ordinary UniHub tests independent of a sibling checkout.
// Both services execute their real adapters; only the unused OIDC discovery and
// model supplier are outside this local HTTP/SQL/final-ledger acceptance gate.
func TestCrossRepositoryProductBrowserAndOriginalBilling(t *testing.T) {
	lrDir := os.Getenv("UNIHUB_CROSS_PRODUCT_LR_DIR")
	if lrDir == "" {
		t.Skip("UNIHUB_CROSS_PRODUCT_LR_DIR is not set")
	}
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	central := httptest.NewUnstartedServer(nil)
	defer central.Close()
	publicURL := "http://" + central.Listener.Addr().String()
	reg, e := productusers.Parse([]byte(`[{"product_id":"llmrouter","name":"Router","entry_url":"https://router.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]},{"product_id":"llmrouter","instance_id":"second","name":"Second","entry_url":"https://second.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	if e != nil {
		t.Fatal(e)
	}
	users := productusers.New(p, reg, publicURL, nil)
	pa := productauth.New(p, a, users, publicURL)
	clients := map[string]products.Client{"router": {ClientID: "router", ProductID: "llmrouter", InstanceID: "default", BrowserSSO: true, RedirectURIs: []string{"https://router.test/api/unihub/callback"}}, "second": {ClientID: "second", ProductID: "llmrouter", InstanceID: "second", BrowserSSO: true, RedirectURIs: []string{"https://second.test/api/unihub/callback"}}}
	pa.ConfigureBrowserClients(clients)
	ops := []string{"identity:register", "identity:authenticate", "identity:browser", "identity:lookup", "identity:lifecycle", "bindings:write", "access:read", "billing:read", "billing:write", "calls:reprice", "credits:award"}
	services := map[string]RuntimeService{"router": {Token: strings.Repeat("a", 32), ProductID: "llmrouter", InstanceID: "default", AllowedOps: ops}, "second": {Token: strings.Repeat("b", 32), ProductID: "llmrouter", InstanceID: "second", AllowedOps: ops}}
	ledgerURL := os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if ledgerURL == "" {
		t.Skip("UNIHUB_TEST_LEDGER_URL is not set")
	}
	l := ledger.NewFormance(ledgerURL, fmt.Sprintf("cross-product-%d", time.Now().UnixNano()), &http.Client{Timeout: 5 * time.Second})
	if e = l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, l)
	api := New(runtimeVerifier{}, a, wallet.New(p, a, l), Options{Billing: b, ProductUsers: users, ProductAuth: pa, ProductAccess: clients, ProductRuntime: services})
	var oidc *httptest.Server
	oidc = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": oidc.URL, "authorization_endpoint": oidc.URL + "/authorize", "token_endpoint": oidc.URL + "/token", "jwks_uri": oidc.URL + "/jwks"})
	}))
	defer oidc.Close()
	browser, e := console.New(ctx, p, runtimeVerifier{}, console.Options{Issuer: oidc.URL, PublicURL: publicURL, ClientID: "console"})
	if e != nil {
		t.Fatal(e)
	}
	central.Config.Handler = pa.BrowserHandler(browser.Handler(api))
	central.Start()
	run, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	cmd := exec.CommandContext(run, "go", "test", "-race", "-count=1", "-run", "^TestCrossProductRuntimeAcceptance$", "-v", "./router")
	cmd.Dir = lrDir
	cmd.Env = append(os.Environ(), "UNIHUB_CROSS_PRODUCT_ORIGIN="+publicURL)
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("LLMRouter cross-product acceptance: %v\n%s", e, output)
	}
	t.Log(string(output))
	var count int
	if e = p.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("canonical identity count %d: %v", count, e)
	}
	if e = p.QueryRow(ctx, `SELECT count(*) FROM product_runtime_bindings`).Scan(&count); e != nil || count != 2 {
		t.Fatalf("original mappings %d: %v", count, e)
	}
	if e = p.QueryRow(ctx, `SELECT count(*) FROM product_runtime_calls WHERE snapshot->>'status'='SETTLED'`).Scan(&count); e != nil || count != 2 {
		t.Fatalf("exact settled calls %d: %v", count, e)
	}
	if e = p.QueryRow(ctx, `SELECT count(*) FROM product_runtime_calls parent JOIN product_runtime_calls child ON child.snapshot->>'parent_call_id'=parent.snapshot->>'call_id' WHERE parent.snapshot->>'call_id'='cross-product-original-billing' AND parent.snapshot->>'charged'='37' AND child.snapshot->>'charged'='10' AND child.snapshot->>'account_id'=parent.snapshot->>'account_id' AND child.snapshot->>'local_user_id'=parent.snapshot->>'local_user_id' AND child.snapshot->>'local_key_id'=parent.snapshot->>'local_key_id' AND child.snapshot->>'model'=parent.snapshot->>'model' AND child.snapshot->>'operation'=parent.snapshot->>'operation' AND child.snapshot->>'pricing_digest'=parent.snapshot->>'pricing_digest'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("accepted-cost child must retain exact original payer/key/price: %d %v", count, e)
	}
	var owner string
	if e = p.QueryRow(ctx, `SELECT personal_account_id FROM product_runtime_bindings WHERE product_id='llmrouter' AND instance_id='second'`).Scan(&owner); e != nil {
		t.Fatal(e)
	}
	for instance, want := range map[string]int64{"default": 500, "second": 453} {
		n, e := l.Balance(ctx, billing.RuntimeAddress(billing.RuntimeScope{ProductID: "llmrouter", InstanceID: instance}, owner, "available"), billing.RuntimeAsset)
		if e != nil || n != want {
			t.Fatalf("%s final ledger %d want %d: %v", instance, n, want, e)
		}
		t.Logf("final ledger: product=llmrouter instance=%s available=%d", instance, n)
	}
}
