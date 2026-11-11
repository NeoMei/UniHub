package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

// TestManualRechargeDirectModeConsoleOnly proves the manual (no real payment)
// recharge is reachable by an allowlisted console owner while real payments are
// configured (PAYMENT_MODE=direct, no sandbox/merchant/signing key), and is
// rejected for a product token even one holding recharges:write, and for a
// console user outside the allowlist.
func TestManualRechargeDirectModeConsoleOnly(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	owner, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice", Name: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, owner.ID)
	if e != nil || len(as) != 1 {
		t.Fatalf("accounts %+v %v", as, e)
	}
	account := as[0].ID

	w := wallet.New(p, a, noLedger{})
	w.ConfigureSandboxRechargeUsers([]string{owner.ID}) // only the master account

	clientPolicy := products.Client{
		ClientID: "sw", ProductID: "superwagie", BillingProducts: []string{"superwagie", "llmrouter"},
		Scopes: []string{"accounts:read", "recharges:write"},
	}
	srv := httptest.NewServer(New(verifier{}, a, w, Options{
		Sandbox:          false, // direct mode: no merchant, no signing key
		ProductVerifiers: map[string]auth.Verifier{"sw": anyProductVerifier{}},
		ProductAccess:    map[string]products.Client{"sw": clientPolicy},
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()

	status := func(token, key string) int {
		body, _ := json.Marshal(map[string]string{"product_id": "", "amount": "1000000"})
		r, e := http.NewRequest("POST", srv.URL+"/v1/accounts/"+account+"/manual-recharges", bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		resp, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := status("alice", "manual-http-owner"); got != http.StatusAccepted {
		t.Fatalf("allowlisted console owner in direct mode: status=%d want 202", got)
	}
	if got := status("product-alice", "manual-http-product"); got != http.StatusForbidden {
		t.Fatalf("product token with recharges:write: status=%d want 403", got)
	}
	if got := status("bob", "manual-http-stranger"); got != http.StatusForbidden {
		t.Fatalf("non-allowlisted console user: status=%d want 403", got)
	}
}
