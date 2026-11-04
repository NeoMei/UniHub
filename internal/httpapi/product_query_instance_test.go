package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
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

	policy := products.Client{ProductID: "superwagie", InstanceID: "one", ClientID: "client", Scopes: []string{"usage:read", "usage:llmrouter"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"client": productVerifier{}}, ProductAccess: map[string]products.Client{"client": policy}})
	base := "/v1/accounts/" + account
	get := func(path string, want int) []byte {
		t.Helper()
		w := call(h, "GET", path, "product-alice", "", "")
		if w.Code != want {
			t.Fatalf("%s: HTTP %d want %d", path, w.Code, want)
		}
		return w.Body.Bytes()
	}
	var page billing.CallPage
	if e := json.Unmarshal(get(base+"/model-pool/calls?asset=CNY%2F6", 200), &page); e != nil || page.Total != "1" || len(page.Items) != 1 || page.Items[0].Source != "product_runtime" || page.Items[0].OriginProductID != "llmrouter" || page.Items[0].Charged != "2" {
		t.Fatalf("unexpected LLMRouter cash projection: %+v, %v", page, e)
	}
	var summary billing.UsageSummary
	if e := json.Unmarshal(get(base+"/model-pool/usage/summary?asset=CNY%2F6", 200), &summary); e != nil || summary.CallsTotal != "1" || summary.ChargesByAsset[billing.CashAsset] != "2" {
		t.Fatalf("unexpected LLMRouter cash summary: %+v, %v", summary, e)
	}
	if e := json.Unmarshal(get(base+"/model-pool/calls", 200), &page); e != nil || page.Total != "1" || page.Items[0].Source != "product_runtime" {
		t.Fatalf("narrow scope included managed calls: %+v, %v", page, e)
	}
	if page.Scope.OriginProductID != "llmrouter" || page.Scope.InstanceID != "one" || page.Scope.Source != "product_runtime" || page.Filter.Source != "product_runtime" {
		t.Fatalf("missing narrow scope: %+v", page.Scope)
	}
	get(base+"/model-pool/calls/"+page.Items[0].CallID, 200)
	get(base+"/model-pool/calls/llmrouter-one", 404)
	get(base+"/model-pool/calls/agentwiki-one", 404)
	get(base+"/model-pool/calls/product_runtime:one?origin_product_id=agentwiki", 403)
	get(base+"/model-pool/usage/summary?origin_product_id=agentwiki", 403)
	get(base+"/model-pool/usage/summary?instance_id=two", 403)
	get(base+"/model-pool/calls?source=managed_model", 400)
	get(base+"/model-pool/calls?cursor="+url.QueryEscape(previousCursor), 400)
	var own billing.CallPage
	if e := json.Unmarshal(get(base+"/calls", 200), &own); e != nil || own.Total != "0" {
		t.Fatalf("ordinary product isolation widened: %+v, %v", own, e)
	}
	get(base+"/model-pool/calls/product_runtime:two", 404)
	get(base+"/model-pool/calls?origin_product_id=agentwiki", 403)
	get(base+"/model-pool/calls?instance_id=two", 403)
	get(base+"/calls?origin_product_id=llmrouter", 403)
	get("/v1/accounts/not-mine/model-pool/calls", 403)
	// Add a second runtime call to exercise real narrow pagination, plus a
	// different native product to prove an omitted origin stays restricted.
	for _, product := range []string{"llmrouter", "agentwiki"} {
		c := billing.RuntimeCall{RuntimeScope: billing.RuntimeScope{ProductID: product, InstanceID: "one"}, RuntimeReserveRequest: billing.RuntimeReserveRequest{CallID: "new-native", AccountID: account, Asset: billing.CashAsset, Model: "native"}, ID: product + "-runtime", ActorID: u.ID, Held: "3", Charged: "2", Status: "SETTLED"}
		raw, _ := json.Marshal(c)
		if _, e = p.Exec(ctx, `INSERT INTO product_runtime_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'d',$3)`, c.ID, account, raw); e != nil {
			t.Fatal(e)
		}
		effect, _ := json.Marshal(map[string]any{"runtime_call": c})
		if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status,created_at) VALUES($1,$2,$3,'runtime_reserve',$1,'d','{}',$4,'APPLIED',$5)`, "reserve-"+c.ID, account, u.ID, effect, time.Now().Add(-time.Minute)); e != nil {
			t.Fatal(e)
		}
	}
	if e := json.Unmarshal(get(base+"/model-pool/calls?limit=1", 200), &page); e != nil || page.Total != "2" || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("narrow pagination: %+v %v", page, e)
	}
	firstID := page.Items[0].CallID
	if e := json.Unmarshal(get(base+"/model-pool/calls?limit=1&cursor="+url.QueryEscape(page.NextCursor), 200), &page); e != nil || page.Total != "2" || page.HasMore || len(page.Items) != 1 || page.Items[0].CallID == firstID || page.Items[0].OriginProductID != "llmrouter" || page.Items[0].Source != "product_runtime" {
		t.Fatalf("narrow continuation: %+v %v", page, e)
	}
	if e := json.Unmarshal(get(base+"/model-pool/usage/summary", 200), &summary); e != nil || summary.CallsTotal != "2" || summary.ChargesByAsset[billing.CashAsset] != "4" || summary.Filter.Source != "product_runtime" {
		t.Fatalf("narrow aggregate: %+v %v", summary, e)
	}
	get(base+"/model-pool/calls/product_runtime:agentwiki-runtime", 404)
	broadPolicy := products.Client{ProductID: "llmrouter", InstanceID: "one", ClientID: "client", Scopes: []string{"usage:all-products"}}
	broad := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"client": productVerifier{}}, ProductAccess: map[string]products.Client{"client": broadPolicy}})
	wide := call(broad, "GET", base+"/model-pool/calls?origin_product_id=llmrouter&limit=1", "product-alice", "", "")
	if wide.Code != 200 || json.Unmarshal(wide.Body.Bytes(), &page) != nil || page.NextCursor == "" {
		t.Fatalf("broad cursor: %d %s", wide.Code, wide.Body)
	}
	get(base+"/model-pool/calls?cursor="+url.QueryEscape(page.NextCursor), 400)
}

func TestLlmrouterUsageScopeRequiresCurrentFinanceRole(t *testing.T) {
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
	team, e := a.CreateTeam(ctx, alice.ID, "narrow usage")
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
	policy := products.Client{ProductID: "superwagie", ClientID: "narrow", Scopes: []string{"usage:llmrouter"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ProductVerifiers: map[string]auth.Verifier{"narrow": anyProductVerifier{}}, ProductAccess: map[string]products.Client{"narrow": policy}})
	base := "/v1/accounts/" + team.ID
	for _, role := range []string{"member", "admin", "billing_admin", "member"} {
		if e = a.ChangeRole(ctx, alice.ID, team.ID, bob.ID, role); e != nil {
			t.Fatal(e)
		}
		// The domain rechecks finance in its own read transaction, even if
		// the HTTP boundary previously observed an owner/finance role.
		if role != "billing_admin" {
			f := billing.CallFilter{Source: "product_runtime", OriginProductID: "llmrouter", RequireFinance: true}
			if _, err := b.QueryCalls(ctx, bob.ID, team.ID, f, billing.CallPageRequest{Limit: 1}); !errors.Is(err, accounts.ErrForbidden) {
				t.Fatalf("finance list role=%s: %v", role, err)
			}
			if _, err := b.QueryUsageSummary(ctx, bob.ID, team.ID, f); !errors.Is(err, accounts.ErrForbidden) {
				t.Fatalf("finance summary role=%s: %v", role, err)
			}
			if _, err := b.QueryCallWithFilter(ctx, bob.ID, team.ID, "missing", f); !errors.Is(err, accounts.ErrForbidden) {
				t.Fatalf("finance detail role=%s: %v", role, err)
			}
		}
		for _, suffix := range []string{"/model-pool/calls", "/model-pool/usage/summary", "/model-pool/calls/missing"} {
			want := 403
			if role == "billing_admin" {
				want = 200
				if suffix == "/model-pool/calls/missing" {
					want = 404
				}
			}
			w := call(h, "GET", base+suffix, "product-bob", "", "")
			if w.Code != want {
				t.Fatalf("role=%s %s: %d want %d", role, suffix, w.Code, want)
			}
		}
	}
	for _, token := range []string{"alice", "product-bob"} {
		w := call(h, "GET", base+"/model-pool/calls", token, "", "")
		if w.Code != 403 {
			t.Fatalf("unauthorized pool %s: %d", token, w.Code)
		}
	}
	if w := call(h, "GET", base+"/calls", "product-alice", "", ""); w.Code != 403 {
		t.Fatalf("narrow scope implied ordinary usage: %d", w.Code)
	}
}
