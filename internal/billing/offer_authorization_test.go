package billing

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
)

func TestOfferAuthorizationUsesOnlyImmutablePublishedOwnership(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	draft := Offer{ID: "draft-ownership", ProductID: "superwagie", Name: "Draft", AccountKind: "both", Kind: "membership", Period: "month", Price: "1", MonthlyCredit: "1"}
	if _, e := f.s.SaveOffer(ctx, f.actor, draft); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.OfferProduct(ctx, draft.ID); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatalf("mutable draft ownership accepted %v", e)
	}
	if _, e := f.s.PublishOffer(ctx, f.actor, draft.ID); e != nil {
		t.Fatal(e)
	}
	profile, _ := BillingPreset("superwagie", "prepaid")
	if _, e := f.s.SaveProfile(ctx, f.actor, "superwagie", profile); e != nil {
		t.Fatal(e)
	}
	if product, e := f.s.OfferProduct(ctx, draft.ID); e != nil || product != "superwagie" {
		t.Fatalf("published ownership lost after policy change %s %v", product, e)
	}
}
