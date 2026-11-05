package billing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/metering"
	"unihub/internal/testutil"
)

func assertUsageCoverage(t *testing.T, summary UsageSummary, legacy, native string) {
	t.Helper()
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Coverage map[string]string `json:"usage_coverage"`
	}
	if err = json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Coverage) != 2 || wire.Coverage["legacy_token_calls"] != legacy || wire.Coverage["native_metered_calls"] != native {
		t.Fatalf("usage_coverage want legacy=%s native=%s; got %s", legacy, native, data)
	}
}

func TestUsageCoverageReadContract(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	owner, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://coverage-test", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := a.ListAccounts(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := New(p, a, nil)
	account := as[0].ID
	when := dt("2026-10-20T12:00:00Z")
	legacy := Usage{Input: "0", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
	rate := Rate{Asset: CreditAsset, Semantics: "metered_v2", Model: "request-model", Metered: &metering.Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
	native := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1", Scale: 0}}}}}
	for _, tc := range []struct{ name, legacy, native string }{{"pending-only", "0", "0"}, {"legacy-only", "1", "0"}, {"zero-token-legacy", "1", "0"}, {"native-request-only", "0", "1"}, {"mixed", "1", "1"}} {
		t.Run(tc.name, func(t *testing.T) {
			filter := CallFilter{OriginProductID: tc.name}
			usage := legacy
			if tc.name == "legacy-only" {
				usage.Input = "7"
			}
			if tc.legacy == "1" {
				queryInsert(t, s, Call{CallID: tc.name + "-legacy", AccountID: account, ActorID: owner.ID, OriginProductID: tc.name, Status: "SETTLED", Charged: "0", Usage: &usage}, &when)
			}
			if tc.native == "1" {
				queryInsert(t, s, Call{CallID: tc.name + "-native", AccountID: account, ActorID: owner.ID, OriginProductID: tc.name, Status: "SETTLED", Charged: "1", Rate: rate, Usage: &native}, &when)
			}
			if tc.name == "pending-only" {
				queryInsert(t, s, Call{CallID: tc.name + "-pending", AccountID: account, ActorID: owner.ID, OriginProductID: tc.name, Status: "PENDING", Charged: "0", Usage: &legacy}, &when)
			}
			summary, err := s.QueryUsageSummary(ctx, owner.ID, account, filter)
			if err != nil {
				t.Fatal(err)
			}
			assertUsageCoverage(t, summary, tc.legacy, tc.native)
			wantInput := "0"
			if tc.name == "legacy-only" {
				wantInput = "7"
			}
			if summary.Usage.Input != wantInput || summary.UsageComplete != (tc.name != "pending-only") {
				t.Fatalf("legacy quantities or completeness changed: %+v", summary)
			}
			if tc.native == "1" && (len(summary.NativeUsage) != 1 || summary.NativeUsage[0].Measurements[0].Quantity.Value != "1") {
				t.Fatalf("native quantity changed: %+v", summary)
			}
		})
	}
	t.Run("truly-empty", func(t *testing.T) {
		summary, err := s.QueryUsageSummary(ctx, owner.ID, account, CallFilter{OriginProductID: "absent"})
		if err != nil {
			t.Fatal(err)
		}
		assertUsageCoverage(t, summary, "0", "0")
		if !summary.UsageComplete {
			t.Fatal("empty completeness changed")
		}
	})
	// Same instance, source, model, asset and time bounds as the existing quantities.
	for _, filter := range []CallFilter{{InstanceID: "other"}, {Source: "product_runtime"}, {Model: "other"}, {Asset: CashAsset}, {From: when.Add(time.Second)}, {To: when}} {
		summary, err := s.QueryUsageSummary(ctx, owner.ID, account, filter)
		if err != nil {
			t.Fatal(err)
		}
		assertUsageCoverage(t, summary, "0", "0")
	}
	// Missing legacy usage remains a reporting gap, never a covered zero call.
	queryInsert(t, s, Call{CallID: "missing", AccountID: account, ActorID: owner.ID, OriginProductID: "missing", Status: "SETTLED", Charged: "0"}, &when)
	summary, err := s.QueryUsageSummary(ctx, owner.ID, account, CallFilter{OriginProductID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	assertUsageCoverage(t, summary, "0", "0")
	if summary.UsageComplete {
		t.Fatal("missing usage marked complete")
	}
	// Coverage cannot rescue malformed stored native evidence.
	native.Metered.Measurements[0].Quantity.Value = "01"
	queryInsert(t, s, Call{CallID: "invalid", AccountID: account, ActorID: owner.ID, OriginProductID: "invalid", Status: "SETTLED", Charged: "1", Rate: rate, Usage: &native}, &when)
	if _, err = s.QueryUsageSummary(ctx, owner.ID, account, CallFilter{OriginProductID: "invalid"}); err == nil {
		t.Fatal("malformed native measurement accepted")
	}
	outsider, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://coverage-test", Subject: "outsider"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueryUsageSummary(ctx, outsider.ID, account, CallFilter{}); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("outsider: %v", err)
	}
}
