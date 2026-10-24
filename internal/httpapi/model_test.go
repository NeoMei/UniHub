package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/modelaccess"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestModelHTTPUsesBoundPayerAndOnlyServiceCanSettle(t *testing.T) {
	base := os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if base == "" {
		t.Skip("UNIHUB_TEST_LEDGER_URL unset")
	}
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
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	account := as[0].ID
	l := ledger.NewFormance(base, fmt.Sprintf("model-api-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if e = l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = l.Post(ctx, ledger.Command{Reference: "fund-test", Postings: []ledger.Posting{{Source: "world", Destination: wallet.Address(account, "superwagie"), Asset: wallet.Asset, Amount: 10000000}}}); e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, l)
	b.ConfigureSandbox(true)
	b.ConfigureOperators([]string{u.ID})
	b.ConfigureTrustedServices([]string{"llmrouter"})
	if _, e = b.SeedSandbox(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Purchase(ctx, u.ID, account, billing.PurchaseRequest{OfferID: "sandbox-sw-month-1", Key: "buy"}); e != nil {
		t.Fatal(e)
	}
	rate := billing.Rate{ID: "test-rate-1", Model: "synthetic-model", Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "1", Denominator: "2"}, Output: billing.Rational{Numerator: "1", Denominator: "2"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}
	if _, e = b.PublishRate(ctx, u.ID, rate); e != nil {
		t.Fatal(e)
	}
	const serviceKey = "test-only-router-service-credential-at-least-32-bytes"
	h := New(verifier{}, a, wallet.New(p, a, l), Options{Billing: b, ModelAccess: modelaccess.New(p, a, b), ServiceToken: serviceKey})
	path := "/v1/accounts/" + account + "/model-credentials"
	w := call(h, "POST", path, "bob", `{"origin_product_id":"superwagie","models":["synthetic-model"],"task_id":"test-job"}`, "")
	if w.Code != 403 {
		t.Fatalf("cross account token %d", w.Code)
	}
	w = call(h, "POST", path, "alice", `{"origin_product_id":"superwagie","models":["synthetic-model"],"task_id":"test-job"}`, "")
	var grant modelaccess.Grant
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &grant) != nil {
		t.Fatalf("model token issuance %d", w.Code)
	}
	body := map[string]string{"credential": grant.Token, "call_id": "model-call-1", "request_digest": "stable-body-digest", "rate_id": rate.ID, "amount": "100", "task_id": "test-job"}
	encode := func(v any) string {
		out, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return string(out)
	}
	if w = call(h, "POST", "/internal/v1/calls/reserve", grant.Token, encode(body), ""); w.Code != 401 {
		t.Fatalf("model token can reserve service API %d", w.Code)
	}
	body["account_id"] = "untrusted-other"
	if w = call(h, "POST", "/internal/v1/calls/reserve", serviceKey, encode(body), ""); w.Code != 400 {
		t.Fatalf("payer override accepted %d", w.Code)
	}
	delete(body, "account_id")
	body["task_id"] = "other-job"
	if w = call(h, "POST", "/internal/v1/calls/reserve", serviceKey, encode(body), ""); w.Code != 403 {
		t.Fatalf("task override accepted %d", w.Code)
	}
	body["task_id"] = "test-job"
	for range 2 {
		w = call(h, "POST", "/internal/v1/calls/reserve", serviceKey, encode(body), "")
		if w.Code != 200 {
			t.Fatalf("reserve %d %s", w.Code, w.Body)
		}
	}
	settlement := `{"usage":{"input":"3","output":"4","cache_read":"0","cache_write":"0","reasoning":"0"},"provider_cost":"0"}`
	if w = call(h, "POST", "/internal/v1/calls/model-call-1/settle", "alice", settlement, ""); w.Code != 401 {
		t.Fatalf("user can settle %d", w.Code)
	}
	for range 2 {
		w = call(h, "POST", "/internal/v1/calls/model-call-1/settle", serviceKey, settlement, "")
		var c billing.Call
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil || c.Charged != "4" {
			t.Fatalf("settle %d %s", w.Code, w.Body)
		}
	}
	if amount, e := l.Balance(ctx, wallet.Address(account, "superwagie"), wallet.Asset); e != nil || amount != 9000000 {
		t.Fatalf("model request changed product cash %d %v", amount, e)
	}
	batches, e := b.Batches(ctx, u.ID, account)
	if e != nil || len(batches) != 1 || batches[0].Available != "999996" || batches[0].Consumed != "4" || batches[0].Reserved != "0" {
		t.Fatalf("credit totals %+v %v", batches, e)
	}
	if w = call(h, "DELETE", path+"/"+grant.ID, "alice", "", ""); w.Code != 204 {
		t.Fatalf("revoke %d", w.Code)
	}
	body["call_id"] = "revoked-call"
	if w = call(h, "POST", "/internal/v1/calls/reserve", serviceKey, encode(body), ""); w.Code != 403 {
		t.Fatalf("revoked credential accepted %d", w.Code)
	}
}
