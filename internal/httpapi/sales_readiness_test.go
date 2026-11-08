package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestCreditSalesReadinessErrorContract(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, billing.ErrCreditRatesUnavailable)
	if w.Code != 409 || w.Body.String() != "{\"error\":\"credit_rates_unavailable\"}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestAddonSubscriptionRequiredErrorContract(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, billing.ErrSubscriptionRequired)
	if w.Code != 409 || w.Body.String() != "{\"error\":\"subscription_required\"}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestUpgradeQuoteInvalidatedErrorContract(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{billing.ErrQuoteInvalidated, "quote_invalidated"},
		{errors.Join(billing.ErrQuoteInvalidated, billing.ErrConflict), "quote_invalidated"},
		{billing.ErrConflict, "conflict"},
		{billing.ErrPending, "operation_pending"},
	} {
		w := httptest.NewRecorder()
		writeError(w, tc.err)
		if w.Code != 409 || w.Body.String() != "{\"error\":\""+tc.code+"\"}\n" {
			t.Fatalf("error=%v status=%d body=%s", tc.err, w.Code, w.Body)
		}
	}
}

func TestCreditSalesCatalogArrayAndPurchaseContract(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	ac, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, noLedger{})
	b.ConfigureSandbox(true)
	if _, e = b.SeedSandbox(ctx); e != nil {
		t.Fatal(e)
	}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, Sandbox: true})
	w := call(h, "GET", "/v1/catalog", "alice", "", "")
	var offers []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &offers) != nil || len(offers) != 5 {
		t.Fatalf("catalog shape: %d %s", w.Code, w.Body)
	}
	for _, o := range offers {
		r, ok := o["sales_readiness"].(map[string]any)
		if !ok || r["ready"] != false || r["reason"] != "credit_rates_unavailable" {
			t.Fatalf("readiness=%+v", o)
		}
	}
	w = call(h, "POST", "/v1/accounts/"+ac[0].ID+"/orders", "alice", `{"offer_id":"sandbox-sw-month-1"}`, "blocked")
	if w.Code != 409 || w.Body.String() != "{\"error\":\"credit_rates_unavailable\"}\n" {
		t.Fatalf("purchase wire: %d %s", w.Code, w.Body)
	}
	var n int
	if e = p.QueryRow(ctx, `SELECT count(*) FROM billing_orders`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("orders=%d %v", n, e)
	}
}
