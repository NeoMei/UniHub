package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestRuntimeCashRequiresExplicitServiceMandateForAllCallOperations(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	identity := accounts.Identity{Issuer: "https://runtime.test", Subject: "cash-owner"}
	u, err := a.EnsureIdentity(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	users := productusers.New(p, productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Permissions: []string{"product:use"}}}}}, identity.Issuer, nil)
	binding, err := users.EnsureRuntimeBinding(ctx, "llmrouter", "default", "42", "cash-bind", identity, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,product_role,version) VALUES($1,'llmrouter','default','["member"]',true,1,1)`, u.ID); err != nil {
		t.Fatal(err)
	}
	l := ledger.NewFormance("http://127.0.0.1:53068", fmt.Sprintf("runtime-cash-http-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if err = l.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Post(ctx, ledger.Command{Reference: "cash-fixture", Postings: []ledger.Posting{{Source: "world", Destination: wallet.Address(binding.PersonalAccountID, ""), Asset: wallet.Asset, Amount: 100}}}); err != nil {
		t.Fatal(err)
	}
	b := billing.New(p, a, l)
	old := RuntimeService{Token: "legacy-service-over-thirty-two-test-characters", ProductID: "llmrouter", InstanceID: "default", AllowedOps: []string{"billing:read", "billing:write", "calls:refund"}}
	cash := old
	cash.Token = "cash-service-over-thirty-two-test-characters"
	cash.AllowedOps = append(append([]string{}, old.AllowedOps...), "cash:charge")
	payments := RuntimeService{Token: "payments-service-over-thirty-two-test-characters", ProductID: "llmrouter", InstanceID: "default", AllowedOps: []string{"cash:payment"}}
	h := New(nil, a, wallet.New(p, a, l), Options{Billing: b, ProductUsers: users, ProductRuntime: map[string]RuntimeService{"old": old, "cash": cash, "payments": payments}})
	request := func(method, path, token string, body any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "/internal/v1/product-runtime"+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.Bytes()
	}
	in := billing.RuntimeReserveRequest{CallID: "cash-call", LocalUserID: "42", LocalKeyID: "lr-key", AccountID: binding.PersonalAccountID, RequestDigest: "request", Model: "lr-model", Operation: "chat", Asset: wallet.Asset, UnitVersion: billing.RuntimeCashUnitVersion, PriceVersion: "lr-price", PricingDigest: "price", Amount: "30"}
	request("POST", "/calls/reserve", old.Token, in, 403)
	request("POST", "/calls/reserve", cash.Token, in, 200)
	for _, suffix := range []string{"", "/topup", "/settle", "/release", "/refund"} {
		method := "POST"
		if suffix == "" {
			method = "GET"
		}
		request(method, "/calls/cash-call"+suffix, old.Token, map[string]string{}, 403)
	}
	path := "/users/42/balance?account_id=" + binding.PersonalAccountID + "&asset=CNY%2F6&unit_version=lr-cny-v1"
	request("GET", path, old.Token, nil, 403)
	var balance billing.RuntimeBalance
	if err = json.Unmarshal(request("GET", path, cash.Token, nil, 200), &balance); err != nil {
		t.Fatal(err)
	}
	if balance.Asset != wallet.Asset || balance.Available != "70" || balance.Held != "30" {
		t.Fatalf("wrong cash balance: %+v", balance)
	}
	request("POST", "/calls/cash-call/release", cash.Token, billing.RuntimeReleaseRequest{Reason: "request failed"}, 200)
	payload := json.RawMessage(`{"order_id":1,"trade_no":"confirmed-order","provider":"wechat_pay","provider_trade_no":"provider-receipt","currency":"CNY","paid_amount_minor":7}`)
	digest := sha256.Sum256(payload)
	payment := billing.RuntimeAwardRequest{ReceiptID: "confirmed-order", SourceKind: "payment", ReceiptDigest: hex.EncodeToString(digest[:]), Reason: "verified provider receipt", LocalUserID: "42", AccountID: binding.PersonalAccountID, Amount: "70000", Asset: wallet.Asset, UnitVersion: billing.RuntimeCashUnitVersion, SourcePayload: payload}
	request("POST", "/awards", cash.Token, payment, 403)
	request("POST", "/awards", old.Token, payment, 403)
	for range 2 {
		request("POST", "/awards", payments.Token, payment, 200)
	}
	if err = json.Unmarshal(request("GET", path, cash.Token, nil, 200), &balance); err != nil {
		t.Fatal(err)
	}
	if balance.Available != "70100" {
		t.Fatalf("verified payment was not credited exactly once: %+v", balance)
	}
	payment.ReceiptID = "amount-mismatch"
	payment.Amount = "70001"
	request("POST", "/awards", payments.Token, payment, 400)
	payment.SourceKind = "signup"
	request("POST", "/awards", payments.Token, payment, 403)
}
