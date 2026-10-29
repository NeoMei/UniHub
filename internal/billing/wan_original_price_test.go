package billing

import (
	"context"
	"testing"
	"time"
	"unihub/internal/metering"
	"unihub/internal/wallet"
)

func TestWanOriginalCashPriceUsesMicroYuanAndOneResolution(t *testing.T) {
	for _, policy := range []struct{ model, name string }{
		{"wan2.6-t2v", "ali_wan26_t2v_cny6_v2"},
		{"wan2.7-t2v", "ali_wan27_t2v_cny6_v1"},
		{"wan2.7-t2v-2026-06-12", "ali_wan27_t2v_cny6_v1"},
	} {
		t.Run(policy.model, func(t *testing.T) {
			r := Rate{ID: "wan-rate", Model: policy.model, Asset: CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "video", Policy: policy.name, Terms: []metering.Term{
				{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "600000", Denominator: "1"}},
				{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1000000", Denominator: "1"}},
			}}}
			if err := validateRate(r); err != nil {
				t.Fatalf("price rejected: %v", err)
			}
			if !RateMatchesWire(r, "videos.create") || RateMatchesWire(r, "images.generations.create") {
				t.Fatal("wrong wire operation authorization")
			}
			for _, tc := range []struct{ tier, want string }{{"video_720p", "3000000"}, {"video_1080p", "5000000"}} {
				u := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "video", State: "confirmed", Source: policy.name, ProviderRequestID: "supplier-task", Measurements: []metering.Measurement{{Key: tc.tier, Unit: "video_second", Quantity: metering.Quantity{Value: "5"}}}}}
				got, err := CalculateCharge(r, u)
				if err != nil || got != tc.want {
					t.Fatalf("%s charge=%s err=%v", tc.tier, got, err)
				}
				u.Metered.Measurements = append(u.Metered.Measurements, metering.Measurement{Key: "video_720p", Unit: "video_second", Quantity: metering.Quantity{Value: "5"}})
				if _, err := CalculateCharge(r, u); err == nil {
					t.Fatal("multiple or duplicated tiers accepted")
				}
			}
			bad := r
			bad.Asset = CreditAsset
			if validateRate(bad) == nil {
				t.Fatal("CNY policy accepted credit asset")
			}
			bad = r
			bad.Model = "unrelated-model"
			if validateRate(bad) == nil {
				t.Fatal("CNY policy accepted unrelated model")
			}
			bad = r
			copyPrice := *r.Metered
			copyPrice.Terms = append([]metering.Term(nil), r.Metered.Terms...)
			bad.Metered = &copyPrice
			bad.Metered.Terms[0].Price = metering.Rational{Numerator: "3", Denominator: "5"}
			if validateRate(bad) == nil {
				t.Fatal("yuan-denominated price accepted as micro-yuan")
			}
			bad.Metered.Terms[0] = r.Metered.Terms[0]
			bad.Metered.Terms[0].Step = &metering.Quantity{Value: "10"}
			if validateRate(bad) == nil {
				t.Fatal("rounding step altered original price")
			}
		})
	}
}

func TestWanOriginalPriceRealLedgerReservationSettlement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.s.SaveProfile(ctx, f.actor, "superwagie", BillingProfile{ProductID: "superwagie", Usage: UsagePolicy{Enabled: true, FundingModes: []string{FundingPrepaidCash}, DefaultFundingMode: FundingPrepaidCash}}); err != nil {
		t.Fatal(err)
	}
	fundCash(t, f, "llmrouter", 10000000)
	policy := "ali_wan27_t2v_cny6_v1"
	rate := Rate{ID: "wan-price", Model: "wan2.7-t2v", Asset: CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "video", Policy: policy, Terms: []metering.Term{
		{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "600000", Denominator: "1"}},
		{Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1000000", Denominator: "1"}},
	}}}
	if _, err := f.s.PublishRate(ctx, f.actor, rate); err != nil {
		t.Fatal(err)
	}
	cid := id("mc_")
	if _, err := f.s.pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version) VALUES($1,$1,$2,$3,'superwagie','["wan2.7-t2v"]','wan-test',$4,$5,$6,1)`, cid, f.actor, f.account, f.s.now().Add(time.Hour), FundingPrepaidCash, CashAsset); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tier, amount string
		remaining    int64
	}{{"video_720p", "3000000", 7000000}, {"video_1080p", "5000000", 2000000}} {
		callID := "wan-" + tc.tier
		req := ReserveRequest{CallID: callID, ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "digest", RateID: rate.ID, Amount: tc.amount, CredentialID: cid}
		call, err := f.s.ReserveWithWire(ctx, "router", req, "default", "wan-test", "videos.create")
		if err != nil || call.Held != tc.amount || call.ChargeAsset != CashAsset {
			t.Fatalf("reserve %+v %v", call, err)
		}
		cashBalance(t, f, wallet.Address(f.account, "llmrouter"), tc.remaining)
		usage := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "video", State: "confirmed", Source: policy, ProviderRequestID: "provider-" + tc.tier, Measurements: []metering.Measurement{{Key: tc.tier, Unit: "video_second", Quantity: metering.Quantity{Value: "5"}}}}}
		call, err = f.s.Settle(ctx, "router", SettlementRequest{CallID: callID, Usage: &usage})
		if err != nil || call.Charged != tc.amount || call.Status != "SETTLED" {
			t.Fatalf("settle %+v %v", call, err)
		}
		if _, err = f.s.Settle(ctx, "router", SettlementRequest{CallID: callID, Usage: &usage}); err != nil {
			t.Fatal(err)
		}
		cashBalance(t, f, CashHoldAddress(f.account, callID), 0)
		cashBalance(t, f, wallet.Address(f.account, "llmrouter"), tc.remaining)
	}
	cashBalance(t, f, "commercial:revenue:llmrouter", 8000000)
}

func TestWanCashSettlementRejectsInvalidProviderDuration(t *testing.T) {
	policy := "ali_wan27_t2v_cny6_v1"
	r := Rate{Model: "wan2.7-t2v", Asset: CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "video", Policy: policy, Terms: []metering.Term{{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "600000", Denominator: "1"}}, {Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1000000", Denominator: "1"}}}}}
	for _, tc := range []struct {
		name, value string
		scale       int
	}{{"zero", "0", 0}, {"one", "1", 0}, {"fraction", "15", 1}, {"sixteen", "16", 0}, {"huge", "99999999999999999999", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			u := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "video", State: "confirmed", Source: policy, ProviderRequestID: "task", Measurements: []metering.Measurement{{Key: "video_720p", Unit: "video_second", Quantity: metering.Quantity{Value: tc.value, Scale: tc.scale}}}}}
			if _, err := CalculateCharge(r, u); err == nil {
				t.Fatal("invalid provider duration was charged")
			}
		})
	}
}
