package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

func TestOpaqueCashCallIDsRoundTripWithoutBlockingAccount(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	fundCash(t, f, "llmrouter", 1000)
	credential := cashCredential(t, f)
	ids := []string{"bad call", "job/path?model=x#1", "任务 ①", "quote\"slash\\and\nnewline"}
	for _, callID := range ids {
		call, e := cashReserve(t, f, callID, credential, "30")
		if e != nil {
			t.Fatalf("reserve opaque id %q: %v", callID, e)
		}
		if call.CallID != callID {
			t.Fatalf("call id changed: %q", call.CallID)
		}
		if call.CashHoldAddress != CashHoldAddress(f.account, callID) {
			t.Fatalf("missing immutable hold address: %+v", call)
		}
		call, e = f.s.TopUp(ctx, "router", callID, "10", "opaque-topup")
		if e != nil || call.Held != "40" {
			t.Fatalf("topup opaque id %q: %+v %v", callID, call, e)
		}
		usage := Usage{Input: "10", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
		call, e = f.s.Settle(ctx, "router", SettlementRequest{CallID: callID, Usage: &usage})
		if e != nil || call.Charged != "10" {
			t.Fatalf("settle opaque id %q: %+v %v", callID, call, e)
		}
		if _, e = cashReserve(t, f, callID, credential, "30"); e != nil {
			t.Fatalf("replay opaque id %q: %v", callID, e)
		}
		releaseID := callID + " release"
		if _, e = cashReserve(t, f, releaseID, credential, "20"); e != nil {
			t.Fatalf("reserve release id %q: %v", releaseID, e)
		}
		if _, e = f.s.Release(ctx, "router", releaseID, "failed"); e != nil {
			t.Fatalf("release opaque id %q: %v", releaseID, e)
		}
	}
	if _, e := cashReserve(t, f, "subsequent-good-call", credential, "20"); e != nil {
		t.Fatalf("valid next call blocked: %v", e)
	}
	if _, e := f.s.Release(ctx, "router", "subsequent-good-call", "finished"); e != nil {
		t.Fatal(e)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 960)
	var unresolved int
	if e := f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE account_id=$1 AND status IN ('CREATED','SUBMITTING','UNKNOWN')`, f.account).Scan(&unresolved); e != nil || unresolved != 0 {
		t.Fatalf("unresolved commands %d: %v", unresolved, e)
	}
}

func TestExistingCashHoldWithoutAddressSnapshotStillFinalizes(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	fundCash(t, f, "llmrouter", 20)
	for _, test := range []struct {
		callID string
		settle bool
	}{{"legacy-cash-settle", true}, {"legacy-cash-release", false}} {
		rawAddress := "cash:" + f.account + ":usage:" + test.callID
		if _, e := f.l.Post(ctx, ledger.Command{Reference: id("legacy-hold-"), Postings: []ledger.Posting{{Source: "world", Destination: rawAddress, Asset: CashAsset, Amount: 30}}}); e != nil {
			t.Fatal(e)
		}
		call := Call{ServiceID: "router", CallID: test.callID, ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", Rate: cashRate("cash-rate"), Held: "30", Charged: "0", PlatformDifference: "0", Status: "RESERVED", ChargeAsset: CashAsset, FundingMode: FundingPrepaidCash, FundingProductID: "llmrouter", BillingProfileVersion: 1}
		if _, e := f.s.pool.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'legacy',$3)`, call.CallID, f.account, encode(call)); e != nil {
			t.Fatal(e)
		}
		if test.settle {
			updated, e := f.s.TopUp(ctx, "router", call.CallID, "10", "legacy-topup")
			if e != nil || updated.Held != "40" {
				t.Fatalf("legacy topup %+v %v", updated, e)
			}
			usage := Usage{Input: "10", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
			if _, e := f.s.Settle(ctx, "router", SettlementRequest{CallID: call.CallID, Usage: &usage}); e != nil {
				t.Fatal(e)
			}
		} else {
			if _, e := f.s.Release(ctx, "router", call.CallID, "failed"); e != nil {
				t.Fatal(e)
			}
		}
		cashBalance(t, f, rawAddress, 0)
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 70)
	cashBalance(t, f, "commercial:revenue:llmrouter", 10)
}

func TestCashHoldNamespacesCannotAliasLegacyOpaqueIDs(t *testing.T) {
	account := "account-id"
	callID := "new call"
	digest := sha256.Sum256([]byte(callID))
	legacy := Call{AccountID: account, CallID: hex.EncodeToString(digest[:])}
	if CashHoldAddress(account, callID) == cashHoldForCall(legacy) {
		t.Fatal("new hash aliases existing literal call id")
	}
}
