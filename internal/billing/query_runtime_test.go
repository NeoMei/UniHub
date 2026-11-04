package billing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestQueryRuntimeMixedSourcesAndBoundaries(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	owner, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://runtime-query.test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	member, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://runtime-query.test", Subject: "member"})
	if e != nil {
		t.Fatal(e)
	}
	team, e := a.CreateTeam(ctx, owner.ID, "Query team")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'member')`, team.ID, member.ID); e != nil {
		t.Fatal(e)
	}
	s := New(p, a, nil)
	when := time.Now().UTC().Truncate(time.Second)
	native := RuntimeCall{RuntimeScope: RuntimeScope{ProductID: "llmrouter", InstanceID: "one"}, RuntimeReserveRequest: RuntimeReserveRequest{CallID: "same-local-call", AccountID: team.ID, Asset: CashAsset, Model: "native-model", Operation: "chat", PriceVersion: "product-v2", PricingDigest: "private-pricing"}, ActorID: member.ID, Held: "11", Charged: "7", Refunded: "2", Status: "SETTLED", Usage: json.RawMessage(`{"supplier_secret":"private-usage"}`)}
	insert := func(c RuntimeCall, at *time.Time, unknown bool) {
		t.Helper()
		c.ID = runtimeID(c.RuntimeScope, "call", c.CallID)
		if _, e := p.Exec(ctx, `INSERT INTO product_runtime_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'private-digest',$3)`, c.ID, c.AccountID, encode(c)); e != nil {
			t.Fatal(e)
		}
		if at != nil {
			status := "APPLIED"
			if unknown {
				status = "UNKNOWN"
			}
			if _, e := p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status,created_at) VALUES($1,$2,$3,'runtime_reserve',$1,'private-digest','{}',$4,$5,$6)`, "cmd-"+c.ID, c.AccountID, c.ActorID, encode(map[string]any{"runtime_call": c}), status, *at); e != nil {
				t.Fatal(e)
			}
		}
	}
	insert(native, &when, false)
	// A managed record may use the same database ID as the native record.
	nativeID := runtimeID(native.RuntimeScope, "call", native.CallID)
	queryInsert(t, s, Call{CallID: nativeID, AccountID: team.ID, ActorID: owner.ID, OriginProductID: "llmrouter", Rate: Rate{Model: "managed-model"}, Status: "SETTLED", Held: "3", Charged: "3"}, &when)
	other := native
	other.InstanceID = "two"
	other.Asset = RuntimeAsset
	other.Charged = "20"
	other.Refunded = "0"
	insert(other, nil, false)
	other = native
	other.ProductID = "other-product"
	insert(other, &when, false)
	other = native
	other.CallID = "unknown-call"
	other.Status = "RESERVED"
	other.Charged = "0"
	insert(other, &when, true)
	filter := CallFilter{OriginProductID: "llmrouter"}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, e := s.QueryCalls(ctx, owner.ID, team.ID, filter, CallPageRequest{Limit: 1, Cursor: cursor})
		if e != nil || page.Total != "4" || page.UnknownTime != "1" {
			t.Fatalf("mixed page %+v %v", page, e)
		}
		v := page.Items[0]
		if seen[v.CallID] {
			t.Fatalf("duplicate query identity %s", v.CallID)
		}
		seen[v.CallID] = true
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 4 {
		t.Fatal(seen)
	}
	page, e := s.QueryCalls(ctx, member.ID, team.ID, filter, CallPageRequest{Limit: 10})
	if e != nil || page.Total != "3" || page.FinancialDetails {
		t.Fatalf("member scope %+v %v", page, e)
	}
	wire, _ := json.Marshal(page)
	for _, secret := range []string{"private-pricing", "private-usage", "private-digest", "\"charged\"", "\"held\"", "\"refunded\"", "\"price_version\""} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("member leak %s: %s", secret, wire)
		}
	}
	v, e := s.QueryCall(ctx, owner.ID, team.ID, "product_runtime:"+nativeID, "llmrouter")
	if e != nil || v.Charged != "7" || v.Usage != nil || v.Rate != nil {
		t.Fatalf("native detail %+v %v", v, e)
	}
	raw, _ := json.Marshal(v)
	var dto map[string]any
	_ = json.Unmarshal(raw, &dto)
	for key, want := range map[string]string{"source": "product_runtime", "source_call_id": "same-local-call", "instance_id": "one", "operation": "chat", "funding_mode": "prepaid_cash", "funding_product_id": "", "refunded": "2", "price_version": "product-v2", "price_owner": "product", "usage_status": "unavailable"} {
		if dto[key] != want {
			t.Errorf("%s=%v want %s", key, dto[key], want)
		}
	}
	if _, e = s.QueryCall(ctx, member.ID, team.ID, nativeID, "llmrouter"); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatalf("member managed lookup %v", e)
	}
	if _, e = s.QueryCall(ctx, owner.ID, team.ID, "product_runtime:"+nativeID, "other-product"); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatalf("cross-product lookup %v", e)
	}
	summary, e := s.QueryUsageSummary(ctx, owner.ID, team.ID, filter)
	if e != nil || summary.CallsTotal != "4" || summary.UnknownCalls != "1" || summary.Asset != "mixed" || summary.Charged != "" || summary.ChargesByAsset[CashAsset] != "7" || summary.ChargesByAsset[CreditAsset] != "3" || summary.ChargesByAsset[RuntimeAsset] != "20" || summary.UsageComplete {
		t.Fatalf("mixed summary %+v %v", summary, e)
	}
	quota, e := s.QueryCalls(ctx, owner.ID, team.ID, CallFilter{OriginProductID: "llmrouter", Asset: RuntimeAsset}, CallPageRequest{Limit: 10})
	if e != nil || quota.Total != "1" || quota.Items[0].Asset != RuntimeAsset {
		t.Fatalf("quota page %+v %v", quota, e)
	}
	timed, e := s.QueryCalls(ctx, owner.ID, team.ID, CallFilter{OriginProductID: "llmrouter", From: when, To: when.Add(time.Second)}, CallPageRequest{Limit: 10})
	if e != nil || timed.Total != "3" {
		t.Fatalf("time window %+v %v", timed, e)
	}
	if _, e = s.QueryCalls(ctx, member.ID, team.ID, filter, CallPageRequest{Limit: 1, Cursor: cursor}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("cross-role cursor %v", e)
	}
	var instanceFilter CallFilter
	_ = json.Unmarshal([]byte(`{"origin_product_id":"llmrouter","instance_id":"one"}`), &instanceFilter)
	instancePage, e := s.QueryCalls(ctx, owner.ID, team.ID, instanceFilter, CallPageRequest{Limit: 10})
	if e != nil || instancePage.Total != "2" {
		t.Fatalf("instance isolation %+v %v", instancePage, e)
	}
	if _, e = s.QueryCalls(ctx, owner.ID, team.ID, instanceFilter, CallPageRequest{Limit: 1, Cursor: cursor}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("instance cursor scope %v", e)
	}
	// Even a managed ID deliberately equal to the native detail address must not silently select a source.
	queryInsert(t, s, Call{CallID: "product_runtime:" + nativeID, AccountID: team.ID, ActorID: owner.ID, OriginProductID: "llmrouter", Rate: Rate{Model: "collision"}, Status: "RELEASED", Charged: "0"}, &when)
	if _, e = s.QueryCall(ctx, owner.ID, team.ID, "product_runtime:"+nativeID, "llmrouter"); !errors.Is(e, ErrConflict) {
		t.Fatalf("ambiguous address selected: %v", e)
	}
}

func TestQueryWaiverIsSafeReadonlyEvidence(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://waiver-query.test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	accts, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	account := accts[0].ID
	s := New(p, a, nil)
	at := time.Now().UTC()
	queryInsert(t, s, Call{CallID: "waived", AccountID: account, ActorID: u.ID, OriginProductID: "llmrouter", Rate: Rate{Model: "test"}, ChargeAsset: CashAsset, Status: "RELEASED", Charged: "0", Held: "0"}, &at)
	if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status) VALUES('release-waived',$1,$2,'release','waived','secret','{}','{}','APPLIED')`, account, u.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `INSERT INTO billing_platform_fault_waivers(case_id,call_id,account_id,operator_id,approval_sha256,approval_raw,original_central_row,original_commands,release_command_id,provider_state) VALUES('safe-case','waived',$1,$2,repeat('a',64),'PRIVATE-APPROVAL','{"private":"PRIVATE-EVIDENCE"}','{"secret":"PRIVATE-COMMAND"}','release-waived','PROVIDER_UNRESOLVED')`, account, u.ID); e != nil {
		t.Fatal(e)
	}
	for _, commandStatus := range []string{"APPLIED", "UNKNOWN"} {
		if _, e = p.Exec(ctx, `UPDATE billing_commands SET status=$1 WHERE id='release-waived'`, commandStatus); e != nil {
			t.Fatal(e)
		}
		v, e := s.QueryCall(ctx, u.ID, account, "waived", "llmrouter")
		if e != nil || v.Waiver == nil {
			t.Fatalf("waiver %+v %v", v, e)
		}
		want := "RELEASED"
		if commandStatus == "UNKNOWN" {
			want = "UNKNOWN"
		}
		if v.Waiver.CaseID != "safe-case" || v.Waiver.ProviderState != "PROVIDER_UNRESOLVED" || v.Waiver.CustomerStatus != want {
			t.Fatalf("waiver %+v", v.Waiver)
		}
		raw, _ := json.Marshal(v)
		for _, private := range []string{"PRIVATE-", "approval_sha256", "original_commands", "operator_id", "release_command_id"} {
			if strings.Contains(string(raw), private) {
				t.Fatalf("waiver exposed internal evidence: %s", raw)
			}
		}
	}
	var count int
	if e = p.QueryRow(ctx, `SELECT count(*) FROM billing_platform_fault_waivers`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("query mutated waiver %d %v", count, e)
	}
}

func TestQueryConfirmedMoneyRemainsVisibleDuringUnknownRefund(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://confirmed-query.test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	account := as[0].ID
	s := New(p, a, nil)
	native := RuntimeCall{RuntimeScope: RuntimeScope{ProductID: "llmrouter", InstanceID: "default"}, RuntimeReserveRequest: RuntimeReserveRequest{CallID: "paid-native", AccountID: account, Asset: CashAsset, Model: "native"}, ID: "paid-native-db", ActorID: u.ID, Status: "SETTLED", Held: "120", Charged: "100", Refunded: "10"}
	if _, e = p.Exec(ctx, `INSERT INTO product_runtime_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'d',$3)`, native.ID, account, encode(native)); e != nil {
		t.Fatal(e)
	}
	pending := native
	pending.Refunded = "40"
	if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status) VALUES('pending-refund',$1,$2,'runtime_refund','refund','d','{}',$3,'UNKNOWN')`, account, u.ID, encode(effect{RuntimeCall: &pending})); e != nil {
		t.Fatal(e)
	}
	managed := Call{CallID: "paid-managed", AccountID: account, ActorID: u.ID, OriginProductID: "llmrouter", Rate: Rate{Model: "managed"}, ChargeAsset: CashAsset, Status: "SETTLED", Held: "30", Charged: "27"}
	queryInsert(t, s, managed, nil)
	pendingManaged := managed
	pendingManaged.Charged = "99"
	if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status) VALUES('pending-managed',$1,$2,'settle','paid-managed','d','{}',$3,'UNKNOWN')`, account, u.ID, encode(effect{Call: &pendingManaged})); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ model, id, charged, refunded string }{{"native", "product_runtime:paid-native-db", "100", "10"}, {"managed", "paid-managed", "27", "0"}} {
		v, e := s.QueryCall(ctx, u.ID, account, tc.id, "llmrouter")
		if e != nil || v.Status != "UNKNOWN" || v.Charged != tc.charged || v.Refunded != tc.refunded {
			t.Fatalf("confirmed detail %+v %v", v, e)
		}
		total, e := s.QueryUsageSummary(ctx, u.ID, account, CallFilter{OriginProductID: "llmrouter", Asset: CashAsset, Model: tc.model})
		if e != nil || total.UnknownCalls != "1" || total.SettledCalls != "0" || total.ChargesByAsset[CashAsset] != tc.charged || total.RefundsByAsset[CashAsset] != tc.refunded {
			t.Fatalf("confirmed summary %s %+v %v", tc.model, total, e)
		}
	}
	total, e := s.QueryUsageSummary(ctx, u.ID, account, CallFilter{OriginProductID: "llmrouter", Asset: CashAsset})
	if e != nil || total.Charged != "127" || total.Refunded != "10" || total.UnknownCalls != "2" {
		t.Fatalf("combined confirmed money %+v %v", total, e)
	}
}
