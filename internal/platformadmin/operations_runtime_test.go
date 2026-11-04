package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestOperationsIncludeRuntimeCallsPaymentsAndTrends(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	admin, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://runtime-operations.test", Subject: "admin"})
	if e != nil {
		t.Fatal(e)
	}
	plain, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://runtime-operations.test", Subject: "plain"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `INSERT INTO platform_admin_roles(user_id,roles,source,granted_by) VALUES($1,'["super_admin"]','test',$1)`, admin.ID); e != nil {
		t.Fatal(e)
	}
	accts, e := a.ListAccounts(ctx, plain.ID)
	if e != nil {
		t.Fatal(e)
	}
	account := accts[0].ID
	when := time.Now().UTC().Add(-time.Minute)
	call := billing.RuntimeCall{RuntimeScope: billing.RuntimeScope{ProductID: "llmrouter", InstanceID: "one"}, RuntimeReserveRequest: billing.RuntimeReserveRequest{CallID: "native-id", AccountID: account, Asset: billing.CashAsset, Model: "native-model", Operation: "chat", PriceVersion: "native-v2", PricingDigest: "PRIVATE-PRICING"}, ID: "same-id", ActorID: plain.ID, Held: "11", Charged: "7", Refunded: "2", Status: "SETTLED", Usage: json.RawMessage(`{"secret":"PRIVATE-USAGE"}`)}
	raw, _ := json.Marshal(call)
	if _, e = p.Exec(ctx, `INSERT INTO product_runtime_calls(id,account_id,request_digest,snapshot) VALUES('same-id',$1,'PRIVATE-DIGEST',$2)`, account, raw); e != nil {
		t.Fatal(e)
	}
	effect, _ := json.Marshal(map[string]any{"runtime_call": call})
	if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status,created_at) VALUES('native-reserve',$1,$2,'runtime_reserve','native-id','PRIVATE-DIGEST','{}',$3,'UNKNOWN',$4)`, account, plain.ID, effect, when); e != nil {
		t.Fatal(e)
	}
	managed := billing.Call{CallID: "same-id", AccountID: account, ActorID: plain.ID, OriginProductID: "llmrouter", Rate: billing.Rate{Model: "managed"}, Status: "SETTLED", Charged: "3"}
	raw, _ = json.Marshal(managed)
	if _, e = p.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot,created_at) VALUES('same-id',$1,'PRIVATE-DIGEST',$2,$3)`, account, raw, when); e != nil {
		t.Fatal(e)
	}
	adjustment := billing.RuntimeAdjustment{RuntimeScope: call.RuntimeScope, ID: "receipt", AccountID: account, Amount: "9007199254740993", Status: "APPLIED", Source: json.RawMessage(`{"asset":"CNY/6","local_user_id":"42","source_payload":{"trade_no":"PRIVATE-RECEIPT"}}`)}
	raw, _ = json.Marshal(adjustment)
	if _, e = p.Exec(ctx, `INSERT INTO product_runtime_adjustments(id,account_id,product_id,instance_id,source_kind,source_key,request_digest,snapshot) VALUES('receipt',$1,'llmrouter','one','payment','business-receipt-42','PRIVATE-DIGEST',$2)`, account, raw); e != nil {
		t.Fatal(e)
	}
	effect, _ = json.Marshal(map[string]any{"runtime_adjustment": adjustment})
	if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status,created_at) VALUES('payment-command',$1,$2,'runtime_payment','receipt','PRIVATE-DIGEST','{}',$3,'APPLIED',$4)`, account, plain.ID, effect, when); e != nil {
		t.Fatal(e)
	}
	// Non-payment awards must never appear as recharges.
	if _, e = p.Exec(ctx, `INSERT INTO product_runtime_adjustments(id,account_id,product_id,instance_id,source_kind,source_key,request_digest,snapshot) VALUES('gift',$1,'llmrouter','one','gift','gift','d',$2)`, account, raw); e != nil {
		t.Fatal(e)
	}
	s := New(p, nil)
	opt := OperationsOptions{ListOptions: ListOptions{Limit: 1}, AccountID: account, ProductID: "llmrouter"}
	seen := map[string]bool{}
	for {
		page, e := s.ListOperations(ctx, admin.ID, "calls", opt)
		if e != nil || page.Total != 2 {
			t.Fatalf("calls %+v %v", page, e)
		}
		v := page.Items[0]
		id := v["id"].(string)
		if seen[id] {
			t.Fatal("duplicate source identity")
		}
		seen[id] = true
		if v["source"] == "product_runtime" {
			if v["status"] != "UNKNOWN" || v["refunded"] != "2" || v["price_owner"] != "product" {
				t.Fatal(v)
			}
		}
		if page.NextCursor == "" {
			break
		}
		opt.Cursor = page.NextCursor
	}
	page, e := s.ListOperations(ctx, admin.ID, "recharges", OperationsOptions{AccountID: account, ProductID: "llmrouter"})
	if e != nil || page.Total != 1 || page.Items[0]["amount"] != "9007199254740993" || page.Items[0]["asset"] != billing.CashAsset || page.Items[0]["source"] != "product_runtime" || page.Items[0]["source_id"] != "business-receipt-42" || page.Items[0]["id"] != "product_runtime:receipt" {
		t.Fatalf("payments %+v %v", page, e)
	}
	raw, _ = json.Marshal(page)
	for _, secret := range []string{"PRIVATE-RECEIPT", "PRIVATE-DIGEST", "source_payload"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("payment secret %s", raw)
		}
	}
	calls, e := s.ListOperations(ctx, admin.ID, "calls", OperationsOptions{})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(calls)
	if strings.Contains(string(raw), "PRIVATE-") {
		t.Fatalf("call secret %s", raw)
	}
	trend, e := s.OperationTimeseries(ctx, admin.ID)
	if e != nil {
		t.Fatal(e)
	}
	var total int64
	for _, d := range trend.Days {
		total += d.Calls
	}
	if total != 2 {
		t.Fatalf("trend calls %d", total)
	}
	overview, e := s.OperationOverview(ctx, admin.ID)
	if e != nil {
		t.Fatal(e)
	}
	counts := overview["calls"].(map[string]any)
	if counts["logical_calls"] != int64(2) || counts["settled"] != int64(1) || counts["unknown"] != int64(1) {
		t.Fatalf("overview %v", counts)
	}
	if _, e = s.ListOperations(ctx, plain.ID, "calls", OperationsOptions{}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("ordinary access %v", e)
	}
}
