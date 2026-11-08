package httpapi

import (
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
	"unihub/internal/payments"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestProductFinanceExternalCheckoutIsolation(t *testing.T) {
	base := os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if base == "" {
		t.Skip("UNIHUB_TEST_LEDGER_URL unset")
	}
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	reg := productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}}}
	users := productusers.New(p, reg, "https://hub.test", nil)
	pa := productauth.New(p, a, users, "https://hub.test")
	actorFor := func(local string) productauth.Identity {
		t.Helper()
		registered, err := pa.Register(ctx, "superwagie", "default", productauth.Registration{LocalUserID: local, OperationID: "register-" + local, Username: local, AuthVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		actor, err := pa.Authenticate(ctx, "superwagie", "default", productauth.Authentication{LocalUserID: local, AuthenticationID: "auth-" + local, Method: "session", AuthenticatedAt: time.Now().Unix(), AuthVersion: registered.AuthVersion})
		if err != nil {
			t.Fatal(err)
		}
		return actor
	}
	owner, other := actorFor("owner"), actorFor("other")
	l := ledger.NewFormance(base, fmt.Sprintf("finance-orders-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if err := l.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	b := billing.New(p, a, l)
	b.ConfigureSandbox(true)
	b.ConfigureOperators([]string{owner.Binding.UserID})
	if _, err := b.SeedSandbox(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PublishRate(ctx, owner.Binding.UserID, billing.Rate{ID: "finance-orders-rate", Model: "fixture-model", Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "1", Denominator: "1"}, Output: billing.Rational{Numerator: "1", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Post(ctx, ledger.Command{Reference: "fund", Postings: []ledger.Posting{{Source: "world", Destination: wallet.Address(owner.Binding.PersonalAccountID, "superwagie"), Asset: wallet.Asset, Amount: 10000000}}}); err != nil {
		t.Fatal(err)
	}
	policy := products.Client{ClientID: "sw-finance", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"accounts:read", "orders:read", "orders:write", "recharges:write", "credits:read", "refunds:write"}}
	limited := products.Client{ClientID: "sw-limited", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"accounts:read", "orders:read"}}
	services := map[string]RuntimeService{
		"write":   {Token: strings.Repeat("w", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: policy.ClientID, AllowedOps: []string{"finance:read", "finance:write"}},
		"read":    {Token: strings.Repeat("r", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: policy.ClientID, AllowedOps: []string{"finance:read"}},
		"limited": {Token: strings.Repeat("l", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: limited.ClientID, AllowedOps: []string{"finance:read", "finance:write"}},
	}
	provider := &checkoutHTTPProvider{}
	h := New(verifier{}, a, wallet.New(p, a, l), Options{PaymentProviders: []payments.Provider{provider}, Billing: b, ProductUsers: users, ProductAuth: pa, ProductAccess: map[string]products.Client{policy.ClientID: policy, limited.ClientID: limited}, ProductRuntime: services})
	path := "/internal/v1/product-finance/accounts/" + owner.Binding.PersonalAccountID
	request := func(service, actor, url, body string, keys ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", url, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+services[service].Token)
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
		for _, key := range keys {
			r.Header.Add("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct{ service, actor, body string }{
		{"read", owner.ActorToken, `{"offer_id":"sandbox-sw-month-1","provider":"wechat"}`},
		{"limited", owner.ActorToken, `{"offer_id":"sandbox-sw-month-1","provider":"wechat"}`},
		{"write", other.ActorToken, `{"offer_id":"sandbox-sw-month-1","provider":"wechat"}`},
		{"write", owner.ActorToken, `{"offer_id":"sandbox-addon","provider":"wechat"}`},
	} {
		w := request(tc.service, tc.actor, path+"/checkouts", tc.body, "denied")
		if w.Code != 403 {
			t.Fatalf("isolation %d %s", w.Code, w.Body)
		}
	}
	for _, keys := range [][]string{nil, {""}, {"one", "two"}, {"bad key"}} {
		w := request("write", owner.ActorToken, path+"/checkouts", `{"offer_id":"sandbox-sw-month-1","provider":"wechat"}`, keys...)
		if w.Code != 403 {
			t.Fatalf("invalid key %d %s", w.Code, w.Body)
		}
	}
	w := request("write", owner.ActorToken, path+"/checkouts", `{"offer_id":"sandbox-sw-month-1","provider":"wechat","amount":"1"}`, "bad-amount")
	if w.Code != 400 {
		t.Fatalf("client amount accepted %d %s", w.Code, w.Body)
	}
	w = request("write", owner.ActorToken, path+"/checkouts", `{"offer_id":"sandbox-sw-month-1","provider":"wechat"}`, "valid")
	if w.Code != 202 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	var x billing.Checkout
	if err := json.Unmarshal(w.Body.Bytes(), &x); err != nil {
		t.Fatal(err)
	}
	w = request("write", owner.ActorToken, path+"/checkouts", `{"offer_id":"sandbox-sw-month-1","provider":"wechat"}`, "valid")
	if w.Code != 202 || provider.creates != 1 {
		t.Fatalf("replay %d %s creates=%d", w.Code, w.Body, provider.creates)
	}
	for _, action := range []string{"reconcile", "close", "refund"} {
		body := `{}`
		if action == "refund" {
			body = `{"reason":"unused"}`
		}
		w = request("write", other.ActorToken, path+"/checkouts/"+x.ID+"/"+action, body, "denied")
		if w.Code != 403 {
			t.Fatalf("checkout actor isolation %s %d %s", action, w.Code, w.Body)
		}
	}
	provider.result = payments.Result{OrderID: x.ID, TransactionID: "http-payment", Amount: x.Amount, Currency: "CNY", Status: "PAID", PaidAt: time.Now()}
	notify := httptest.NewRequest("POST", "/payments/wechat/notify", strings.NewReader("fixture"))
	notify.Header.Set("X-Fixture-Verified", "yes")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, notify)
	if out.Code != 200 {
		t.Fatalf("callback %d %s", out.Code, out.Body)
	}
	w = request("write", owner.ActorToken, path+"/checkouts/"+x.ID+"/reconcile", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"fulfillment_status":"FULFILLED"`) {
		t.Fatalf("settled %d %s", w.Code, w.Body)
	}
	// An unverified callback never receives an ACK.
	out = httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("POST", "/payments/wechat/notify", strings.NewReader("fixture")))
	if out.Code != 400 {
		t.Fatal("unverified callback accepted")
	}
}

type checkoutHTTPProvider struct {
	creates int
	result  payments.Result
}

func (*checkoutHTTPProvider) ID() string         { return "wechat" }
func (*checkoutHTTPProvider) MerchantID() string { return "http-fixture" }
func (p *checkoutHTTPProvider) Create(_ context.Context, r payments.Request) (payments.Session, error) {
	p.creates++
	return payments.Session{Kind: "qr", URL: "weixin://wxpay/bizpayurl?pr=fixture", ExpiresAt: r.ExpiresAt}, nil
}
func (p *checkoutHTTPProvider) Query(context.Context, string) (payments.Result, error) {
	return p.result, nil
}
func (*checkoutHTTPProvider) Close(context.Context, string) error { return nil }
func (p *checkoutHTTPProvider) VerifyNotification(h http.Header, _ []byte) (payments.Result, error) {
	if h.Get("X-Fixture-Verified") != "yes" {
		return payments.Result{}, payments.ErrVerification
	}
	return p.result, nil
}
func (*checkoutHTTPProvider) Refund(context.Context, payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{}, payments.ErrUnavailable
}
func (*checkoutHTTPProvider) QueryRefund(context.Context, payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{}, payments.ErrUnavailable
}
