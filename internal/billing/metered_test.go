package billing

import (
	"context"
	"encoding/json"
	"errors"
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
	changed := nativeRate()
	changed.ID = r.ID
	changed.Model = r.Model
	changed.Metered.Terms[0].Price.Numerator = "999"
	if _, err := f.s.pool.Exec(ctx, `UPDATE billing_rates SET snapshot=$1 WHERE id=$2`, encode(changed), r.ID); err != nil {
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
	changedUsage := nativeUsage("1501")
	if _, err := f.s.Settle(ctx, "router", SettlementRequest{CallID: "native-call", Usage: &changedUsage, ProviderCost: "17"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	if _, err := f.s.Reserve(ctx, "router", ReserveRequest{CallID: "native-zero", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "zero", RateID: r.ID, Amount: "1"}); err != nil {
		t.Fatal(err)
	}
	zero := nativeUsage("0")
	zeroCall, err := f.s.Settle(ctx, "router", SettlementRequest{CallID: "native-zero", Usage: &zero})
	if err != nil || zeroCall.Status != "SETTLED" || zeroCall.Charged != "0" {
		t.Fatalf("zero: %+v %v", zeroCall, err)
	}
	summary, err := f.s.QueryUsageSummary(ctx, f.actor, f.account, CallFilter{})
	if err != nil || !summary.UsageComplete || summary.Usage.Input != "0" || len(summary.NativeUsage) != 1 || summary.NativeUsage[0].Measurements[0].Quantity.Value != "1500" {
		t.Fatalf("summary %+v %v", summary, err)
	}
}

func TestNativeUsageWireOmitsLegacyCounters(t *testing.T) {
	b, err := json.Marshal(nativeUsage("0"))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 1 || wire["metered"] == nil {
		t.Fatalf("native wire: %s", b)
	}
}
