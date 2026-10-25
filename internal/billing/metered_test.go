package billing

import (
	"context"
	"encoding/json"
	"testing"
	"unihub/internal/metering"
)

func nativeRate() Rate {
	return Rate{Asset: CreditAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}}}}
}
func nativeUsage(v string) Usage {
	return Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: v, Scale: 0}}}}}
}
func TestMeteredChargeContract(t *testing.T) {
	for _, tc := range []struct{ value, want string }{{"1500", "2"}, {"0", "0"}, {"1000", "1"}} {
		got, err := CalculateCharge(nativeRate(), nativeUsage(tc.value))
		if err != nil || got != tc.want {
			t.Fatalf("%s: %s %v", tc.value, got, err)
		}
	}
	r, u := nativeRate(), nativeUsage("1")
	u.Input = "0"
	if _, err := CalculateCharge(r, u); err == nil {
		t.Fatal("mixed usage accepted")
	}
	u = nativeUsage("1")
	u.Metered.Operation = "chat"
	if _, err := CalculateCharge(r, u); err == nil {
		t.Fatal("wrong operation accepted")
	}
	r.Input = Rational{"0", "1"}
	if _, err := CalculateCharge(r, nativeUsage("1")); err == nil {
		t.Fatal("mixed rate accepted")
	}
}
func TestLegacyUsageJSONUnchanged(t *testing.T) {
	b, err := json.Marshal(Usage{Input: "1", Output: "2", CacheRead: "3", CacheWrite: "4", Reasoning: "5"})
	if err != nil || string(b) != `{"input":"1","output":"2","cache_read":"3","cache_write":"4","reasoning":"5"}` {
		t.Fatalf("%s %v", b, err)
	}
}

func TestMeteredSettlementFrozenReplayAndSummary(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-month-1", "native-buy")
	r := nativeRate()
	r.ID = "native"
	r.Model = "speech-model"
	if _, err := f.s.PublishRate(ctx, f.actor, r); err != nil {
		t.Fatal(err)
	}
	bad := r
	bad.ID = "duplicate"
	bad.Metered = &metering.Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: append(r.Metered.Terms, r.Metered.Terms...)}
	if _, err := f.s.PublishRate(ctx, f.actor, bad); err == nil {
		t.Fatal("duplicate published")
	}
	if _, err := f.s.Reserve(ctx, "router", ReserveRequest{CallID: "native-call", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "d", RateID: r.ID, Amount: "1"}); err != nil {
		t.Fatal(err)
	}
	call, err := f.s.Settle(ctx, "router", SettlementRequest{CallID: "native-call"})
	if err != nil || call.Status != "MISSING_USAGE" {
		t.Fatalf("missing %+v %v", call, err)
	}
	u := nativeUsage("1500")
	req := SettlementRequest{CallID: "native-call", Usage: &u, ProviderCost: "17"}
	call, err = f.s.Settle(ctx, "router", req)
	if err != nil || call.Charged != "1" || call.PlatformDifference != "1" || call.ProviderCost != "17" {
		t.Fatalf("settled %+v %v", call, err)
	}
	replay, err := f.s.Settle(ctx, "router", req)
	if err != nil || replay.FinalDigest != call.FinalDigest {
		t.Fatalf("replay %+v %v", replay, err)
	}
	summary, err := f.s.QueryUsageSummary(ctx, f.actor, f.account, CallFilter{})
	if err != nil || !summary.UsageComplete || summary.Usage.Input != "0" || len(summary.NativeUsage) != 1 || summary.NativeUsage[0].Measurements[0].Quantity.Value != "1500" {
		t.Fatalf("summary %+v %v", summary, err)
	}
}
