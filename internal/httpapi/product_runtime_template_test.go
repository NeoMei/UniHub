package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

// A verified source payment must be deliverable with the actual public
// deployment template. A template that omits its scope strands paid orders.
func TestRuntimeTemplateDeliversVerifiedPaymentExactlyOnce(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../config/examples/product-runtime-services.json")
	if e != nil {
		t.Fatal(e)
	}
	var services map[string]RuntimeService
	if e = json.Unmarshal(raw, &services); e != nil {
		t.Fatal(e)
	}
	ordinary := services["llmrouter"]
	ordinary.Token = strings.Repeat("p", 32)
	services["llmrouter"] = ordinary
	importer := services["llmrouter-reviewed-import"]
	importer.Token = strings.Repeat("i", 32)
	services["llmrouter-reviewed-import"] = importer
	a := accounts.New(p)
	id := accounts.Identity{Issuer: "https://template.test", Subject: "original-user"}
	u, e := a.EnsureIdentity(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	users := productusers.New(p, productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Permissions: []string{"product:use"}}}}}, id.Issuer, nil)
	binding, e := users.EnsureRuntimeBinding(ctx, "llmrouter", "default", "42", "verified-bind", id, u)
	if e != nil {
		t.Fatal(e)
	}
	l := ledger.NewFormance("http://127.0.0.1:53068", fmt.Sprintf("runtime-template-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if e = l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, l)
	h := New(runtimeVerifier{}, a, wallet.New(p, a, l), Options{Billing: b, ProductUsers: users, ProductRuntime: services})
	award := billing.RuntimeAwardRequest{ReceiptID: "verified-original-order", SourceKind: "payment", ReceiptDigest: "original-paid-receipt", Reason: "original verified source payment", LocalUserID: "42", AccountID: binding.PersonalAccountID, Amount: "25", Asset: billing.RuntimeAsset, UnitVersion: billing.RuntimeUnitVersion, SourcePayload: json.RawMessage(`{"paid_amount_minor":100,"currency":"CNY","order_id":"original-order"}`)}
	request := func(token string, want int) {
		t.Helper()
		body, _ := json.Marshal(award)
		r := httptest.NewRequest("POST", "/internal/v1/product-runtime/awards", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("verified receipt status %d want %d: %s", w.Code, want, w.Body)
		}
	}
	request(importer.Token, http.StatusForbidden)
	request(ordinary.Token, http.StatusOK)
	request(ordinary.Token, http.StatusOK)
	available, e := l.Balance(ctx, billing.RuntimeAddress(billing.RuntimeScope{ProductID: "llmrouter", InstanceID: "default"}, binding.PersonalAccountID, "available"), billing.RuntimeAsset)
	if e != nil || available != 25 {
		t.Fatalf("payment delivery balance %d: %v", available, e)
	}
}
