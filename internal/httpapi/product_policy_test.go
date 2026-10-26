package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestExplicitProductScopesDoNotInheritLegacyRoutes(t *testing.T) {
	for _, tc := range []struct {
		pattern, scope string
		want           bool
	}{
		{"GET /v1/me", "accounts:read", true},
		{"GET /v1/accounts/{account}/wallets", "wallets:read", true},
		{"POST /v1/accounts", "accounts:write", true},
		{"DELETE /v1/accounts/{account}/invitations/{invitation}", "members:write", true},
		{"POST /v1/accounts/{account}/orders", "orders:write", true},
		{"GET /v1/accounts/{account}/credits", "accounts:read", false},
		{"POST /ops/v1/offers", "orders:write", false},
		{"DELETE /v1/accounts/{account}/unknown", "members:write", false},
	} {
		c := products.Client{ProductID: "superwagie", Scopes: []string{tc.scope}}
		if got := productScopeAllowed(tc.pattern, c); got != tc.want {
			t.Errorf("%s with %s = %v", tc.pattern, tc.scope, got)
		}
	}
}

func TestExplicitProductListsAndOriginAreIsolated(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	b.ConfigureSandbox(true)
	if _, err := b.SeedSandbox(ctx); err != nil {
		t.Fatal(err)
	}
	c := products.Client{ProductID: "superwagie", ClientID: "sw", Scopes: []string{"accounts:read", "catalog:read", "wallets:read", "orders:write", "recharges:write", "members:write"}, Origins: []string{"https://sw.test"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, Sandbox: true, Merchant: "sandbox", ProductVerifiers: map[string]auth.Verifier{"sw": productVerifier{}}, ProductAccess: map[string]products.Client{"sw": c}})
	w := call(h, "GET", "/v1/accounts", "product-alice", "", "")
	var as []accounts.Account
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &as) != nil || len(as) != 1 {
		t.Fatalf("accounts %d %s", w.Code, w.Body)
	}
	account := as[0].ID
	w = call(h, "GET", "/v1/catalog", "product-alice", "", "")
	var offers []billing.Offer
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &offers) != nil || len(offers) != 4 {
		t.Fatalf("catalog %d %s", w.Code, w.Body)
	}
	for _, o := range offers {
		if o.ProductID != "superwagie" {
			t.Fatalf("foreign offer leaked %#v", o)
		}
	}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/v1/accounts/" + account + "/credits", "", 403},
		{"GET", "/v1/accounts/not-mine/wallets", "", 403},
		{"POST", "/v1/accounts/" + account + "/orders", `{"offer_id":"sandbox-addon"}`, 403},
		{"POST", "/v1/accounts/" + account + "/recharges", `{"product_id":"agentwiki","amount":"1000000"}`, 403},
	} {
		w = call(h, tc.method, tc.path, "product-alice", tc.body, "test")
		if w.Code != tc.want {
			t.Fatalf("%s => %d %s", tc.path, w.Code, w.Body)
		}
	}
	for _, tc := range []struct {
		origin string
		want   int
	}{{"https://sw.test", 200}, {"https://other.test", 403}, {"https://sw.test.evil", 403}, {"null", 403}, {"", 200}} {
		r := httptest.NewRequest("GET", "/v1/accounts", nil)
		r.Header.Set("Authorization", "Bearer product-alice")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("origin %q => %d", tc.origin, w.Code)
		}
	}
}

func TestProductCallQueriesBindOriginAndValidateWindow(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	c := products.Client{ProductID: "superwagie", ClientID: "sw", Scopes: []string{"accounts:read", "usage:read"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"sw": productVerifier{}}, ProductAccess: map[string]products.Client{"sw": c}})
	w := call(h, "GET", "/v1/accounts", "product-alice", "", "")
	var as []accounts.Account
	if json.Unmarshal(w.Body.Bytes(), &as) != nil || len(as) != 1 {
		t.Fatalf("accounts %s", w.Body)
	}
	user, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"query-1", "query-2", "query-3"} {
		v := billing.Call{CallID: id, AccountID: as[0].ID, ActorID: user.ID, OriginProductID: "superwagie", Rate: billing.Rate{Model: "model-x"}, Status: "SETTLED", Charged: "3", Held: "5", Usage: &billing.Usage{Input: "2", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}}
		raw, _ := json.Marshal(v)
		if _, e = p.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'private',$3)`, id, as[0].ID, raw); e != nil {
			t.Fatal(e)
		}
	}

	base := "/v1/accounts/" + as[0].ID
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/calls", 200}, {"/usage/summary", 200}, {"/calls?origin_product_id=agentwiki", 403},
		{"/calls?limit=0", 400}, {"/calls?limit=101", 400}, {"/calls?from=bad", 400},
		{"/calls?from=2026-01-01T00:00:00Z&to=2026-10-01T00:00:00Z", 400},
		{"/calls?from=2026-10-01T00:00:00Z&to=2026-09-01T00:00:00Z", 400},
		{"/calls?actor_id=another", 400},
	} {
		w = call(h, "GET", base+tc.path, "product-alice", "", "")
		if w.Code != tc.status {
			t.Fatalf("%s => %d %s", tc.path, w.Code, w.Body)
		}
	}
	w = call(h, "GET", base+"/calls?limit=1", "product-alice", "", "")
	var first billing.CallPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || first.Total != "3" || !first.HasMore || len(first.Items) != 1 {
		t.Fatalf("first page %d %s", w.Code, w.Body)
	}
	w = call(h, "GET", base+"/calls?limit=1&cursor="+url.QueryEscape(first.NextCursor), "product-alice", "", "")
	var second billing.CallPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &second) != nil || len(second.Items) != 1 || second.Items[0].CallID == first.Items[0].CallID || !second.Filter.To.Equal(first.Filter.To) {
		t.Fatalf("cursor continuation %d %s", w.Code, w.Body)
	}
	if w = call(h, "GET", base+"/calls?limit=1&model=changed&cursor="+url.QueryEscape(first.NextCursor), "product-alice", "", ""); w.Code != 400 {
		t.Fatalf("changed cursor filter %d", w.Code)
	}

	if w = call(h, "GET", "/v1/accounts/not-mine/calls", "product-alice", "", ""); w.Code != 403 {
		t.Fatalf("foreign calls %d", w.Code)
	}
}

type anyProductVerifier struct{}

func (anyProductVerifier) Verify(ctx context.Context, raw string) (auth.Claims, error) {
	if raw == "product-bob" {
		return (verifier{}).Verify(ctx, "bob")
	}
	return (productVerifier{}).Verify(ctx, raw)
}

func TestProductFinancialScopeStillRequiresDomainRoleAndFiltersLists(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	alice, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	bob, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if e != nil {
		t.Fatal(e)
	}
	team, e := a.CreateTeam(ctx, alice.ID, "product team")
	if e != nil {
		t.Fatal(e)
	}
	invite, e := a.Invite(ctx, alice.ID, team.ID, bob.ID, "member")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.AcceptInvitation(ctx, bob.ID, invite.ID); e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, noLedger{})
	for _, product := range []string{"superwagie", "agentwiki"} {
		o := billing.Order{ID: product + "-order", AccountID: team.ID, ActorID: alice.ID, ProductID: product, CreatedAt: time.Now().UTC(), Offer: billing.Offer{ProductID: product}, PaymentStatus: "PAID", FulfillmentStatus: "FULFILLED"}
		raw, _ := json.Marshal(o)
		if _, e = p.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,$1,'test',$4)`, o.ID, team.ID, alice.ID, raw); e != nil {
			t.Fatal(e)
		}
		sub := billing.Subscription{ID: product + "-sub", AccountID: team.ID, ProductID: product, Offer: o.Offer}
		raw, _ = json.Marshal(sub)
		if _, e = p.Exec(ctx, `INSERT INTO billing_subscriptions(id,account_id,product_id,snapshot) VALUES($1,$2,$3,$4)`, sub.ID, team.ID, product, raw); e != nil {
			t.Fatal(e)
		}
		q := billing.Quote{ID: product + "-quote", AccountID: team.ID, SubscriptionID: sub.ID, Offer: o.Offer}
		raw, _ = json.Marshal(q)
		if _, e = p.Exec(ctx, `INSERT INTO billing_quotes(id,account_id,snapshot) VALUES($1,$2,$3)`, q.ID, team.ID, raw); e != nil {
			t.Fatal(e)
		}
		auth := billing.Authorization{ProductID: product, Purpose: "funding", ActorID: alice.ID, Version: 1}
		raw, _ = json.Marshal(auth)
		if _, e = p.Exec(ctx, `INSERT INTO billing_authorizations(account_id,product_id,purpose,snapshot) VALUES($1,$2,'funding',$3)`, team.ID, product, raw); e != nil {
			t.Fatal(e)
		}
		batch := billing.Batch{ID: product + "-batch", AccountID: team.ID, ProductID: product, OrderID: o.ID, SubscriptionID: sub.ID, SourceKey: product + "-source", StartsAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now()}
		raw, _ = json.Marshal(batch)
		if _, e = p.Exec(ctx, `INSERT INTO billing_batches(id,account_id,order_id,source_key,snapshot) VALUES($1,$2,$3,$4,$5)`, batch.ID, team.ID, o.ID, batch.SourceKey, raw); e != nil {
			t.Fatal(e)
		}
	}
	c := products.Client{ProductID: "superwagie", ClientID: "sw", Scopes: []string{"accounts:read", "accounts:write", "members:read", "members:write", "wallets:read", "orders:read", "orders:write", "settings:read", "settings:write", "subscriptions:read", "credits:read", "refunds:write", "usage:read"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"sw": anyProductVerifier{}}, ProductAccess: map[string]products.Client{"sw": c}})
	base := "/v1/accounts/" + team.ID
	for _, suffix := range []string{"orders", "subscriptions", "settings"} {
		w := call(h, "GET", base+"/"+suffix, "product-alice", "", "")
		var objects []map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &objects) != nil || len(objects) != 1 || objects[0]["product_id"] != "superwagie" {
			t.Fatalf("filtered %s %d %s", suffix, w.Code, w.Body)
		}
	}
	for _, suffix := range []string{"wallets", "orders", "settings"} {
		if w := call(h, "GET", base+"/"+suffix, "product-bob", "", ""); w.Code != 403 {
			t.Fatalf("member finance %s %d", suffix, w.Code)
		}
	}
	for _, suffix := range []string{"credits", "resources"} {
		w := call(h, "GET", base+"/"+suffix, "product-bob", "", "")
		if w.Code != 200 {
			t.Fatalf("member %s %d %s", suffix, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), `"order_id"`) || strings.Contains(w.Body.String(), `"source_key"`) || strings.Contains(w.Body.String(), `"subscription_id"`) || strings.Contains(w.Body.String(), `"wallets"`) {
			t.Fatalf("foreign data %s", w.Body)
		}
	}
	for _, tc := range []struct{ path, body string }{
		{"/quotes/agentwiki-quote/accept", `{}`}, {"/quotes", `{"subscription_id":"agentwiki-sub","offer_id":"missing"}`}, {"/orders/agentwiki-order/refund", `{}`},
	} {
		w := call(h, "POST", base+tc.path, "product-alice", tc.body, "refuse")
		if w.Code != 403 && w.Code != 404 {
			t.Fatalf("foreign command %s %d %s", tc.path, w.Code, w.Body)
		}
	}
	var events int
	if e = p.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE account_id=$1`, team.ID).Scan(&events); e != nil || events != 0 {
		t.Fatalf("forbidden command sideeffect %d %v", events, e)
	}
	// Product management works in its own UI, with the original RBAC still active.
	if w := call(h, "POST", "/v1/accounts", "product-alice", `{"name":"headless team"}`, ""); w.Code != 201 {
		t.Fatalf("create team %d %s", w.Code, w.Body)
	}
	if w := call(h, "DELETE", base+"/members/"+alice.ID, "product-bob", "", ""); w.Code != 403 {
		t.Fatalf("member role escalation %d", w.Code)
	}
}

func TestMalformedDirectProductOptionsFailClosed(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	for _, c := range []products.Client{{ClientID: "sw", Scopes: []string{"accounts:read"}}, {ProductID: "superwagie", ClientID: "different", Scopes: []string{"accounts:read"}}} {
		h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{ProductVerifiers: map[string]auth.Verifier{"sw": productVerifier{}}, ProductAccess: map[string]products.Client{"sw": c}})
		if w := call(h, "GET", "/v1/accounts", "product-alice", "", ""); w.Code != 403 {
			t.Fatalf("malformed product policy granted request %d %s", w.Code, w.Body)
		}
	}
}

func TestModelPoolReportingPolicy(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	for _, tc := range []struct {
		product string
		scopes  []string
		token   string
		want    int
	}{
		{"llmrouter", []string{"accounts:read", "usage:all-products", "usage:read"}, "product-alice", 200},
		{"llmrouter", []string{"accounts:read", "usage:read"}, "product-alice", 403},
		{"superwagie", []string{"accounts:read", "usage:all-products"}, "product-alice", 403},
	} {
		c := products.Client{ProductID: tc.product, ClientID: "pool", Scopes: tc.scopes}
		h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"pool": productVerifier{}}, ProductAccess: map[string]products.Client{"pool": c}})
		w := call(h, "GET", "/v1/accounts", tc.token, "", "")
		var as []accounts.Account
		if json.Unmarshal(w.Body.Bytes(), &as) != nil || len(as) != 1 {
			t.Fatalf("accounts %s", w.Body)
		}
		for _, suffix := range []string{"/model-pool/calls", "/model-pool/usage/summary"} {
			w = call(h, "GET", "/v1/accounts/"+as[0].ID+suffix, tc.token, "", "")
			if w.Code != tc.want {
				t.Fatalf("%s %s: %d %s", tc.product, suffix, w.Code, w.Body)
			}
		}
		if w = call(h, "GET", "/v1/accounts/not-mine/model-pool/calls", tc.token, "", ""); w.Code != 403 {
			t.Fatalf("foreign: %d", w.Code)
		}
	}
}

func TestModelPoolAggregateIsolationAndCurrentRole(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	alice, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	bob, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if e != nil {
		t.Fatal(e)
	}
	team, e := a.CreateTeam(ctx, alice.ID, "pool team")
	if e != nil {
		t.Fatal(e)
	}
	inv, e := a.Invite(ctx, alice.ID, team.ID, bob.ID, "member")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.AcceptInvitation(ctx, bob.ID, inv.ID); e != nil {
		t.Fatal(e)
	}
	c := products.Client{ProductID: "llmrouter", ClientID: "pool", Scopes: []string{"usage:read", "usage:all-products"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"pool": anyProductVerifier{}}, ProductAccess: map[string]products.Client{"pool": c}})
	for i, origin := range []string{"superwagie", "agentwiki", "llmrouter"} {
		v := billing.Call{CallID: origin + "-pool-call", AccountID: team.ID, ActorID: alice.ID, OriginProductID: origin, Rate: billing.Rate{Model: "pool-model"}, Status: "SETTLED", Charged: "3", Held: "5", Usage: &billing.Usage{Input: "2", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}}
		if origin == "agentwiki" {
			v.ChargeAsset = billing.CashAsset
		}
		v.ProviderCost = "private-cost"
		v.CredentialID = "private-credential"
		raw, _ := json.Marshal(v)
		if _, e = p.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot,created_at) VALUES($1,$2,'pool',$3,$4)`, v.CallID, team.ID, raw, time.Now().Add(-time.Minute+time.Duration(i)*time.Millisecond)); e != nil {
			t.Fatal(e)
		}
	}
	base := "/v1/accounts/" + team.ID
	get := func(path, token string, status int) []byte {
		t.Helper()
		w := call(h, "GET", path, token, "", "")
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", token, path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	var first billing.CallPage
	if e = json.Unmarshal(get(base+"/model-pool/calls?limit=1", "product-alice", 200), &first); e != nil || first.Total != "3" || !first.HasMore {
		t.Fatalf("aggregate %#v %v", first, e)
	}
	cursor := url.QueryEscape(first.NextCursor)
	other, e := a.CreateTeam(ctx, alice.ID, "other pool team")
	if e != nil {
		t.Fatal(e)
	}
	get("/v1/accounts/"+other.ID+"/model-pool/calls?cursor="+cursor, "product-alice", 400)

	get(base+"/model-pool/calls?limit=1&cursor="+cursor, "product-alice", 200)
	get(base+"/model-pool/calls?limit=1&origin_product_id=superwagie&cursor="+cursor, "product-alice", 400)
	get(base+"/model-pool/calls?limit=1&asset=CNY%2F6&cursor="+cursor, "product-alice", 400)
	get("/v1/accounts/not-mine/model-pool/calls?cursor="+cursor, "product-alice", 403)
	var own billing.CallPage
	json.Unmarshal(get(base+"/calls", "product-alice", 200), &own)
	if own.Total != "1" || own.Items[0].OriginProductID != "llmrouter" {
		t.Fatalf("ordinary widened %#v", own)
	}
	json.Unmarshal(get(base+"/model-pool/calls?origin_product_id=agentwiki", "product-alice", 200), &own)
	if own.Total != "1" || own.Items[0].OriginProductID != "agentwiki" {
		t.Fatalf("filter %#v", own)
	}
	get(base+"/calls?origin_product_id=agentwiki", "product-alice", 403)
	get(base+"/model-pool/calls/agentwiki-pool-call?origin_product_id=agentwiki", "product-alice", 200)
	get(base+"/model-pool/calls/agentwiki-pool-call?origin_product_id=superwagie", "product-alice", 404)
	var summary billing.UsageSummary
	rawSummary := get(base+"/model-pool/usage/summary", "product-alice", 200)
	if e = json.Unmarshal(rawSummary, &summary); e != nil {
		t.Fatal(e)
	}
	if summary.Asset != "mixed" || summary.ChargesByAsset[billing.CreditAsset] != "6" || summary.ChargesByAsset[billing.CashAsset] != "3" {
		t.Fatalf("mixed assets %#v", summary)
	}
	if strings.Contains(string(rawSummary), "private-") {
		t.Fatalf("private data leaked %s", rawSummary)
	}
	get(base+"/model-pool/usage/summary?asset=CREDIT%2F6", "product-alice", 200)
	// Console owner and legacy product identities cannot acquire this capability.
	get(base+"/model-pool/calls", "alice", 403)
	for _, role := range []string{"member", "admin", "billing_admin"} {
		if role != "member" {
			if e = a.ChangeRole(ctx, alice.ID, team.ID, bob.ID, role); e != nil {
				t.Fatal(e)
			}
		}
		status := 403
		if role == "billing_admin" {
			status = 200
		}
		for _, suffix := range []string{"/model-pool/calls", "/model-pool/calls/agentwiki-pool-call", "/model-pool/usage/summary"} {
			get(base+suffix, "product-bob", status)
		}
	}
	// A finance cursor cannot be reused by another actor or after role removal.
	get(base+"/model-pool/calls?cursor="+cursor, "product-bob", 400)
	if e = a.ChangeRole(ctx, alice.ID, team.ID, bob.ID, "member"); e != nil {
		t.Fatal(e)
	}
	get(base+"/model-pool/calls?cursor="+cursor, "product-bob", 403)
	legacy := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"llmrouter": productVerifier{}}})
	if w := call(legacy, "GET", base+"/model-pool/calls", "product-alice", "", ""); w.Code != 403 {
		t.Fatalf("legacy %d", w.Code)
	}
}
