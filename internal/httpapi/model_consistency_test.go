package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/modelaccess"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestProductModelConsistencyReadOnlyAndScoped(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	profile := `{"product_id":"superwagie","version":1,"usage":{"enabled":true,"funding_modes":["prepaid_cash"],"default_funding_mode":"prepaid_cash","models":["present","missing"]}}`
	if _, e := p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) VALUES('superwagie',1,'deployment',$1)`, profile); e != nil {
		t.Fatal(e)
	}
	for _, r := range []billing.Rate{{ID: "present-1", Model: "present", Status: "published", Asset: billing.CashAsset}, {ID: "present-2", Model: "present", Status: "published", Asset: billing.CashAsset}, {ID: "extra", Model: "foreign", Status: "published", Asset: billing.CashAsset}, {ID: "credit", Model: "missing", Status: "published", Asset: billing.CreditAsset}} {
		raw, _ := json.Marshal(r)
		if _, e := p.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2)`, r.ID, raw); e != nil {
			t.Fatal(e)
		}
	}
	client := products.Client{ProductID: "superwagie", ClientID: "sw", Scopes: []string{"catalog:read", "model-credentials:write"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ModelAccess: modelaccess.New(p, a, b), ProductVerifiers: map[string]auth.Verifier{"sw": productVerifier{}}, ProductAccess: map[string]products.Client{"sw": client}})
	path := "/v1/products/superwagie/model-consistency"
	w := call(h, "GET", path, "product-alice", "", "")
	var got struct {
		ProductID  string         `json:"product_id"`
		Asset      string         `json:"asset"`
		Allowlist  []string       `json:"allowlist"`
		Missing    []string       `json:"missing_models"`
		Extra      []string       `json:"extra_models"`
		Duplicates []string       `json:"duplicate_models"`
		Rates      []billing.Rate `json:"published_rates"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("consistency %d %s", w.Code, w.Body)
	}
	if got.ProductID != "superwagie" || got.Asset != billing.CashAsset || len(got.Allowlist) != 2 || len(got.Missing) != 1 || got.Missing[0] != "missing" || len(got.Extra) != 1 || got.Extra[0] != "foreign" || len(got.Duplicates) != 1 || got.Duplicates[0] != "present" || len(got.Rates) != 3 {
		t.Fatalf("projection %#v", got)
	}
	if strings.Contains(w.Body.String(), "token") || strings.Contains(w.Body.String(), "credential") {
		t.Fatalf("secret-bearing projection %s", w.Body)
	}
	if w = call(h, "GET", "/v1/products/llmrouter/model-consistency", "product-alice", "", ""); w.Code != 403 {
		t.Fatalf("cross product %d", w.Code)
	}
	var n int
	if e := p.QueryRow(ctx, `SELECT count(*) FROM model_credentials`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("read issued credential %d: %v", n, e)
	}
}
