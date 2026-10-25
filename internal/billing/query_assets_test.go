package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUsageSeparatesCashAndCreditsAndBindsAssetCursor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	when := time.Now().UTC()
	for _, c := range []Call{
		{CallID: "a-credit", AccountID: f.account, ActorID: f.actor, OriginProductID: "superwagie", Rate: Rate{Model: "m"}, Status: "SETTLED", Charged: "3", Usage: &Usage{"1", "0", "0", "0", "0"}},
		{CallID: "b-cash", AccountID: f.account, ActorID: f.actor, OriginProductID: "superwagie", ChargeAsset: CashAsset, FundingMode: FundingPrepaidCash, Rate: Rate{Model: "m", Asset: CashAsset}, Status: "SETTLED", Charged: "7", Usage: &Usage{"2", "0", "0", "0", "0"}},
		{CallID: "c-cash", AccountID: f.account, ActorID: f.actor, OriginProductID: "superwagie", ChargeAsset: CashAsset, FundingMode: FundingPrepaidCash, Rate: Rate{Model: "m", Asset: CashAsset}, Status: "RESERVED", Charged: "0"},
	} {
		queryInsert(t, f.s, c, &when)
	}
	summary, e := f.s.QueryUsageSummary(ctx, f.actor, f.account, CallFilter{})
	if e != nil || summary.Asset != "mixed" || summary.Charged != "" || summary.ChargesByAsset[CreditAsset] != "3" || summary.ChargesByAsset[CashAsset] != "7" {
		t.Fatalf("mixed currency summary %+v %v", summary, e)
	}
	view, e := f.s.QueryCall(ctx, f.actor, f.account, "b-cash", "superwagie")
	if e != nil || view.Asset != CashAsset || view.Charged != "7" {
		t.Fatalf("cash view %+v %v", view, e)
	}
	filter := CallFilter{Asset: CashAsset}
	summary, e = f.s.QueryUsageSummary(ctx, f.actor, f.account, filter)
	if e != nil || summary.Asset != CashAsset || summary.Charged != "7" || summary.CallsTotal != "2" {
		t.Fatalf("cash summary %+v %v", summary, e)
	}
	first, e := f.s.QueryCalls(ctx, f.actor, f.account, filter, CallPageRequest{Limit: 1})
	if e != nil || first.Total != "2" || first.NextCursor == "" {
		t.Fatalf("cash page %+v %v", first, e)
	}
	restored, e := RestoreCallFilter(first.NextCursor)
	if e != nil || restored.Asset != CashAsset {
		t.Fatalf("cursor asset %+v %v", restored, e)
	}
	filter.Asset = CreditAsset
	if _, e = f.s.QueryCalls(ctx, f.actor, f.account, filter, CallPageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("asset cursor reinterpreted: %v", e)
	}
	if _, e = f.s.QueryUsageSummary(ctx, f.actor, f.account, CallFilter{Asset: "USD/2"}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unsupported currency %v", e)
	}
}
