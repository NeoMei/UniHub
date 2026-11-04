package httpapi

import (
	"context"
	"encoding/json"
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

func TestProductQueriesBindInstanceForCallsSummaryAndPool(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	b := billing.New(p, a, noLedger{})
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	accts, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	account := accts[0].ID
	for _, instance := range []string{"one", "two"} {
		c := billing.RuntimeCall{RuntimeScope: billing.RuntimeScope{ProductID: "llmrouter", InstanceID: instance}, RuntimeReserveRequest: billing.RuntimeReserveRequest{CallID: "same-call", AccountID: account, Asset: billing.CashAsset, Model: "native"}, ID: instance, ActorID: u.ID, Held: "3", Charged: "2", Status: "SETTLED"}
		raw, _ := json.Marshal(c)
		if _, e = p.Exec(ctx, `INSERT INTO product_runtime_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'d',$3)`, instance, account, raw); e != nil {
			t.Fatal(e)
		}
		effect, _ := json.Marshal(map[string]any{"runtime_call": c})
		if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status,created_at) VALUES($1,$2,$3,'runtime_reserve',$1,'d','{}',$4,'APPLIED',$5)`, "reserve-"+instance, account, u.ID, effect, time.Now().Add(-time.Minute)); e != nil {
			t.Fatal(e)
		}
		for _, product := range []string{"llmrouter", "agentwiki"} {
			m := billing.Call{CallID: product + "-" + instance, AccountID: account, ActorID: u.ID, OriginProductID: product, InstanceID: instance, Rate: billing.Rate{Model: "managed"}, Status: "RELEASED", Held: "0", Charged: "0"}
			raw, _ = json.Marshal(m)
			if _, e = p.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot,created_at) VALUES($1,$2,'d',$3,$4)`, m.CallID, account, raw, time.Now().Add(-time.Minute)); e != nil {
				t.Fatal(e)
			}
		}
	}
	previousCursor := ""
	for _, instance := range []string{"one", "two"} {
		policy := products.Client{ProductID: "llmrouter", InstanceID: instance, ClientID: "client", Scopes: []string{"usage:read", "usage:all-products"}}
		h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"client": productVerifier{}}, ProductAccess: map[string]products.Client{"client": policy}})
		other := "two"
		if instance == "two" {
			other = "one"
		}
		base := "/v1/accounts/" + account
		if previousCursor != "" {
			w := call(h, "GET", base+"/calls?limit=1&cursor="+previousCursor, "product-alice", "", "")
			if w.Code != 400 {
				t.Fatalf("cross-instance cursor accepted %d %s", w.Code, w.Body)
			}
		}
		var first billing.CallPage
		w := call(h, "GET", base+"/calls?limit=1", "product-alice", "", "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || first.NextCursor == "" {
			t.Fatalf("instance first page %d %s", w.Code, w.Body)
		}
		previousCursor = first.NextCursor

		for _, prefix := range []string{"", "/model-pool"} {
			var page billing.CallPage
			w := call(h, "GET", base+prefix+"/calls", "product-alice", "", "")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
				t.Fatalf("page %d %s", w.Code, w.Body)
			}
			want := "2"
			if prefix != "" {
				want = "3"
			}
			if page.Total != want {
				t.Fatalf("instance %s path %s total %s want %s", instance, prefix, page.Total, want)
			}
			var summary billing.UsageSummary
			w = call(h, "GET", base+prefix+"/usage/summary", "product-alice", "", "")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &summary) != nil || summary.CallsTotal != want {
				t.Fatalf("summary %d %s", w.Code, w.Body)
			}
			for _, detail := range []string{"product_runtime:" + other, "llmrouter-" + other} {
				w = call(h, "GET", base+prefix+"/calls/"+detail, "product-alice", "", "")
				if w.Code != 404 {
					t.Fatalf("instance %s read %s: %d %s", instance, detail, w.Code, w.Body)
				}
			}
			w = call(h, "GET", base+prefix+"/calls/product_runtime:"+instance, "product-alice", "", "")
			if w.Code != 200 {
				t.Fatalf("own instance %d %s", w.Code, w.Body)
			}
			for _, suffix := range []string{"/calls?instance_id=", "/usage/summary?instance_id=", "/calls/product_runtime:" + other + "?instance_id="} {
				w = call(h, "GET", base+prefix+suffix+other, "product-alice", "", "")
				if w.Code != 403 {
					t.Fatalf("instance override %s %d %s", suffix, w.Code, w.Body)
				}
			}
		}
	}
}
