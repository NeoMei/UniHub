package billing

import (
	"context"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/metering"
	"unihub/internal/testutil"
)

// A credit-v1 call stores a metered_v2 observation, although its rate keeps
// llmrouter_native semantics. The observation is audit data: the native quota
// and frozen conversion determine the charge.
func creditMeteredCall(id, account, actor, origin string, usage *Usage) Call {
	return Call{
		CallID:           id,
		AccountID:        account,
		ActorID:          actor,
		OriginProductID:  origin,
		Status:           "SETTLED",
		Charged:          "11",
		ChargeAsset:      CreditAsset,
		FundingMode:      FundingCredits,
		FundingProductID: "superwagie",
		Rate:             Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"},
		NativePricing: &NativeQuote{
			PricingVersion: NativeCreditPricingVersion,
			Asset:          CreditAsset,
			BillingModel:   "credit-model",
			Conversion:     NativeConversion{Version: "test-points-v1", Numerator: "3", Denominator: "2", Rounding: "lr-cumulative-ceil-v1"},
		},
		NativeQuota: "7",
		Usage:       usage,
	}
}

func creditMeteredUsage(quantity string) *Usage {
	return &Usage{Metered: &metering.Usage{
		Version:   "metered_v2",
		State:     "confirmed",
		Source:    "provider",
		Operation: "chat",
		Measurements: []metering.Measurement{
			{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: quantity, Scale: 0}},
			{Key: "output", Unit: "token", Quantity: metering.Quantity{Value: "3", Scale: 0}},
		},
	}}
}

func TestUsageSummaryAcceptsCreditNativeObservations(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	owner, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://credit-usage-summary-test", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := a.ListAccounts(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	account := as[0].ID
	s := New(p, a, nil)
	when := dt("2026-11-13T10:22:20Z")
	legacy := Usage{Input: "5", Output: "2", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
	nativeRate := Rate{Asset: CreditAsset, Semantics: "metered_v2", Model: "request-model", Metered: &metering.Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
	nativeUsage := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1", Scale: 0}}}}}
	records := []Call{
		{CallID: "credit-1", AccountID: account, ActorID: owner.ID, OriginProductID: "credit-valid", Status: "SETTLED", Charged: "11", Rate: Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: NativeCreditPricingVersion, Asset: CreditAsset}, Usage: creditMeteredUsage("7")},
		{CallID: "credit-2", AccountID: account, ActorID: owner.ID, OriginProductID: "credit-valid", Status: "SETTLED", Charged: "6", Rate: Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: NativeCreditPricingVersion, Asset: CreditAsset}, Usage: creditMeteredUsage("1")},
		{CallID: "legacy-v1", AccountID: account, ActorID: owner.ID, OriginProductID: "legacy-v1", Status: "SETTLED", Charged: "1", Rate: Rate{Asset: CreditAsset, Model: "legacy-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: "lr-native-v1", Asset: CashAsset}, Usage: &legacy},
		{CallID: "central-metered", AccountID: account, ActorID: owner.ID, OriginProductID: "central-metered", Status: "SETTLED", Charged: "1", Rate: nativeRate, Usage: &nativeUsage},
		{CallID: "credit-no-usage", AccountID: account, ActorID: owner.ID, OriginProductID: "credit-no-usage", Status: "SETTLED", Charged: "0", Rate: Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: NativeCreditPricingVersion, Asset: CreditAsset}},
		{CallID: "credit-uncharged", AccountID: account, ActorID: owner.ID, OriginProductID: "credit-uncharged", Status: "SETTLED", Charged: "01", Rate: Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: NativeCreditPricingVersion, Asset: CreditAsset}, Usage: creditMeteredUsage("7")},
		{CallID: "credit-legacy-counters", AccountID: account, ActorID: owner.ID, OriginProductID: "credit-legacy-counters", Status: "SETTLED", Charged: "11", Rate: Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: NativeCreditPricingVersion, Asset: CreditAsset}, Usage: &Usage{Input: "1.5"}},
		{CallID: "credit-mixed", AccountID: account, ActorID: owner.ID, OriginProductID: "credit-mixed", Status: "SETTLED", Charged: "11", Rate: Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: NativeCreditPricingVersion, Asset: CreditAsset}, Usage: &Usage{Input: "1", Metered: creditMeteredUsage("7").Metered}},
		{CallID: "credit-bad-measurement", AccountID: account, ActorID: owner.ID, OriginProductID: "credit-bad-measurement", Status: "SETTLED", Charged: "11", Rate: Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: NativeCreditPricingVersion, Asset: CreditAsset}, Usage: creditMeteredUsage("01")},
		{CallID: "central-bad-measurement", AccountID: account, ActorID: owner.ID, OriginProductID: "central-bad-measurement", Status: "SETTLED", Charged: "1", Rate: nativeRate, Usage: &Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "01", Scale: 0}}}}}},
	}
	for _, call := range records {
		queryInsert(t, s, call, &when)
	}

	summary, err := s.QueryUsageSummary(ctx, owner.ID, account, CallFilter{OriginProductID: "credit-valid"})
	if err != nil {
		t.Fatalf("valid credit observations rejected: %v", err)
	}
	assertUsageCoverage(t, summary, "0", "2")
	if !summary.UsageComplete || summary.SettledCalls != "2" || summary.Charged != "17" || summary.Usage.Input != "0" {
		t.Fatalf("valid credit summary: %+v", summary)
	}
	if len(summary.NativeUsage) != 1 || summary.NativeUsage[0].Operation != "chat" || len(summary.NativeUsage[0].Measurements) != 2 || summary.NativeUsage[0].Measurements[0].Quantity.Value != "8" || summary.NativeUsage[0].Measurements[1].Quantity.Value != "6" {
		t.Fatalf("credit dimensions: %+v", summary.NativeUsage)
	}

	for _, tc := range []struct {
		origin   string
		legacy   string
		native   string
		complete bool
	}{
		{"legacy-v1", "1", "0", true},
		{"central-metered", "0", "1", true},
		{"credit-no-usage", "0", "0", false},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			got, err := s.QueryUsageSummary(ctx, owner.ID, account, CallFilter{OriginProductID: tc.origin})
			if err != nil {
				t.Fatalf("compatible record rejected: %v", err)
			}
			assertUsageCoverage(t, got, tc.legacy, tc.native)
			if got.UsageComplete != tc.complete {
				t.Fatalf("usage_complete=%v, want %v: %+v", got.UsageComplete, tc.complete, got)
			}
		})
	}

	for _, origin := range []string{"credit-uncharged", "credit-legacy-counters", "credit-mixed", "credit-bad-measurement", "central-bad-measurement"} {
		t.Run("reject-"+origin, func(t *testing.T) {
			if _, err := s.QueryUsageSummary(ctx, owner.ID, account, CallFilter{OriginProductID: origin}); err == nil {
				t.Fatalf("invalid stored counters accepted for %s", origin)
			}
		})
	}

	// A later valid record must not mask an earlier damaged record.
	queryInsert(t, s, Call{CallID: "credit-valid-after-bad", AccountID: account, ActorID: owner.ID, OriginProductID: "credit-bad-measurement", Status: "SETTLED", Charged: "2", Rate: Rate{Asset: CreditAsset, Model: "credit-model", Semantics: "llmrouter_native"}, NativePricing: &NativeQuote{PricingVersion: NativeCreditPricingVersion, Asset: CreditAsset}, Usage: creditMeteredUsage("2")}, &when)
	if _, err := s.QueryUsageSummary(ctx, owner.ID, account, CallFilter{OriginProductID: "credit-bad-measurement"}); err == nil {
		t.Fatal("damaged observation masked by a later valid call")
	}
}

// The same stored shape must work when the query has an explicit time window.
func TestUsageSummaryCreditObservationsRespectTimeFilter(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	owner, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://credit-usage-summary-time-test", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := a.ListAccounts(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	account := as[0].ID
	s := New(p, a, nil)
	start := dt("2026-11-12T10:22:20Z")
	end := dt("2026-11-14T08:03:46Z")
	queryInsert(t, s, creditMeteredCall("inside", account, owner.ID, "credit-window", creditMeteredUsage("7")), &start)
	outside := end
	queryInsert(t, s, creditMeteredCall("outside", account, owner.ID, "credit-window", creditMeteredUsage("1")), &outside)

	summary, err := s.QueryUsageSummary(ctx, owner.ID, account, CallFilter{OriginProductID: "credit-window", From: start, To: end})
	if err != nil {
		t.Fatalf("windowed credit summary: %v", err)
	}
	assertUsageCoverage(t, summary, "0", "1")
	if !summary.UsageComplete || summary.Charged != "11" || summary.NativeUsage[0].Measurements[0].Quantity.Value != "7" {
		t.Fatalf("selected call outside the requested window: %+v", summary)
	}
}
