package billing

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

// An account limit is shared by the two CNY charging channels. A managed Key's
// private limit must still count that Key alone, even if a native Key has its ID.
func TestAccountBudgetCountsBothChannelsWithoutMixingAssetsOrKeys(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://budget-test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	account := as[0].ID
	s := New(p, a, nil)
	if _, e = p.Exec(ctx, `INSERT INTO products(id,name,enabled) VALUES('llmrouter','LLMRouter',true) ON CONFLICT DO NOTHING`); e != nil {
		t.Fatal(e)
	}
	managed := Call{CallID: "managed-hold", AccountID: account, ActorID: u.ID, OriginProductID: "llmrouter", InstanceID: "default", ChargeAsset: CashAsset, FundingMode: FundingPrepaidCash, CredentialID: "managed-key", Status: "RESERVED", Held: "20", Charged: "0"}
	queryInsert(t, s, managed, nil)
	managed.CallID, managed.CredentialID, managed.Status, managed.Held, managed.Charged = "managed-paid", "other-key", "SETTLED", "30", "30"
	queryInsert(t, s, managed, nil)
	insert := func(id, instance, asset, status, held, charged, refunded string) {
		t.Helper()
		c := RuntimeCall{ID: id, ActorID: u.ID, RuntimeScope: RuntimeScope{ProductID: "llmrouter", InstanceID: instance}, RuntimeReserveRequest: RuntimeReserveRequest{CallID: id, AccountID: account, LocalKeyID: "managed-key", Asset: asset}, Status: status, Held: held, Charged: charged, Refunded: refunded}
		if _, e := p.Exec(ctx, `INSERT INTO product_runtime_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'test',$3)`, id, account, encode(c)); e != nil {
			t.Fatal(e)
		}
	}
	insert("native-hold", "default", CashAsset, "RESERVED", "15", "0", "0")
	insert("native-paid", "default", CashAsset, "SETTLED", "40", "40", "10")
	insert("native-quota", "default", RuntimeAsset, "RESERVED", "999", "0", "0")
	insert("native-other-instance", "other", CashAsset, "RESERVED", "999", "0", "0")
	if h, u, e := UsageTotalsOn(ctx, p, account, "llmrouter", "default", CashAsset, ""); e != nil || h != "35" || u != "60" {
		t.Fatalf("account totals %s/%s: %v", h, u, e)
	}
	if h, u, e := UsageTotalsOn(ctx, p, account, "llmrouter", "default", CashAsset, "managed-key"); e != nil || h != "20" || u != "0" {
		t.Fatalf("private key totals %s/%s: %v", h, u, e)
	}
	if h, u, e := UsageTotalsOn(ctx, p, account, "other-product", "default", CashAsset, ""); e != nil || h != "0" || u != "0" {
		t.Fatalf("cross product %s/%s: %v", h, u, e)
	}
	budget, e := s.SetUsageBudget(ctx, u.ID, account, "llmrouter", "default", FundingPrepaidCash, UsageBudgetRequest{MaxTotal: "100"})
	if e != nil || budget.Held != "35" || budget.Used != "60" || budget.Remaining == nil || *budget.Remaining != "5" {
		t.Fatalf("budget %+v: %v", budget, e)
	}
	for _, additional := range []int64{5, 6} {
		wantForbidden := additional == 6
		e = s.runtimeCheckBudget(ctx, p, RuntimeScope{ProductID: "llmrouter", InstanceID: "default"}, account, CashAsset, additional, additional)
		if errors.Is(e, accounts.ErrForbidden) != wantForbidden || (e != nil && !errors.Is(e, accounts.ErrForbidden)) {
			t.Fatalf("native additional=%d: %v", additional, e)
		}
		managed.CredentialID = ""
		e = s.checkUsageBudget(ctx, p, managed, additional, additional)
		if errors.Is(e, accounts.ErrForbidden) != wantForbidden || (e != nil && !errors.Is(e, accounts.ErrForbidden)) {
			t.Fatalf("managed additional=%d: %v", additional, e)
		}
	}
}

func TestRuntimeBudgetKeepsUnknownTopupExposure(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://budget-test", Subject: "pending"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	account := as[0].ID
	call := RuntimeCall{ID: "native", ActorID: u.ID, RuntimeScope: RuntimeScope{ProductID: "llmrouter", InstanceID: "default"}, RuntimeReserveRequest: RuntimeReserveRequest{CallID: "local-call", AccountID: account, Asset: CashAsset}, Status: "RESERVED", Held: "10", Charged: "0"}
	if _, e = p.Exec(ctx, `INSERT INTO product_runtime_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'test',$3)`, call.ID, account, encode(call)); e != nil {
		t.Fatal(e)
	}
	call.Held = "25"
	if _, e = p.Exec(ctx, `INSERT INTO billing_commands(id,account_id,actor_id,kind,request_key,request_digest,command,effect,status) VALUES('pending',$1,$2,'runtime_topup','topup','hash','{}',$3,'UNKNOWN')`, account, u.ID, encode(effect{RuntimeCall: &call})); e != nil {
		t.Fatal(e)
	}
	if h, u, e := UsageTotalsOn(ctx, p, account, "llmrouter", "default", CashAsset, ""); e != nil || h != "25" || u != "0" {
		t.Fatalf("unknown topup exposure %s/%s: %v", h, u, e)
	}
}
