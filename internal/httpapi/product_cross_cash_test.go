package httpapi

import (
	"context"
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
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestCrossRepositoryUnifiedCashFunding(t *testing.T) {
	lrDir := os.Getenv("UNIHUB_CROSS_PRODUCT_LR_DIR")
	if lrDir == "" {
		t.Skip("UNIHUB_CROSS_PRODUCT_LR_DIR is not set")
	}
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	identity := accounts.Identity{Issuer: "https://cross-cash.test", Subject: "cash-owner"}
	u, err := a.EnsureIdentity(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	users := productusers.New(p, productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Permissions: []string{"product:use"}}}}}, identity.Issuer, nil)
	binding, err := users.EnsureRuntimeBinding(ctx, "llmrouter", "default", "42", "cross-cash", identity, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,product_role,version) VALUES($1,'llmrouter','default','["member"]',true,1,1)`, u.ID); err != nil {
		t.Fatal(err)
	}
	base := os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if base == "" {
		base = "http://127.0.0.1:53068"
	}
	l := ledger.NewFormance(base, fmt.Sprintf("cross-cash-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if err = l.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	scope := billing.RuntimeScope{ProductID: "llmrouter", InstanceID: "default"}
	if _, err = l.Post(ctx, ledger.Command{Reference: "cash-fixture", Postings: []ledger.Posting{
		{Source: "world", Destination: wallet.Address(binding.PersonalAccountID, ""), Asset: wallet.Asset, Amount: 10000},
		{Source: "world", Destination: wallet.Address(binding.PersonalAccountID, "llmrouter"), Asset: wallet.Asset, Amount: 400},
		{Source: "world", Destination: billing.RuntimeAddress(scope, binding.PersonalAccountID, "available"), Asset: billing.RuntimeAsset, Amount: 500},
	}}); err != nil {
		t.Fatal(err)
	}
	b := billing.New(p, a, l)
	service := RuntimeService{Token: strings.Repeat("c", 32), ProductID: "llmrouter", InstanceID: "default", AllowedOps: []string{"access:read", "billing:read", "billing:write", "calls:refund", "calls:reprice", "cash:charge"}}
	server := httptest.NewServer(New(nil, a, wallet.New(p, a, l), Options{Billing: b, ProductUsers: users, ProductRuntime: map[string]RuntimeService{"cash": service}}))
	defer server.Close()
	run, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(run, "go", "test", "-count=1", "-run", "^TestCrossProductUnifiedCashAcceptance$", "-v", "./model")
	cmd.Dir = lrDir
	cmd.Env = append(os.Environ(), "UNIHUB_CROSS_CASH_ORIGIN="+server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("LLMRouter shared cash acceptance: %v\n%s", err, output)
	}
	t.Log(string(output))
	for _, check := range []struct {
		address, asset string
		want           int64
	}{
		{wallet.Address(binding.PersonalAccountID, ""), wallet.Asset, 10000},
		{wallet.Address(binding.PersonalAccountID, "llmrouter"), wallet.Asset, 400},
		{billing.RuntimeAddress(scope, binding.PersonalAccountID, "available"), billing.RuntimeAsset, 500},
	} {
		got, err := l.Balance(ctx, check.address, check.asset)
		if err != nil || got != check.want {
			t.Fatalf("final ledger %s=%d want%d: %v", check.asset, got, check.want, err)
		}
	}
	var count int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM product_runtime_calls WHERE snapshot->>'asset'='CNY/6' AND snapshot->>'status'='SETTLED' AND snapshot->>'charged'='541' AND snapshot->>'refunded'='541'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("single exact final cash receipt count=%d: %v", count, err)
	}
}
