package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/modelaccess"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests exercise the public and trusted-service HTTP contracts against
// isolated real PostgreSQL and Formance state. Only OIDC identity verification
// uses the existing test fixtures: this is not Casdoor or live-gateway acceptance.
const billingModesServiceToken = "acceptance-only-llmrouter-service-credential-over-32-bytes"

type billingModesFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	ledger  *ledger.Formance
	billing *billing.Service
	wallet  *wallet.Service
	server  *httptest.Server
	client  *http.Client
	actor   accounts.User
	account string
}

func newBillingModesFixture(t *testing.T) *billingModesFixture {
	t.Helper()
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
	actor, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice", Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	l := ledger.NewFormance(base, fmt.Sprintf("billing-modes-http-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if err = l.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	b := billing.New(p, a, l)
	b.ConfigureSandbox(true)
	b.ConfigureOperators([]string{actor.ID})
	b.ConfigureTrustedServices([]string{"llmrouter"})
	w := wallet.New(p, a, l)
	clientPolicy := products.Client{
		ClientID: "sw", ProductID: "superwagie", BillingProducts: []string{"superwagie", "llmrouter"},
		Scopes: []string{"accounts:read", "catalog:read", "subscriptions:read", "credits:read", "wallets:read", "usage:read", "model-credentials:write", "recharges:read", "recharges:write", "operations:read", "orders:read", "orders:write", "refunds:write"},
	}
	server := httptest.NewServer(New(verifier{}, a, w, Options{
		Sandbox: true, Merchant: "sandbox", SigningKey: []byte("acceptance-only-payment-signing-key-over-32-bytes"),
		Billing: b, ModelAccess: modelaccess.New(p, a, b), ServiceToken: billingModesServiceToken,
		OperatorVerifier: verifier{}, ProductVerifiers: map[string]auth.Verifier{"sw": anyProductVerifier{}}, ProductAccess: map[string]products.Client{"sw": clientPolicy},
	}))
	t.Cleanup(server.Close)
	f := &billingModesFixture{t: t, pool: p, ledger: l, billing: b, wallet: w, server: server, client: server.Client(), actor: actor}
	f.client.Timeout = 30 * time.Second
	var as []accounts.Account
	f.request("GET", "/v1/accounts", "product-alice", nil, "", http.StatusOK, &as)
	if len(as) != 1 || as[0].Role != "owner" {
		t.Fatalf("personal account ownership: %+v", as)
	}
	f.account = as[0].ID
	var me accounts.User
	f.request("GET", "/v1/me", "product-alice", nil, "", http.StatusOK, &me)
	if me.ID != actor.ID {
		t.Fatal("product and console identities did not resolve to the same account owner")
	}
	return f
}

func (f *billingModesFixture) request(method, path, token string, body any, key string, want int, out any) []byte {
	f.t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(data))
	if err != nil {
		f.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err = io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if response.StatusCode != want {
		f.t.Fatalf("%s %s: status=%d body=%s; want %d", method, path, response.StatusCode, data, want)
	}
	if response.StatusCode == http.StatusNoContent {
		return data
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		f.t.Fatalf("%s %s returned non-JSON content", method, path)
	}
	if out != nil {
		if err = json.Unmarshal(data, out); err != nil {
			f.t.Fatalf("%s %s: invalid JSON: %v", method, path, err)
		}
	}
	return data
}

func (f *billingModesFixture) profile(preset string) billing.BillingProfile {
	f.t.Helper()
	var presets map[string]billing.BillingProfile
	f.request("GET", "/v1/billing-presets", "product-alice", nil, "", 200, &presets)
	if len(presets) != 3 {
		f.t.Fatalf("presets: %+v", presets)
	}
	var old billing.BillingProfile
	f.request("GET", "/v1/products/superwagie/billing-profile", "product-alice", nil, "", 200, &old)
	p, ok := presets[preset]
	if !ok {
		f.t.Fatalf("missing preset %s", preset)
	}
	p.ProductID, p.Version = "superwagie", old.Version
	var saved billing.BillingProfile
	f.request("PUT", "/ops/v1/products/superwagie/billing-profile", "alice", p, "", 200, &saved)
	if saved.Version != old.Version+1 || saved.ProductID != "superwagie" {
		f.t.Fatalf("profile version: %+v", saved)
	}
	return saved
}

func (f *billingModesFixture) recharge(product, amount, key string) {
	f.t.Helper()
	path := "/v1/accounts/" + f.account
	var order wallet.Order
	f.request("POST", path+"/recharges", "alice", map[string]string{"product_id": product, "amount": amount}, key, 201, &order)
	if order.Amount != amount || order.Currency != "CNY" || order.ProductID != product || order.OperationID != nil {
		f.t.Fatalf("unsettled sandbox order: %+v", order)
	}
	var op wallet.Operation
	f.request("POST", path+"/recharges/"+order.ID+"/sandbox-settle", "alice", nil, "", 202, &op)
	// The payment endpoint durably queues a command; run the normal wallet worker
	// explicitly, then observe completion through HTTP instead of minting money.
	if err := f.wallet.Process(context.Background(), op.ID); err != nil {
		f.t.Fatal(err)
	}
	var applied wallet.Operation
	f.request("GET", path+"/operations/"+op.ID, "alice", nil, "", 200, &applied)
	if applied.Status != "APPLIED" || applied.TransactionID == nil || *applied.TransactionID == "" {
		f.t.Fatalf("sandbox recharge was not ledger-confirmed: %+v", applied)
	}
	var replay wallet.Operation
	f.request("POST", path+"/recharges/"+order.ID+"/sandbox-settle", "alice", nil, "", 202, &replay)
	if replay.ID != op.ID {
		f.t.Fatal("sandbox settlement replay created a new command")
	}
}

func (f *billingModesFixture) rate(id, asset, numerator string) billing.Rate {
	f.t.Helper()
	r := billing.Rate{ID: id, Model: "acceptance-model", Asset: asset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: numerator, Denominator: "1"}, Output: billing.Rational{Numerator: "0", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}
	var published billing.Rate
	f.request("POST", "/ops/v1/rates", "alice", r, "", 201, &published)
	return published
}

func (f *billingModesFixture) credential(mode string) modelaccess.Grant {
	f.t.Helper()
	var grant modelaccess.Grant
	f.request("POST", "/v1/accounts/"+f.account+"/model-credentials", "product-alice", modelaccess.Request{Models: []string{"acceptance-model"}, TaskID: "acceptance-task", FundingMode: mode}, "", 201, &grant)
	if grant.AccountID != f.account || grant.ActorID != f.actor.ID || grant.OriginProductID != "superwagie" || grant.Audience != "llmrouter" {
		f.t.Fatalf("credential payer/origin was not bound: %+v", grant.Claims)
	}
	return grant
}

func billingModesReserve(grant modelaccess.Grant, id, rate, amount string) map[string]string {
	return map[string]string{"credential": grant.Token, "call_id": id, "request_digest": "digest-" + id, "rate_id": rate, "amount": amount, "task_id": "acceptance-task"}
}

func (f *billingModesFixture) reserve(grant modelaccess.Grant, id, rate, amount string) billing.Call {
	f.t.Helper()
	var call billing.Call
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, billingModesReserve(grant, id, rate, amount), "", 200, &call)
	if call.AccountID != f.account || call.ActorID != f.actor.ID || call.OriginProductID != "superwagie" || call.FundingProductID != "llmrouter" || call.Status != "RESERVED" {
		f.t.Fatalf("reserved call payer/state: %+v", call)
	}
	return call
}

func (f *billingModesFixture) settle(id, input string) billing.Call {
	f.t.Helper()
	var call billing.Call
	f.request("POST", "/internal/v1/calls/"+id+"/settle", billingModesServiceToken, map[string]any{"usage": billing.Usage{Input: input, Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}, "provider_cost": "0"}, "", 200, &call)
	if call.Status != "SETTLED" {
		f.t.Fatalf("unsettled call: %+v", call)
	}
	return call
}

func (f *billingModesFixture) balance(product string, want int64) {
	f.t.Helper()
	n, err := f.ledger.Balance(context.Background(), wallet.Address(f.account, product), billing.CashAsset)
	if err != nil || n != want {
		f.t.Fatalf("%q CNY balance=%d; want %d: %v", product, n, want, err)
	}
}

func TestComposableBillingPrepaidCashHTTPAcceptance(t *testing.T) {
	f := newBillingModesFixture(t)
	profile := f.profile("prepaid")
	if profile.Subscription.Enabled || profile.IncludedCredits.Enabled || profile.Addons.Enabled || profile.Usage.RequireSubscription {
		t.Fatalf("prepaid unexpectedly requires membership: %+v", profile)
	}
	// Product clients can read authorized commercial metadata; only deployment
	// operators can append versions, and optimistic versions reject stale writes.
	f.request("GET", "/v1/products/agentwiki/billing-profile", "product-alice", nil, "", 403, nil)
	f.request("PUT", "/ops/v1/products/superwagie/billing-profile", "product-alice", profile, "", 403, nil)
	f.request("PUT", "/ops/v1/products/superwagie/billing-profile", "bob", profile, "", 403, nil)
	stale := profile
	stale.Version = 0
	f.request("PUT", "/ops/v1/products/superwagie/billing-profile", "alice", stale, "", 409, nil)
	creditRate := f.rate("http-credit-rate", billing.CreditAsset, "2")
	cashRate := f.rate("http-cash-rate", billing.CashAsset, "1")
	var rates []billing.Rate
	f.request("GET", "/internal/v1/rates", billingModesServiceToken, nil, "", 200, &rates)
	if len(rates) != 2 || creditRate.Model != cashRate.Model || creditRate.Asset != "CREDIT/6" || cashRate.Asset != "CNY/6" {
		t.Fatalf("same-model denominated rates: %+v", rates)
	}
	path := "/v1/accounts/" + f.account
	var subscriptions []billing.Subscription
	f.request("GET", path+"/subscriptions", "product-alice", nil, "", 200, &subscriptions)
	if len(subscriptions) != 0 {
		t.Fatal("prepaid fixture has a subscription")
	}
	f.recharge("superwagie", "1000000", "source-cash")
	f.recharge("", "7000000", "unified-cash")
	f.recharge("llmrouter", "1000000", "router-cash")
	grant := f.credential("")
	if grant.FundingMode != "prepaid_cash" || grant.Asset != "CNY/6" || grant.BillingProfileVersion != 1 {
		t.Fatalf("prepaid credential denomination: %+v", grant.Claims)
	}
	f.request("POST", path+"/model-credentials", "bob", modelaccess.Request{OriginProductID: "superwagie", Models: []string{"acceptance-model"}, TaskID: "acceptance-task"}, "", 403, nil)
	f.request("POST", path+"/model-credentials", "product-alice", modelaccess.Request{OriginProductID: "agentwiki", Models: []string{"acceptance-model"}, TaskID: "acceptance-task"}, "", 403, nil)
	f.request("POST", path+"/model-credentials", "product-alice", modelaccess.Request{FundingMode: "credits", Models: []string{"acceptance-model"}, TaskID: "acceptance-task"}, "", 403, nil)
	body := billingModesReserve(grant, "cash-main", cashRate.ID, "80")
	f.request("POST", "/internal/v1/calls/reserve", grant.Token, body, "", 401, nil)
	body["account_id"] = "untrusted-payer"
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, body, "", 400, nil)
	delete(body, "account_id")
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, billingModesReserve(grant, "cash-wrong-rate", creditRate.ID, "80"), "", 403, nil)
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, billingModesReserve(grant, "cash-insufficient", cashRate.ID, "1000001"), "", 402, nil)
	call := f.reserve(grant, "cash-main", cashRate.ID, "80")
	if call.ChargeAsset != "CNY/6" || call.FundingMode != "prepaid_cash" || call.BillingProfileVersion != 1 || call.Held != "80" {
		t.Fatalf("cash hold snapshot: %+v", call)
	}
	f.balance("llmrouter", 999920)
	f.balance("superwagie", 1000000)
	f.balance("", 7000000)
	// Replaying an identical reserve and settlement cannot create another debit.
	f.reserve(grant, "cash-main", cashRate.ID, "80")
	f.request("POST", "/internal/v1/calls/cash-main/settle", "product-alice", map[string]any{"usage": billing.Usage{Input: "30", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}, "provider_cost": "0"}, "", 401, nil)
	for range 2 {
		call = f.settle("cash-main", "30")
		if call.Charged != "30" || call.ChargeAsset != "CNY/6" {
			t.Fatalf("cash settlement denomination: %+v", call)
		}
	}
	f.balance("llmrouter", 999970)
	f.balance("superwagie", 1000000)
	f.balance("", 7000000)
	if n, err := f.ledger.Balance(context.Background(), billing.CashHoldAddress(f.account, "cash-main"), "CNY/6"); err != nil || n != 0 {
		t.Fatalf("settled cash hold=%d: %v", n, err)
	}
	if n, err := f.ledger.Balance(context.Background(), "commercial:revenue:llmrouter", "CNY/6"); err != nil || n != 30 {
		t.Fatalf("router revenue=%d: %v", n, err)
	}
	f.reserve(grant, "cash-released", cashRate.ID, "40")
	var topped billing.Call
	f.request("POST", "/internal/v1/calls/cash-released/topup", billingModesServiceToken, map[string]string{"amount": "20"}, "topup-once", 200, &topped)
	if topped.Held != "60" || topped.ChargeAsset != "CNY/6" {
		t.Fatalf("cash topup: %+v", topped)
	}
	f.balance("llmrouter", 999910)
	var released billing.Call
	f.request("POST", "/internal/v1/calls/cash-released/release", billingModesServiceToken, map[string]string{"reason": "provider_failed"}, "", 200, &released)
	if released.Status != "RELEASED" || released.Charged != "0" || released.ChargeAsset != "CNY/6" {
		t.Fatalf("cash release: %+v", released)
	}
	f.balance("llmrouter", 999970)
	var receipt billing.Call
	f.request("GET", "/internal/v1/calls/cash-main", billingModesServiceToken, nil, "", 200, &receipt)
	if receipt.ChargeAsset != "CNY/6" || receipt.Charged != "30" || receipt.AccountID != f.account || receipt.FundingProductID != "llmrouter" {
		t.Fatalf("trusted receipt: %+v", receipt)
	}
	var detail billing.CallView
	data := f.request("GET", path+"/calls/cash-main", "product-alice", nil, "", 200, &detail)
	if detail.Asset != "CNY/6" || detail.Charged != "30" || detail.TaskID != "acceptance-task" || strings.Contains(string(data), "credential") || strings.Contains(string(data), "provider_cost") {
		t.Fatalf("public call detail: %s", data)
	}
	f.request("GET", path+"/calls/cash-main", "bob", nil, "", 403, nil)
	f.request("GET", path+"/calls?origin_product_id=agentwiki", "product-alice", nil, "", 403, nil)
	var first billing.CallPage
	f.request("GET", path+"/calls?asset=CNY%2F6&limit=1", "product-alice", nil, "", 200, &first)
	if first.Total != "2" || len(first.Items) != 1 || first.Items[0].Asset != "CNY/6" || first.NextCursor == "" {
		t.Fatalf("cash pagination: %+v", first)
	}
	var next billing.CallPage
	f.request("GET", path+"/calls?cursor="+url.QueryEscape(first.NextCursor)+"&limit=1", "product-alice", nil, "", 200, &next)
	if len(next.Items) != 1 || next.Items[0].CallID == first.Items[0].CallID || next.Items[0].Asset != "CNY/6" || next.HasMore {
		t.Fatalf("cash continuation: %+v", next)
	}
	f.request("GET", path+"/calls?cursor="+url.QueryEscape(first.NextCursor)+"&asset=CREDIT%2F6&limit=1", "product-alice", nil, "", 400, nil)
	var summary billing.UsageSummary
	f.request("GET", path+"/usage/summary?asset=CNY%2F6", "product-alice", nil, "", 200, &summary)
	if summary.Asset != "CNY/6" || summary.Charged != "30" || summary.SettledCalls != "1" || summary.ReleasedCalls != "1" {
		t.Fatalf("cash summary: %+v", summary)
	}
	var resources struct {
		BillingProfile billing.BillingProfile `json:"billing_profile"`
		Wallets        []wallet.Balance       `json:"wallets"`
		Credits        struct {
			Spendable string `json:"spendable"`
		} `json:"credits"`
	}
	f.request("GET", path+"/resources", "product-alice", nil, "", 200, &resources)
	seenCash := false
	for _, balance := range resources.Wallets {
		if balance.ProductID == "llmrouter" && balance.Asset == "CNY/6" && balance.Amount == "999970" {
			seenCash = true
		}
	}
	if resources.BillingProfile.Usage.DefaultFundingMode != "prepaid_cash" || !seenCash || resources.Credits.Spendable != "0" {
		t.Fatalf("prepaid resources: %+v", resources)
	}
	// A new product configuration stops new holds; already accepted calls still
	// settle with their recorded denomination, rate, and profile version.
	f.reserve(grant, "cash-before-disable", cashRate.ID, "40")
	f.profile("subscription")
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, billingModesReserve(grant, "cash-after-disable", cashRate.ID, "1"), "", 403, nil)
	old := f.settle("cash-before-disable", "10")
	if old.ChargeAsset != "CNY/6" || old.Charged != "10" || old.FundingMode != "prepaid_cash" || old.BillingProfileVersion != 1 {
		t.Fatalf("in-flight cash was repriced by current profile: %+v", old)
	}
	f.balance("llmrouter", 999960)
	f.balance("superwagie", 1000000)
	f.balance("", 7000000)
}

func (f *billingModesFixture) offer(id, period, credits string) billing.Offer {
	f.t.Helper()
	offer := billing.Offer{ID: id, ProductID: "superwagie", Name: "HTTP acceptance membership", AccountKind: "both", Kind: "membership", Period: period, Price: "1000000", MonthlyCredit: credits, Rank: 1, Entitlements: map[string]string{"workspace": "enabled"}, UpgradeRule: "remaining_term_prorata_v1"}
	f.request("POST", "/ops/v1/offers", "alice", offer, "", 201, nil)
	var published billing.Offer
	f.request("POST", "/ops/v1/offers/"+id+"/publish", "alice", map[string]any{}, "", 200, &published)
	if published.Status != "published" {
		f.t.Fatalf("unpublished offer: %+v", published)
	}
	return published
}

func (f *billingModesFixture) purchase(offer, key string) billing.Order {
	f.t.Helper()
	var order billing.Order
	f.request("POST", "/v1/accounts/"+f.account+"/orders", "product-alice", map[string]string{"offer_id": offer}, key, 202, &order)
	if order.PaymentStatus != "PAID" || order.FulfillmentStatus != "FULFILLED" || order.ProductFunding != "1000000" || order.UnifiedFunding != "0" {
		f.t.Fatalf("membership payment/fulfillment: %+v", order)
	}
	return order
}

// This catches a credit call silently switching to a funded cash wallet and a
// report accidentally summing micro-CNY and credits into one monetary total.
func TestComposableBillingHybridNeverFallsBackAndReportsMixedAssetsHTTP(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("hybrid")
	creditRate := f.rate("hybrid-credit-rate", "CREDIT/6", "2")
	cashRate := f.rate("hybrid-cash-rate", "CNY/6", "1")
	f.recharge("superwagie", "3000000", "hybrid-membership-cash")
	f.recharge("llmrouter", "1000000", "hybrid-cash-not-fallback")
	f.offer("hybrid-month", "month", "100")
	order := f.purchase("hybrid-month", "hybrid-month-buy")
	if order.GrantAmount != "100" {
		t.Fatalf("included credit grant: %+v", order)
	}
	grant := f.credential("")
	if grant.FundingMode != "credits" || grant.Asset != "CREDIT/6" || grant.BillingProfileVersion != 1 {
		t.Fatalf("legacy-compatible default grant: %+v", grant.Claims)
	}
	path := "/v1/accounts/" + f.account
	f.request("POST", path+"/model-credentials", "product-alice", modelaccess.Request{Models: []string{"acceptance-model"}, TaskID: "acceptance-task", FundingMode: "prepaid_cash"}, "", 403, nil)
	call := f.reserve(grant, "hybrid-credit-call", creditRate.ID, "100")
	if call.ChargeAsset != "CREDIT/6" || call.FundingMode != "credits" {
		t.Fatalf("hybrid hold denomination: %+v", call)
	}
	call = f.settle("hybrid-credit-call", "3")
	if call.Charged != "6" || call.ChargeAsset != "CREDIT/6" {
		t.Fatalf("credit calculation: %+v", call)
	}
	var batches []billing.Batch
	f.request("GET", path+"/credits", "alice", nil, "", 200, &batches)
	if len(batches) != 1 || batches[0].Available != "94" || batches[0].Consumed != "6" || batches[0].Reserved != "0" {
		t.Fatalf("credit ledger after settlement: %+v", batches)
	}
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, billingModesReserve(grant, "hybrid-credit-exhausted", creditRate.ID, "95"), "", 402, nil)
	f.balance("llmrouter", 1000000)
	f.balance("superwagie", 2000000)
	f.profile("prepaid")
	// A paid order replay belongs to its immutable original commitment even when
	// the current profile has stopped selling subscriptions.
	replayedOrder := f.purchase("hybrid-month", "hybrid-month-buy")
	if replayedOrder.ID != order.ID || replayedOrder.GrantAmount != "100" {
		t.Fatalf("profile change rewrote a paid order replay: %+v", replayedOrder)
	}
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, billingModesReserve(grant, "old-credit-disabled", creditRate.ID, "1"), "", 403, nil)
	cashGrant := f.credential("prepaid_cash")
	f.reserve(cashGrant, "hybrid-then-cash", cashRate.ID, "40")
	call = f.settle("hybrid-then-cash", "7")
	if call.Charged != "7" || call.ChargeAsset != "CNY/6" || call.BillingProfileVersion != 2 {
		t.Fatalf("explicit cash-mode call: %+v", call)
	}
	f.balance("llmrouter", 999993)
	f.balance("superwagie", 2000000)
	var summary billing.UsageSummary
	raw := f.request("GET", path+"/usage/summary", "product-alice", nil, "", 200, &summary)
	if summary.Asset != "mixed" || summary.Charged != "" || summary.ChargesByAsset["CNY/6"] != "7" || summary.ChargesByAsset["CREDIT/6"] != "6" || summary.SettledCalls != "2" {
		t.Fatalf("mixed-asset HTTP summary: %s", raw)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["charged"]; exists {
		t.Fatal("mixed-asset response exposed a misleading single amount")
	}
	f.request("GET", path+"/usage/summary?asset=CREDIT%2F6", "product-alice", nil, "", 200, &summary)
	if summary.Asset != "CREDIT/6" || summary.Charged != "6" || summary.CallsTotal != "1" {
		t.Fatalf("credit-filtered summary: %+v", summary)
	}
	f.request("GET", path+"/usage/summary?asset=CNY%2F6", "product-alice", nil, "", 200, &summary)
	if summary.Asset != "CNY/6" || summary.Charged != "7" || summary.CallsTotal != "1" {
		t.Fatalf("cash-filtered summary: %+v", summary)
	}
}

// A zero included-credit offer sells service rights, without minting a zero
// batch/ledger command, granting model access, or assuming rights are refundable.
func TestComposableBillingPureSubscriptionZeroCreditsHTTPAcceptance(t *testing.T) {
	for _, period := range []string{"month", "year"} {
		t.Run(period, func(t *testing.T) {
			f := newBillingModesFixture(t)
			f.profile("subscription")
			f.rate("pure-credit-rate", "CREDIT/6", "2")
			f.rate("pure-cash-rate", "CNY/6", "1")
			f.recharge("superwagie", "3000000", "pure-cash")
			offer := f.offer("pure-"+period, period, "0")
			order := f.purchase(offer.ID, "pure-buy")
			if order.GrantAmount != "0" || order.SubscriptionID == "" || !order.End.After(order.Start) {
				t.Fatalf("pure rights fulfillment: %+v", order)
			}
			path := "/v1/accounts/" + f.account
			var subs []billing.Subscription
			f.request("GET", path+"/subscriptions", "product-alice", nil, "", 200, &subs)
			if len(subs) != 1 || subs[0].Status != "active" || subs[0].Offer.MonthlyCredit != "0" || subs[0].Offer.Entitlements["workspace"] != "enabled" || !subs[0].PaidThrough.Equal(order.End) {
				t.Fatalf("pure service rights: %+v", subs)
			}
			firstEnd := subs[0].PaidThrough
			// Exercise ordinary maintenance both within a paid term and at renewal.
			if _, err := f.billing.Maintain(context.Background(), subs[0].Anchor.AddDate(0, 0, 20)); err != nil {
				t.Fatal(err)
			}
			if period == "year" {
				if _, err := f.billing.Maintain(context.Background(), subs[0].Anchor.AddDate(0, 3, 0)); err != nil {
					t.Fatal(err)
				}
			}
			f.request("PUT", path+"/settings", "alice", map[string]any{"product_id": "superwagie", "purpose": "renewal", "enabled": true, "version": 0, "max_per_operation": "1000000", "max_total": "10000000", "expires_at": firstEnd.AddDate(2, 0, 0)}, "", 200, nil)
			if _, err := f.billing.Maintain(context.Background(), firstEnd.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			f.request("GET", path+"/subscriptions", "product-alice", nil, "", 200, &subs)
			if len(subs) != 1 || !subs[0].PaidThrough.After(firstEnd) || subs[0].Offer.MonthlyCredit != "0" {
				t.Fatalf("pure renewal: %+v", subs)
			}
			var batches []billing.Batch
			f.request("GET", path+"/credits", "product-alice", nil, "", 200, &batches)
			if len(batches) != 0 {
				t.Fatalf("pure membership minted %d credit batches", len(batches))
			}
			var forbiddenCommands int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billing_commands bc WHERE account_id=$1 AND (kind='monthly' OR EXISTS (SELECT 1 FROM jsonb_array_elements(command->'postings') p WHERE p->>'asset'='CREDIT/6' OR (p->>'amount')::bigint<=0))`, f.account).Scan(&forbiddenCommands); err != nil {
				t.Fatal(err)
			}
			if forbiddenCommands != 0 {
				t.Fatalf("pure membership emitted %d credit/zero ledger commands", forbiddenCommands)
			}
			// Empty durable fulfillment records carry the database rights update.
			// They must never become zero-value transactions in the real ledger.
			rows, err := f.pool.Query(context.Background(), `SELECT command,COALESCE(ledger_transaction_id,'') FROM billing_commands WHERE account_id=$1 AND jsonb_array_length(command->'postings')=0`, f.account)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var data []byte
				var transactionID string
				if err = rows.Scan(&data, &transactionID); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				var command ledger.Command
				if err = json.Unmarshal(data, &command); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				found, lookupErr := f.ledger.Find(context.Background(), command.Reference)
				if lookupErr != nil || found != nil || transactionID != "" {
					rows.Close()
					t.Fatalf("pure fulfillment sent an empty ledger transaction: found=%v transaction=%s err=%v", found != nil, transactionID, lookupErr)
				}
			}
			if err = rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
			for _, mode := range []string{"", "credits", "prepaid_cash"} {
				f.request("POST", path+"/model-credentials", "product-alice", modelaccess.Request{Models: []string{"acceptance-model"}, TaskID: "acceptance-task", FundingMode: mode}, "", 403, nil)
			}
			f.balance("superwagie", 1000000)
			var orders []billing.Order
			f.request("GET", path+"/orders", "product-alice", nil, "", 200, &orders)
			refundOrder := ""
			for _, saved := range orders {
				if saved.Kind == "renewal" && saved.GrantAmount == "0" && saved.FulfillmentStatus == "FULFILLED" {
					refundOrder = saved.ID
				}
			}
			if len(orders) != 2 || refundOrder == "" {
				t.Fatalf("pure renewal did not create exactly one paid zero-credit order: %+v", orders)
			}
			f.request("POST", path+"/orders/"+refundOrder+"/refund", "product-alice", map[string]any{}, "pure-refund", 409, nil)
			f.request("GET", path+"/orders", "product-alice", nil, "", 200, &orders)
			manual := false
			for _, saved := range orders {
				if saved.ID == refundOrder && saved.RefundStatus == "MANUAL_REVIEW" && saved.RefundReason == "service_entitlements_require_review" {
					manual = true
				}
			}
			if !manual {
				t.Fatalf("pure refund did not persist manual review: %+v", orders)
			}
			f.balance("superwagie", 1000000)
		})
	}
}

func TestComposableBillingPaidHybridYearKeepsMonthlyGrantsAfterProfileChangeHTTP(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("hybrid")
	f.recharge("superwagie", "2000000", "annual-membership-cash")
	f.offer("hybrid-year", "year", "100")
	order := f.purchase("hybrid-year", "hybrid-year-buy")
	path := "/v1/accounts/" + f.account
	var batches []billing.Batch
	f.request("GET", path+"/credits", "alice", nil, "", 200, &batches)
	if len(batches) != 1 || batches[0].Original != "100" || batches[0].Available != "100" {
		t.Fatalf("annual purchase prepaid all future credits: %+v", batches)
	}
	f.profile("prepaid")
	if _, err := f.billing.Maintain(context.Background(), order.Start.AddDate(0, 1, 0).Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.request("GET", path+"/credits", "alice", nil, "", 200, &batches)
	if len(batches) != 2 {
		t.Fatalf("paid annual terms did not issue the next monthly batch: %+v", batches)
	}
	for _, batch := range batches {
		if batch.Original != "100" || batch.OrderID != order.ID {
			t.Fatalf("paid annual grant was repriced: %+v", batch)
		}
	}
	var subscriptions []billing.Subscription
	f.request("GET", path+"/subscriptions", "product-alice", nil, "", 200, &subscriptions)
	if len(subscriptions) != 1 || subscriptions[0].Status != "active" || !subscriptions[0].PaidThrough.Equal(order.End) || subscriptions[0].Offer.MonthlyCredit != "100" {
		t.Fatalf("component change revoked paid annual rights: %+v", subscriptions)
	}
}

func TestComposableBillingCashTeamMemberPrivacyHTTPAcceptance(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("prepaid")
	f.rate("team-cash-rate", "CNY/6", "1")
	var bob accounts.User
	f.request("GET", "/v1/me", "bob", nil, "", 200, &bob)
	var team accounts.Account
	f.request("POST", "/v1/accounts", "alice", map[string]string{"name": "Cash acceptance team"}, "", 201, &team)
	f.account = team.ID
	path := "/v1/accounts/" + team.ID
	var invitation accounts.Invitation
	f.request("POST", path+"/invitations", "alice", map[string]string{"user_id": bob.ID, "role": "member"}, "", 201, &invitation)
	f.request("POST", "/v1/invitations/"+invitation.ID+"/accept", "bob", nil, "", 200, nil)
	f.recharge("llmrouter", "1000000", "team-router-cash")
	ownerGrant := f.credential("")
	f.reserve(ownerGrant, "team-owner-call", "team-cash-rate", "10")
	f.settle("team-owner-call", "3")
	var memberGrant modelaccess.Grant
	f.request("POST", path+"/model-credentials", "product-bob", modelaccess.Request{Models: []string{"acceptance-model"}, TaskID: "acceptance-task", FundingMode: "prepaid_cash"}, "", 201, &memberGrant)
	if memberGrant.ActorID != bob.ID || memberGrant.AccountID != team.ID || memberGrant.Asset != "CNY/6" {
		t.Fatalf("member credential ownership: %+v", memberGrant.Claims)
	}
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, billingModesReserve(memberGrant, "team-member-call", "team-cash-rate", "10"), "", 200, nil)
	f.settle("team-member-call", "5")
	var summary billing.UsageSummary
	raw := f.request("GET", path+"/usage/summary", "product-bob", nil, "", 200, &summary)
	if summary.Scope.Visibility != "actor" || summary.Scope.ActorID != bob.ID || summary.CallsTotal != "1" || summary.FinancialDetails || summary.Charged != "" || len(summary.ChargesByAsset) != 0 || strings.Contains(string(raw), `"charged"`) {
		t.Fatalf("ordinary member received team financial totals: %s", raw)
	}
	var detail billing.CallView
	raw = f.request("GET", path+"/calls/team-member-call", "product-bob", nil, "", 200, &detail)
	if detail.Asset != "CNY/6" || detail.FinancialDetails || detail.Charged != "" || detail.Held != "" || strings.Contains(string(raw), `"charged"`) {
		t.Fatalf("ordinary member received call financial fields: %s", raw)
	}
	f.request("GET", path+"/calls/team-owner-call", "product-bob", nil, "", 404, nil)
	var resources map[string]json.RawMessage
	f.request("GET", path+"/resources", "product-bob", nil, "", 200, &resources)
	if _, exists := resources["wallets"]; exists {
		t.Fatal("ordinary member received account cash balances")
	}
	if _, exists := resources["billing_profile"]; !exists {
		t.Fatal("ordinary member lost public billing policy")
	}
	f.request("POST", path+"/recharges", "product-bob", map[string]string{"product_id": "llmrouter", "amount": "1000000"}, "forbidden-member-recharge", 403, nil)
	f.request("GET", path+"/wallets", "product-bob", nil, "", 403, nil)
	f.request("PATCH", path+"/members/"+bob.ID, "alice", map[string]string{"role": "billing_admin"}, "", 204, nil)
	f.request("GET", path+"/usage/summary", "product-bob", nil, "", 200, &summary)
	if summary.Scope.Visibility != "account" || !summary.FinancialDetails || summary.CallsTotal != "2" || summary.Asset != "CNY/6" || summary.Charged != "8" {
		t.Fatalf("current finance role did not reveal the correct CNY account total: %+v", summary)
	}
	f.balance("llmrouter", 999992)
}
