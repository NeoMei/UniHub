package billing

import (
	"context"
	"errors"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

func cashRate(id string) Rate {
	return Rate{ID: id, Model: "cash-model", Asset: CashAsset, Semantics: "exclusive_v1", Input: Rational{"1", "1"}, Output: Rational{"0", "1"}, CacheRead: Rational{"0", "1"}, CacheWrite: Rational{"0", "1"}, Reasoning: Rational{"0", "1"}}
}
func TestRateAssetsAreDenominationsWithoutConversion(t *testing.T) {
	r := cashRate("r")
	u := Usage{"3", "0", "0", "0", "0"}
	if n, e := CalculateCharge(r, u); e != nil || n != "3" {
		t.Fatalf("cash charge %s %v", n, e)
	}
	r.Asset = "CREDIT/6"
	if n, e := CalculateCharge(r, u); e != nil || n != "3" {
		t.Fatalf("credit charge %s %v", n, e)
	}
	r.Asset = "USD/6"
	if _, e := CalculateCharge(r, u); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unknown denomination %v", e)
	}
}
func prepaidFixture(t *testing.T) fixture {
	f := setup(t)
	ctx := context.Background()
	p := BillingProfile{ProductID: "superwagie", Usage: UsagePolicy{Enabled: true, FundingModes: []string{FundingPrepaidCash}, DefaultFundingMode: FundingPrepaidCash}}
	if _, e := f.s.SaveProfile(ctx, f.actor, p.ProductID, p); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.PublishRate(ctx, f.actor, cashRate("cash-rate")); e != nil {
		t.Fatal(e)
	}
	return f
}
func fundCash(t *testing.T, f fixture, product string, n int64) {
	t.Helper()
	if _, e := f.l.Post(context.Background(), ledger.Command{Reference: id("fund-"), Postings: []ledger.Posting{{Source: "world", Destination: wallet.Address(f.account, product), Asset: CashAsset, Amount: n}}}); e != nil {
		t.Fatal(e)
	}
}
func cashCredential(t *testing.T, f fixture) string {
	t.Helper()
	cid := id("mc_")
	_, e := f.s.pool.Exec(context.Background(), `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version) VALUES($1,$1,$2,$3,'superwagie','["cash-model"]','cash',$4,$5,$6,1)`, cid, f.actor, f.account, f.s.now().Add(time.Hour), FundingPrepaidCash, CashAsset)
	if e != nil {
		t.Fatal(e)
	}
	return cid
}
func cashReserve(t *testing.T, f fixture, call, credential, amount string) (Call, error) {
	t.Helper()
	return f.s.Reserve(context.Background(), "router", ReserveRequest{CallID: call, ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "cash", RateID: "cash-rate", Amount: amount, CredentialID: credential})
}
func cashBalance(t *testing.T, f fixture, address string, want int64) {
	t.Helper()
	n, e := f.l.Balance(context.Background(), address, CashAsset)
	if e != nil || n != want {
		t.Fatalf("%s balance %d want %d: %v", address, n, want, e)
	}
}
func TestPrepaidUsageWithoutSubscriptionUsesOnlyRouterCash(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	fundCash(t, f, "superwagie", 1000)
	fundCash(t, f, "llmrouter", 100)
	cid := cashCredential(t, f)
	call, e := cashReserve(t, f, "cash-call", cid, "80")
	if e != nil {
		t.Fatal(e)
	}
	if call.ChargeAsset != CashAsset || call.FundingMode != FundingPrepaidCash || call.FundingProductID != "llmrouter" || call.BillingProfileVersion != 1 {
		t.Fatalf("snapshot %+v", call)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 20)
	cashBalance(t, f, wallet.Address(f.account, "superwagie"), 1000)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000000000)
	u := Usage{"30", "0", "0", "0", "0"}
	call, e = f.s.Settle(ctx, "router", SettlementRequest{CallID: call.CallID, Usage: &u})
	if e != nil || call.Charged != "30" {
		t.Fatalf("settle %+v %v", call, e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 70)
	cashBalance(t, f, CashHoldAddress(f.account, call.CallID), 0)
	cashBalance(t, f, "commercial:revenue:llmrouter", 30)
	if _, e = f.s.Settle(ctx, "router", SettlementRequest{CallID: call.CallID, Usage: &u}); e != nil {
		t.Fatal(e)
	}
	if _, e = cashReserve(t, f, "cash-call", cid, "80"); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 70)
}
func TestPrepaidInsufficientRouterCashNeverUsesOtherWalletOrCredits(t *testing.T) {
	f := prepaidFixture(t)
	fundCash(t, f, "superwagie", 1000)
	cid := cashCredential(t, f)
	if _, e := cashReserve(t, f, "insufficient", cid, "1"); !errors.Is(e, ledger.ErrInsufficientFunds) {
		t.Fatalf("insufficient %v", e)
	}
	cashBalance(t, f, wallet.Address(f.account, "superwagie"), 1000)
	cashBalance(t, f, wallet.Address(f.account, ""), 1000000000)
	// A credential whose trusted denomination differs cannot select a CNY rate.
	if _, e := f.s.pool.Exec(context.Background(), `UPDATE model_credentials SET funding_mode='credits',asset='CREDIT/6' WHERE id=$1`, cid); e != nil {
		t.Fatal(e)
	}
	if _, e := cashReserve(t, f, "wrong-credential", cid, "1"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("untrusted cash %v", e)
	}
}
func TestPrepaidTopupReleaseAndUnknownRecoveryKeepOneAsset(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	fundCash(t, f, "llmrouter", 100)
	cid := cashCredential(t, f)
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	call, e := cashReserve(t, f, "lost-cash", cid, "40")
	if !errors.Is(e, ErrPending) {
		t.Fatalf("lost %+v %v", call, e)
	}
	// Current policy can change while an uncertain command keeps its immutable terms.
	if _, e = f.s.pool.Exec(ctx, `UPDATE products SET enabled=false WHERE id='superwagie'`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	call, e = f.s.GetCall(ctx, "router", "lost-cash")
	if e != nil || call.Status != "RESERVED" {
		t.Fatalf("recover %+v %v", call, e)
	}
	if _, e = f.s.TopUp(ctx, "router", call.CallID, "10", "blocked"); e == nil {
		t.Fatal("new hold bypassed current product policy")
	}
	if _, e = f.s.pool.Exec(ctx, `UPDATE products SET enabled=true WHERE id='superwagie'`); e != nil {
		t.Fatal(e)
	}
	call, e = f.s.TopUp(ctx, "router", call.CallID, "20", "topup")
	if e != nil || call.Held != "60" {
		t.Fatalf("topup %+v %v", call, e)
	}
	if _, e = f.s.TopUp(ctx, "router", call.CallID, "20", "topup"); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 40)
	if _, e = f.s.Settle(ctx, "router", SettlementRequest{CallID: call.CallID}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Release(ctx, "router", call.CallID, "failed"); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
	cashBalance(t, f, CashHoldAddress(f.account, call.CallID), 0)
}

func TestCreditUsageNeverFallsBackToPrepaidCash(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.auth(t)
	f.buy(t, "sandbox-sw-month-1", "member")
	rateFixture(t, f)
	fundCash(t, f, "llmrouter", 5000000)
	if _, e := f.s.Reserve(ctx, "router", ReserveRequest{CallID: "no-fallback", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "legacy", RateID: "r", Amount: "1000001"}); !errors.Is(e, ErrInsufficientCredits) {
		t.Fatalf("credits fallback %v", e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 5000000)
}
func TestPrepaidPolicyRevocationStopsNewHoldsButOldSettlementCapsCharge(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	fundCash(t, f, "llmrouter", 100)
	cid := cashCredential(t, f)
	call, e := cashReserve(t, f, "old-policy", cid, "40")
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.s.Profile(ctx, "superwagie")
	if e != nil {
		t.Fatal(e)
	}
	p.Usage = UsagePolicy{}
	if _, e = f.s.SaveProfile(ctx, f.actor, p.ProductID, p); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.TopUp(ctx, "router", call.CallID, "1", "revoked"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("topup escaped policy %v", e)
	}
	if _, e = cashReserve(t, f, "new-policy", cid, "1"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("reserve escaped policy %v", e)
	}
	u := Usage{"70", "0", "0", "0", "0"}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, e = f.s.Settle(ctx, "router", SettlementRequest{CallID: call.CallID, Usage: &u}); !errors.Is(e, ErrPending) {
		t.Fatalf("loss %v", e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	call, e = f.s.GetCall(ctx, "router", call.CallID)
	if e != nil || call.Charged != "40" || call.PlatformDifference != "30" || call.ChargeAsset != CashAsset || call.BillingProfileVersion != 1 {
		t.Fatalf("original capped terms %+v %v", call, e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 60)
	cashBalance(t, f, "commercial:revenue:llmrouter", 40)
}
func TestPrepaidCreatedCommandCannotDebitAnAlternativeWallet(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	fundCash(t, f, "superwagie", 100)
	cid := cashCredential(t, f)
	call := Call{ServiceID: "router", CallID: "tampered", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", Rate: cashRate("cash-rate"), Held: "40", Status: "RESERVED", ChargeAsset: CashAsset, FundingMode: FundingPrepaidCash, FundingProductID: "llmrouter", BillingProfileVersion: 1, CredentialID: cid}
	tx, e := f.s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'tampered',$3)`, call.CallID, f.account, encode(call)); e != nil {
		t.Fatal(e)
	}
	cmd, e := f.s.createCommand(ctx, tx, f.account, f.actor, "reserve", call.CallID, []ledger.Posting{{Source: wallet.Address(f.account, "superwagie"), Destination: CashHoldAddress(f.account, call.CallID), Asset: CashAsset, Amount: 40}}, effect{Call: &call})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = f.s.pool.QueryRow(ctx, `SELECT status FROM billing_commands WHERE id=$1`, cmd.ID).Scan(&status); e != nil || status != "CANCELED" {
		t.Fatalf("alternative wallet command %s %v", status, e)
	}
	cashBalance(t, f, wallet.Address(f.account, "superwagie"), 100)
	cashBalance(t, f, CashHoldAddress(f.account, call.CallID), 0)
}

func TestPrepaidMemberRemovalStopsNewHoldsAndAllowsExistingRelease(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	owner := f.actor
	member, e := f.s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://cash-test", Subject: "member"})
	if e != nil {
		t.Fatal(e)
	}
	team, e := f.s.accounts.CreateTeam(ctx, owner, "Cash team")
	if e != nil {
		t.Fatal(e)
	}
	inv, e := f.s.accounts.Invite(ctx, owner, team.ID, member.ID, "member")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.accounts.AcceptInvitation(ctx, member.ID, inv.ID); e != nil {
		t.Fatal(e)
	}
	f.account = team.ID
	f.actor = member.ID
	fundCash(t, f, "llmrouter", 100)
	cid := cashCredential(t, f)
	call, e := cashReserve(t, f, "removed-member", cid, "40")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.accounts.RemoveMember(ctx, owner, team.ID, member.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.TopUp(ctx, "router", call.CallID, "1", "removed"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("removed member topped up %v", e)
	}
	if _, e = cashReserve(t, f, "removed-new", cid, "1"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("removed member reserved %v", e)
	}
	if _, e = f.s.Release(ctx, "router", call.CallID, "cancelled"); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 100)
}
