package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

type operatorVerifier struct{}

func (operatorVerifier) Verify(ctx context.Context, token string) (auth.Claims, error) {
	if token != "ops-alice" && token != "ops-bob" {
		return auth.Claims{}, errors.New("wrong operator audience")
	}
	return auth.Claims{Issuer: "https://test", Subject: token[4:], Name: token[4:]}, nil
}

func billingFixture(t *testing.T) (http.Handler, *billing.Service, accounts.Account, accounts.User) {
	t.Helper()
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice", Name: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, noLedger{})
	b.ConfigureSandbox(true)
	b.ConfigureOperators([]string{u.ID})
	b.ConfigureTrustedServices([]string{"llmrouter"})
	if _, e = b.SeedSandbox(ctx); e != nil {
		t.Fatal(e)
	}
	return New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, OperatorVerifier: operatorVerifier{}, ServiceToken: "trusted-model-service-key-at-least-32-characters"}), b, as[0], u
}
func TestCommercialRoutesValidateAccountAndMutationBoundary(t *testing.T) {
	h, _, a, _ := billingFixture(t)
	if w := call(h, "GET", "/v1/catalog", "alice", "", ""); w.Code != 200 {
		t.Fatalf("catalog: %d %s", w.Code, w.Body)
	}
	for _, suffix := range []string{"subscriptions", "orders", "credits", "settings"} {
		if w := call(h, "GET", "/v1/accounts/"+a.ID+"/"+suffix, "bob", "", ""); w.Code != 403 {
			t.Fatalf("crossaccount %s: %d", suffix, w.Code)
		}
	}
	for _, body := range []string{`{"offer_id":"sandbox-sw-month-1","amount":"1"}`, `{"offer_id":"sandbox-sw-month-1","account_id":"another"}`, `{"offer_id":1}`} {
		if w := call(h, "POST", "/v1/accounts/"+a.ID+"/orders", "alice", body, "test"); w.Code != 400 {
			t.Fatalf("invalid order: %d %s", w.Code, w.Body)
		}
	}
	if w := call(h, "POST", "/v1/accounts/"+a.ID+"/orders", "alice", `{"offer_id":"sandbox-sw-month-1"}`, ""); w.Code != 400 {
		t.Fatalf("missing key %d", w.Code)
	}
}
func TestOperatorAndModelServiceAreNotAccountOwnerPermissions(t *testing.T) {
	h, _, _, _ := billingFixture(t)
	if w := call(h, "GET", "/ops/v1/offers", "ops-bob", "", ""); w.Code != 403 {
		t.Fatalf("nonoperator %d", w.Code)
	}
	if w := call(h, "GET", "/ops/v1/offers", "alice", "", ""); w.Code != 401 {
		t.Fatalf("user audience granted operator access: %d", w.Code)
	}
	w := call(h, "GET", "/ops/v1/offers", "ops-alice", "", "")
	var offers []billing.Offer
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &offers) != nil || len(offers) == 0 {
		t.Fatalf("operatorcatalog: %d %s", w.Code, w.Body)
	}
	for _, token := range []string{"", "alice", "bob"} {
		if w := call(h, "GET", "/internal/v1/rates", token, "", ""); w.Code != 401 {
			t.Fatalf("ordinarytoken %q service status %d", token, w.Code)
		}
	}
	if w := call(h, "GET", "/internal/v1/rates", "trusted-model-service-key-at-least-32-characters", "", ""); w.Code != 200 {
		t.Fatalf("service rates %d %s", w.Code, w.Body)
	}
}
