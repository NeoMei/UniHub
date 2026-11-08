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
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestProductFinanceOrderUpgradeAndIsolation(t *testing.T) {
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
	policy := products.Client{ClientID: "sw-finance", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"accounts:read", "orders:read", "orders:write", "recharges:write", "credits:read"}}
	limited := products.Client{ClientID: "sw-limited", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"accounts:read", "orders:read"}}
	services := map[string]RuntimeService{
		"write":   {Token: strings.Repeat("w", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: policy.ClientID, AllowedOps: []string{"finance:read", "finance:write"}},
		"read":    {Token: strings.Repeat("r", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: policy.ClientID, AllowedOps: []string{"finance:read"}},
		"limited": {Token: strings.Repeat("l", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: limited.ClientID, AllowedOps: []string{"finance:read", "finance:write"}},
	}
	// Real external payment remains unavailable; existing funded wallets may buy.
	h := New(verifier{}, a, wallet.New(p, a, l), Options{Billing: b, ProductUsers: users, ProductAuth: pa, ProductAccess: map[string]products.Client{policy.ClientID: policy, limited.ClientID: limited}, ProductRuntime: services})
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
	for _, suffix := range []string{"/orders", "/quotes/quote-test/accept"} {
		for _, keys := range [][]string{nil, {""}, {"one", "two"}, {"invalid key"}} {
			w := request("write", owner.ActorToken, path+suffix, `{}`, keys...)
			if w.Code != http.StatusForbidden {
				t.Fatalf("missing/ambiguous mutation key accepted on %s: %d %s", suffix, w.Code, w.Body)
			}
		}
	}
	for _, suffix := range []string{"/orders", "/quotes", "/quotes/quote-test/accept"} {
		for _, service := range []string{"read", "limited"} {
			w := request(service, owner.ActorToken, path+suffix, `{}`, "denied")
			if w.Code != http.StatusForbidden {
				t.Fatalf("service/scope bypass %s %s: %d %s", service, suffix, w.Code, w.Body)
			}
		}
	}
	for _, tc := range []struct{ actor, body string }{
		{other.ActorToken, `{"offer_id":"sandbox-sw-month-1"}`},
		{owner.ActorToken, `{"offer_id":"sandbox-addon"}`},
	} {
		w := request("write", tc.actor, path+"/orders", tc.body, "isolation")
		if w.Code != http.StatusForbidden {
			t.Fatalf("account/product scope bypass: %d %s", w.Code, w.Body)
		}
	}
	buy := request("write", owner.ActorToken, path+"/orders", `{"offer_id":"sandbox-sw-month-1"}`, "purchase")
	var first billing.Order
	if buy.Code != http.StatusAccepted || json.Unmarshal(buy.Body.Bytes(), &first) != nil || first.PaymentStatus != "PAID" || first.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("wallet purchase unavailable: %d %s", buy.Code, buy.Body)
	}
	replay := request("write", owner.ActorToken, path+"/orders", `{"offer_id":"sandbox-sw-month-1"}`, "purchase")
	var repeated billing.Order
	if replay.Code != http.StatusAccepted || json.Unmarshal(replay.Body.Bytes(), &repeated) != nil || repeated.ID != first.ID {
		t.Fatalf("purchase replay diverged: %d %s", replay.Code, replay.Body)
	}
	quoteResponse := request("write", owner.ActorToken, path+"/quotes", fmt.Sprintf(`{"subscription_id":%q,"offer_id":"sandbox-sw-month-2"}`, first.SubscriptionID))
	var quote billing.Quote
	if quoteResponse.Code != http.StatusCreated || json.Unmarshal(quoteResponse.Body.Bytes(), &quote) != nil || quote.ID == "" || quote.GrantAmount != "1000000" {
		t.Fatalf("upgrade quote unavailable: %d %s", quoteResponse.Code, quoteResponse.Body)
	}
	accepted := request("write", owner.ActorToken, path+"/quotes/"+quote.ID+"/accept", `{}`, "upgrade")
	var upgrade billing.Order
	if accepted.Code != http.StatusAccepted || json.Unmarshal(accepted.Body.Bytes(), &upgrade) != nil || upgrade.PaymentStatus != "PAID" || upgrade.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("upgrade acceptance unavailable: %d %s", accepted.Code, accepted.Body)
	}
	acceptedAgain := request("write", owner.ActorToken, path+"/quotes/"+quote.ID+"/accept", `{}`, "upgrade")
	if acceptedAgain.Code != http.StatusAccepted || json.Unmarshal(acceptedAgain.Body.Bytes(), &repeated) != nil || repeated.ID != upgrade.ID {
		t.Fatalf("upgrade replay diverged: %d %s", acceptedAgain.Code, acceptedAgain.Body)
	}
	stale := request("write", owner.ActorToken, path+"/quotes/"+quote.ID+"/accept", `{}`, "new-key-stale-quote")
	if stale.Code != http.StatusConflict || stale.Body.String() != "{\"error\":\"quote_invalidated\"}\n" {
		t.Fatalf("stale quote did not return pre-payment error: %d %s", stale.Code, stale.Body)
	}
	var count int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM billing_orders WHERE account_id=$1`, owner.Binding.PersonalAccountID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("replay or denied request created extra orders: count=%d err=%v", count, err)
	}
	for _, service := range []string{"write", "limited"} {
		r := httptest.NewRequest("GET", path+"/credits", nil)
		r.Header.Set("Authorization", "Bearer "+services[service].Token)
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+owner.ActorToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if service == "limited" {
			if w.Code != http.StatusForbidden {
				t.Fatalf("credits:read scope bypass: %d %s", w.Code, w.Body)
			}
			continue
		}
		var batches []map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &batches) != nil || len(batches) != 2 {
			t.Fatalf("credit batch read unavailable: %d %s", w.Code, w.Body)
		}
		for _, batch := range batches {
			if batch["kind"] != "subscription" || batch["product_id"] != "superwagie" || batch["source_scope"] != "product" {
				t.Fatalf("own subscription not projected: %+v", batch)
			}
		}
	}
	recharge := request("write", owner.ActorToken, path+"/recharges", `{"product_id":"superwagie","amount":"1000000"}`, "external-payment")
	if recharge.Code != http.StatusServiceUnavailable || !strings.Contains(recharge.Body.String(), "payment_not_configured") {
		t.Fatalf("real payment boundary changed: %d %s", recharge.Code, recharge.Body)
	}
}
