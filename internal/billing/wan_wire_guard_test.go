package billing

import (
	"context"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/metering"
	"unihub/internal/testutil"
)

func TestPlannedVideoCannotAuthorizeNewWire(t *testing.T) {
	old := Rate{Model: "wan2.6-t2v", Asset: CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1"}}
	if RateMatchesWire(old, "videos.create") {
		t.Fatal("old Wan price authorized new video wire")
	}
	cases := []struct{ name, wire, operation, policy string }{
		{"chat", "chat.completions.create", "chat", "provider_reported_v1"},
		{"embedding", "embeddings.create", "embedding", "provider_reported_v1"},
	}
	otherVideo := Rate{Semantics: "metered_v2", Metered: &metering.Price{Operation: "video", Policy: "sora_completed_seconds_v1"}}
	if RateMatchesWire(otherVideo, "videos.create") {
		t.Fatal("planned video wire authorized")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate := Rate{Semantics: "metered_v2", Metered: &metering.Price{Operation: tc.operation, Policy: tc.policy}}
			if !RateMatchesWire(rate, tc.wire) {
				t.Fatalf("%s incorrectly denied", tc.name)
			}
		})
	}
}

func TestPlannedVideoRetainsPreviouslyRecordedReplayAndSettlement(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := accounts.New(pool)
	user, err := store.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://historical-video", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	accountsList, err := store.ListAccounts(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	account := accountsList[0].ID
	ledgerFixture := &rateTestLedger{balances: map[string]int64{}, transactions: map[string]ledger.Transaction{}}
	service := New(pool, store, ledgerFixture)
	service.ConfigureTrustedServices([]string{"router"})
	for _, tc := range []struct {
		id, wire, instance, task string
		settle                   bool
	}{
		{id: "old-legacy", wire: "", instance: "", task: "", settle: false},
		{id: "old-wire", wire: "videos.create", instance: "default", task: "task", settle: true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			rate := Rate{ID: "previous-rate", Model: "previous-model", Asset: CashAsset, Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_video_duration_v1", Terms: []metering.Term{{Key: "video", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
			request := ReserveRequest{CallID: tc.id, ActorID: user.ID, AccountID: account, OriginProductID: "superwagie", RequestDigest: "digest", RateID: rate.ID, Amount: "10"}
			requestHash := reserveRequestHash("router", request, tc.instance, tc.task)
			if tc.wire != "" {
				requestHash = hash(struct{ Base, Wire string }{requestHash, tc.wire})
			}
			old := Call{ServiceID: "router", CallID: tc.id, ActorID: user.ID, AccountID: account, OriginProductID: "superwagie", RequestDigest: "digest", Rate: rate, Held: "10", Charged: "0", PlatformDifference: "0", Status: "RESERVED", WireOperation: tc.wire, InstanceID: tc.instance, TaskID: tc.task, ChargeAsset: CashAsset, FundingMode: FundingPrepaidCash, FundingProductID: "llmrouter", CashHoldAddress: CashHoldAddress(account, tc.id)}
			tx, e := pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer database.Rollback(tx)
			if _, e = tx.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,$3,$4)`, old.CallID, account, requestHash, encode(old)); e != nil {
				t.Fatal(e)
			}
			command, e := service.createCommand(ctx, tx, account, user.ID, "reserve", old.CallID, []ledger.Posting{{Source: "world", Destination: old.CashHoldAddress, Asset: CashAsset, Amount: 10}}, effect{Call: &old})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(ctx, `UPDATE billing_commands SET status='APPLIED' WHERE id=$1`, command.ID); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			ledgerFixture.balances[old.CashHoldAddress+"/"+CashAsset] = 10
			got, e := service.ReserveWithWire(ctx, "router", request, tc.instance, tc.task, tc.wire)
			if e != nil || got.Status != "RESERVED" || got.Held != "10" {
				t.Fatalf("historical replay: %+v %v", got, e)
			}
			if tc.settle {
				usage := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "video", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "video", Unit: "video_second", Quantity: metering.Quantity{Value: "5"}}}}}
				got, e = service.Settle(ctx, "router", SettlementRequest{CallID: old.CallID, Usage: &usage})
				if e != nil || got.Status != "SETTLED" || got.Charged != "5" {
					t.Fatalf("historical settlement: %+v %v", got, e)
				}
			} else {
				got, e = service.Release(ctx, "router", old.CallID, "historical-no-output")
				if e != nil || got.Status != "RELEASED" {
					t.Fatalf("historical release: %+v %v", got, e)
				}
			}
		})
	}
}
