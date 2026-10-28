package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/platformadmin"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestAdminOperationsHTTPRealAuthorityAndDomains(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(pool)
	alice, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	bob, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if e != nil {
		t.Fatal(e)
	}
	for role, id := range map[string]string{"finance_admin": alice.ID, "user_admin": bob.ID} {
		raw, _ := json.Marshal([]string{role})
		if _, e = pool.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,$2,'test',$1)`, id, raw); e != nil {
			t.Fatal(e)
		}
	}
	admin := platformadmin.New(pool, nil)
	b := billing.New(pool, a, nil)
	h := New(verifier{}, a, wallet.New(pool, a, noLedger{}), Options{PlatformAdmin: admin, Billing: b, ProductVerifiers: map[string]auth.Verifier{"sw": anyProductVerifier{}}})
	for _, resource := range []string{"overview", "overview/timeseries", "accounts", "api-keys", "calls", "transactions", "recharges", "orders", "refunds", "subscriptions", "credit-batches", "products", "offers", "rates", "billing-profiles", "usage-budgets", "audit-events", "system-status"} {
		w := call(h, "GET", "/admin/v1/"+resource, "alice", "", "")
		if w.Code != 200 {
			t.Errorf("%s => %d %s", resource, w.Code, w.Body)
		}
		w = call(h, "GET", "/admin/v1/"+resource, "product-alice", "", "")
		if w.Code != 403 {
			t.Errorf("product accessed %s: %d %s", resource, w.Code, w.Body)
		}
	}
	for _, resource := range []string{"orders", "refunds", "calls", "api-keys", "usage-budgets", "transactions", "billing-profiles"} {
		w := call(h, "GET", "/admin/v1/"+resource, "bob", "", "")
		if w.Code != 403 {
			t.Errorf("useradmin finance %s %d %s", resource, w.Code, w.Body)
		}
	}
	wts := call(h, "GET", "/admin/v1/overview/timeseries", "bob", "", "")
	if wts.Code != 200 {
		t.Errorf("useradmin timeseries %d %s", wts.Code, wts.Body)
	}
	for _, path := range []string{"/admin/v1/accounts?limit=101", "/admin/v1/accounts?limit=bad", "/admin/v1/accounts?from=yesterday", "/admin/v1/accounts?cursor=invalid", "/admin/v1/accounts?from=2026-10-28T00:00:00Z&to=2026-10-27T00:00:00Z"} {
		w := call(h, "GET", path, "alice", "", "")
		if w.Code != 400 {
			t.Errorf("invalid list %s => %d %s", path, w.Code, w.Body)
		}
	}
	create := `{"offer":{"id":"http-draft","product_id":"llmrouter","name":"Test","account_kind":"both","kind":"addon","price":"10000","monthly_credit":"100","validity_months":1},"version":"0","reason":"test draft"}`
	w := call(h, "POST", "/admin/v1/offers", "alice", create, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(h, "POST", "/admin/v1/offers", "bob", create, "")
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(h, "GET", "/admin/v1/offers", "alice", "", "")
	var page platformadmin.Page[map[string]any]
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	version := page.Items[0]["version"]
	w = call(h, "POST", "/admin/v1/offers/http-draft/publish", "alice", fmt.Sprintf(`{"version":%q,"reason":"publish test"}`, version), "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = call(h, "POST", "/admin/v1/offers/http-draft/publish", "alice", fmt.Sprintf(`{"version":%q,"reason":"stale publish"}`, version), "")
	if w.Code != 409 {
		t.Fatal("version bypass", w.Code, w.Body)
	}
	w = call(h, "GET", "/admin/v1/exports?resource=accounts", "bob", "", "")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatal(w.Code, w.Body)
	}
	w = call(h, "GET", "/admin/v1/exports?resource=orders", "bob", "", "")
	if w.Code != 403 {
		t.Fatal("export bypass", w.Code, w.Body)
	}
	w = call(h, "GET", "/admin/v1/call-attempts", "alice", "", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "supplier_attempt_telemetry_unavailable") {
		t.Fatal("attempt telemetry fabricated", w.Code, w.Body)
	}
	if _, e = pool.Exec(ctx, `UPDATE platform_admin_roles SET roles='[]' WHERE user_id=$1`, alice.ID); e != nil {
		t.Fatal(e)
	}
	w = call(h, "GET", "/admin/v1/orders", "alice", "", "")
	if w.Code != 403 {
		t.Fatal("role revocation ineffective", w.Code, w.Body)
	}
}
