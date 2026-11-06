package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestSandboxRechargeCapabilityDefaultsToDenied(t *testing.T) {
	service := wallet.New(nil, nil, noLedger{})
	s := &api{wallet: service, options: Options{Sandbox: true}}
	w := httptest.NewRecorder()
	if err := s.capabilities(w, httptest.NewRequest("GET", "/v1/capabilities", nil), accounts.User{ID: "12345678-1234-1234-1234-1234567890ab"}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Sandbox  bool `json:"sandbox"`
		Recharge bool `json:"product_wallet_recharge"`
		Unified  bool `json:"unified_wallet_recharge"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Sandbox || result.Recharge || result.Unified {
		t.Fatalf("empty allowlist must deny recharge while preserving sandbox: %s", w.Body)
	}
}

func TestSandboxRechargeAdmissionAcrossAuthenticatedHTTPRoutes(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(pool)
	alice, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice", Name: "neomei"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob", Name: "neomei"})
	if err != nil {
		t.Fatal(err)
	}
	team, err := a.CreateTeam(ctx, alice.ID, "Sandbox policy fixture")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := a.Invite(ctx, alice.ID, team.ID, bob.ID, "billing_admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.AcceptInvitation(ctx, bob.ID, invite.ID); err != nil {
		t.Fatal(err)
	}
	service := wallet.New(pool, a, noLedger{})
	service.ConfigureSandboxRechargeUsers([]string{alice.ID})
	signingKey := []byte("sandbox-policy-test-signing-key-over-32-bytes")
	policy := products.Client{ClientID: "sw", ProductID: "superwagie", Scopes: []string{"accounts:read", "recharges:read", "recharges:write"}}
	h := New(verifier{}, a, service, Options{Sandbox: true, Merchant: "sandbox", SigningKey: signingKey, ProductVerifiers: map[string]auth.Verifier{"sw": anyProductVerifier{}}, ProductAccess: map[string]products.Client{"sw": policy}})
	request := func(mode, actor, method, path, body, key string) *httptest.ResponseRecorder {
		if mode == "product" {
			return call(h, method, path, "product-"+actor, body, key)
		}
		if mode == "console" {
			return call(h, method, path, actor, body, key)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		r = r.WithContext(auth.WithBrowserClaims(r.Context(), auth.Claims{Issuer: "https://test", Subject: actor, Name: "neomei"}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := "/v1/accounts/" + team.ID + "/recharges"
	for _, mode := range []string{"console", "browser", "product"} {
		t.Run(mode, func(t *testing.T) {
			for _, actor := range []string{"alice", "bob"} {
				result := request(mode, actor, "GET", "/v1/capabilities", "", "")
				var caps struct {
					Sandbox  bool `json:"sandbox"`
					Recharge bool `json:"product_wallet_recharge"`
					Unified  bool `json:"unified_wallet_recharge"`
				}
				if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &caps) != nil || !caps.Sandbox || caps.Recharge != (actor == "alice") || caps.Unified != (actor == "alice" && mode != "product") {
					t.Fatalf("%s capabilities %s: %d %s", mode, actor, result.Code, result.Body)
				}
			}
			denied := request(mode, "bob", "POST", base, `{"product_id":"superwagie","amount":"1000000"}`, mode+"-denied")
			if denied.Code != 403 || !strings.Contains(denied.Body.String(), `"forbidden"`) {
				t.Fatalf("foreign actor admission: %d %s", denied.Code, denied.Body)
			}
			created := request(mode, "alice", "POST", base, `{"product_id":"superwagie","amount":"1000000"}`, mode+"-allowed")
			var order wallet.Order
			if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &order) != nil {
				t.Fatalf("allowed actor admission: %d %s", created.Code, created.Body)
			}
			denied = request(mode, "bob", "POST", base+"/"+order.ID+"/sandbox-settle", "", "")
			if denied.Code != 403 {
				t.Fatalf("foreign billing admin settled allowed actor order: %d %s", denied.Code, denied.Body)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM payment_receipts WHERE order_id=$1`, order.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("denied settlement persisted receipt: count=%d err=%v", count, err)
			}
			accepted := request(mode, "alice", "POST", base+"/"+order.ID+"/sandbox-settle", "", "")
			if accepted.Code != 202 {
				t.Fatalf("allowed actor settlement: %d %s", accepted.Code, accepted.Body)
			}
			service.ConfigureSandboxRechargeUsers(nil)
			if got := request(mode, "alice", "GET", base+"/"+order.ID, "", ""); got.Code != 200 {
				t.Fatalf("historical read denied: %d %s", got.Code, got.Body)
			}
			if got := request(mode, "alice", "POST", base, `{"product_id":"superwagie","amount":"1000000"}`, mode+"-allowed"); got.Code != 201 {
				t.Fatalf("existing create idempotency lost: %d %s", got.Code, got.Body)
			}
			service.ConfigureSandboxRechargeUsers([]string{alice.ID})
		})
	}
	// A valid payment signature cannot replace the persisted order actor policy.
	created := request("console", "alice", "POST", base, `{"product_id":"superwagie","amount":"1000000"}`, "webhook-policy")
	var order wallet.Order
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &order) != nil {
		t.Fatal(created.Body)
	}
	receipt := wallet.Receipt{Merchant: "sandbox", OrderID: order.ID, TransactionID: "signed-policy-fixture", Amount: order.Amount, Currency: order.Currency, Status: "SUCCEEDED"}
	body, _ := json.Marshal(receipt)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	webhook := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/webhooks/sandbox", bytes.NewReader(body))
		r.Header.Set("X-UniHub-Timestamp", timestamp)
		r.Header.Set("X-UniHub-Signature", wallet.SignReceipt(signingKey, timestamp, body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	service.ConfigureSandboxRechargeUsers([]string{bob.ID})
	if got := webhook(); got.Code != 403 {
		t.Fatalf("signed webhook bypassed original actor policy: %d %s", got.Code, got.Body)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payment_receipts WHERE order_id=$1`, order.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied webhook persisted receipt: count=%d err=%v", count, err)
	}
	service.ConfigureSandboxRechargeUsers([]string{alice.ID})
	if got := webhook(); got.Code != 202 {
		t.Fatalf("allowed signed webhook: %d %s", got.Code, got.Body)
	}
	service.ConfigureSandboxRechargeUsers(nil)
	if got := webhook(); got.Code != 202 {
		t.Fatalf("historical webhook replay lost: %d %s", got.Code, got.Body)
	}
}

func TestSandboxRechargeCapabilitiesStayDisabledWithoutPaymentMode(t *testing.T) {
	actor := "12345678-1234-1234-1234-1234567890ab"
	service := wallet.New(nil, nil, noLedger{})
	service.ConfigureSandboxRechargeUsers([]string{actor})
	s := &api{wallet: service, options: Options{Sandbox: false}}
	w := httptest.NewRecorder()
	if err := s.capabilities(w, httptest.NewRequest("GET", "/v1/capabilities", nil), accounts.User{ID: actor}); err != nil {
		t.Fatal(err)
	}
	var caps struct {
		Sandbox bool `json:"sandbox"`
		Product bool `json:"product_wallet_recharge"`
		Unified bool `json:"unified_wallet_recharge"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Sandbox || caps.Product || caps.Unified {
		t.Fatalf("disabled payment mode advertises recharge: %s", w.Body)
	}
}
