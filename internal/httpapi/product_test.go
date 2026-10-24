package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/modelaccess"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

type productVerifier struct{}

type productEligibility struct{}

func (productEligibility) Subscriptions(context.Context, string, string) ([]billing.Subscription, error) {
	return []billing.Subscription{{ProductID: "superwagie", Status: "active", PaidThrough: time.Now().Add(time.Hour)}, {ProductID: "agentwiki", Status: "active", PaidThrough: time.Now().Add(time.Hour)}}, nil
}
func (productEligibility) Rates(context.Context) ([]billing.Rate, error) {
	return []billing.Rate{{Model: "x", Status: "published"}}, nil
}

func (productVerifier) Verify(ctx context.Context, raw string) (auth.Claims, error) {
	if raw == "product-alice" {
		raw = "alice"
	} else {
		raw = "invalid"
	}
	return (verifier{}).Verify(ctx, raw)
}

func TestProductIdentityCannotBecomeFinanceOrOtherProduct(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ModelAccess: modelaccess.New(p, a, productEligibility{}), ProductVerifiers: map[string]auth.Verifier{"superwagie": productVerifier{}}})
	w := call(h, "GET", "/v1/accounts", "product-alice", "", "")
	var as []accounts.Account
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &as) != nil || len(as) != 1 {
		t.Fatalf("product accounts %d %s", w.Code, w.Body)
	}
	account := as[0].ID
	w = call(h, "POST", "/v1/accounts/"+account+"/model-credentials", "product-alice", `{"origin_product_id":"superwagie","models":["x"],"task_id":"t"}`, "")
	var grant modelaccess.Grant
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &grant) != nil || grant.OriginProductID != "superwagie" || grant.AccountID != account {
		t.Fatalf("own product grant: %d", w.Code)
	}
	for _, path := range []string{"/v1/me", "/v1/accounts/" + account + "/subscriptions", "/v1/accounts/" + account + "/credits"} {
		if w = call(h, "GET", path, "product-alice", "", ""); w.Code != 200 {
			t.Fatalf("read %s: %d", path, w.Code)
		}
	}
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/accounts", `{"name":"unintended team"}`},
		{"GET", "/v1/accounts/" + account + "/wallets", ""},
		{"POST", "/v1/accounts/" + account + "/orders", `{"offer_id":"x"}`},
		{"POST", "/v1/accounts/" + account + "/model-credentials", `{"origin_product_id":"agentwiki","models":["x"],"task_id":"t"}`},
	} {
		if w = call(h, tc.method, tc.path, "product-alice", tc.body, "test"); w.Code != 403 {
			t.Fatalf("privilege crossover %s %s => %d", tc.method, tc.path, w.Code)
		}
	}
	// Adding another Authorization field may never pick one arbitrarily.
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r.Header.Add("Authorization", "Bearer alice")
	r.Header.Add("Authorization", "Bearer product-alice")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("ambiguous bearer %d", w.Code)
	}
	if w = call(h, "GET", "/v1/accounts/"+account+"/wallets", "alice", "", ""); w.Code != 200 {
		t.Fatalf("console finance identity broken %d", w.Code)
	}
}
