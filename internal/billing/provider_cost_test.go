package billing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestSettlementOptionalProviderCostRemainsIndependentAndIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name, payload, cost string
	}{
		{"omitted", `{}`, ""},
		{"empty", `{"provider_cost":""}`, ""},
		{"zero", `{"provider_cost":"0"}`, "0"},
		{"known", `{"provider_cost":"27"}`, "27"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.auth(t)
			ctx := context.Background()
			f.buy(t, "sandbox-sw-month-1", "purchase")
			rateFixture(t, f)
			if _, err := f.s.Reserve(ctx, "router", ReserveRequest{"call", f.actor, f.account, "superwagie", "digest", "r", "100", ""}); err != nil {
				t.Fatal(err)
			}
			var req SettlementRequest
			if err := json.Unmarshal([]byte(tc.payload), &req); err != nil {
				t.Fatal(err)
			}
			req.CallID = "call"
			// Missing usage must keep the hold even when provider cost is unknown.
			missing, err := f.s.Settle(ctx, "router", req)
			if err != nil || missing.Status != "MISSING_USAGE" || missing.ProviderCost != tc.cost {
				t.Fatalf("missing usage: %+v %v", missing, err)
			}
			req.Usage = &Usage{Input: "150", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
			for range 2 {
				got, err := f.s.Settle(ctx, "router", req)
				if err != nil || got.Status != "SETTLED" || got.Charged != "100" || got.PlatformDifference != "50" || got.ProviderCost != tc.cost {
					t.Fatalf("settlement: %+v %v", got, err)
				}
			}
			stored, err := f.s.GetCall(ctx, "router", req.CallID)
			if err != nil || stored.ProviderCost != tc.cost {
				t.Fatalf("stored provider observation: %+v %v", stored, err)
			}
			// Unknown and known-zero costs must not silently replay as equivalent.
			changed := req
			if req.ProviderCost == "" {
				changed.ProviderCost = "0"
			} else {
				changed.ProviderCost = ""
			}
			if _, err := f.s.Settle(ctx, "router", changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("changed provider observation accepted: %v", err)
			}
			var commands int
			if err := f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE kind='settle' AND account_id=$1`, f.account).Scan(&commands); err != nil || commands != 1 {
				t.Fatalf("settlement commands=%d err=%v", commands, err)
			}
			batches, err := f.s.Batches(ctx, f.actor, f.account)
			if err != nil || len(batches) != 1 || batches[0].Consumed != "100" || batches[0].Reserved != "0" || batches[0].Available != "999900" {
				t.Fatalf("ledger quantities: %+v %v", batches, err)
			}
		})
	}
}

func TestSettlementRejectsInvalidProvidedCostWithoutChangingHold(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-month-1", "purchase")
	rateFixture(t, f)
	if _, err := f.s.Reserve(ctx, "router", ReserveRequest{"call", f.actor, f.account, "superwagie", "digest", "r", "100", ""}); err != nil {
		t.Fatal(err)
	}
	for _, cost := range []string{"-1", "1.5", "invalid", " ", " 0", "+1", "9223372036854775808"} {
		if _, err := f.s.Settle(ctx, "router", SettlementRequest{"call", &Usage{Input: "50", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}, cost}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("cost %q: %v", cost, err)
		}
	}
	got, err := f.s.GetCall(ctx, "router", "call")
	if err != nil || got.Status != "RESERVED" || got.ProviderCost != "" {
		t.Fatalf("invalid cost mutated call: %+v %v", got, err)
	}
	batches, err := f.s.Batches(ctx, f.actor, f.account)
	if err != nil || len(batches) != 1 || batches[0].Reserved != "100" || batches[0].Consumed != "0" {
		t.Fatalf("invalid cost changed ledger: %+v %v", batches, err)
	}
}
