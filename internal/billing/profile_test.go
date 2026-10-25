package billing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

// Zero monthly credit is a membership term; it must never turn an addon into a
// zero-valued grant or pretend that a service-only subscription is unused.
func TestPureMembershipOfferValidation(t *testing.T) {
	pure := Offer{ID: "pure", ProductID: "superwagie", Name: "Membership", AccountKind: "both", Kind: "membership", Period: "month", Price: "1000000", MonthlyCredit: "0"}
	if e := validateOffer(pure); e != nil {
		t.Fatalf("pure membership rejected: %v", e)
	}
	pure.RefundRule = "unused_only_v1"
	if e := validateOffer(pure); !errors.Is(e, ErrInvalid) {
		t.Fatalf("pure auto-refund accepted: %v", e)
	}
	pure.RefundRule = ""
	pure.Kind = "addon"
	pure.Period = ""
	pure.ValidityMonths = 1
	if e := validateOffer(pure); !errors.Is(e, ErrInvalid) {
		t.Fatalf("zero addon accepted: %v", e)
	}
}

func profileService(t *testing.T) *Service {
	t.Helper()
	p := testutil.Database(t)
	if e := database.Migrate(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	s := New(p, accounts.New(p), nil)
	s.ConfigureOperators([]string{"operator"})
	return s
}

func TestBillingProfilePresetsAndComposition(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		sub, included, addons, usage, required bool
		funding                                string
	}{
		{"hybrid", true, true, true, true, true, "credits"},
		{"prepaid", false, false, false, true, false, "prepaid_cash"},
		{"subscription", true, false, false, false, false, ""},
	} {
		p, e := BillingPreset("superwagie", tc.name)
		if e != nil {
			t.Fatal(e)
		}
		if p.Version != 0 || p.ProductID != "superwagie" || p.Subscription.Enabled != tc.sub || p.IncludedCredits.Enabled != tc.included || p.Addons.Enabled != tc.addons || p.Usage.Enabled != tc.usage || p.Usage.RequireSubscription != tc.required || p.Usage.DefaultFundingMode != tc.funding {
			t.Fatalf("%s: %+v", tc.name, p)
		}
		if e = p.Validate(); e != nil {
			t.Fatalf("preset %s: %v", tc.name, e)
		}
	}
	p, _ := BillingPreset("superwagie", "prepaid")
	p.Subscription.Enabled = true
	p.Usage.RequireSubscription = true
	if e := p.Validate(); e != nil {
		t.Fatalf("subscription + prepaid rejected: %v", e)
	}
	p.Usage.FundingModes = []string{"credits", "prepaid_cash"}
	p.Addons.Enabled = true
	if e := p.Validate(); e != nil {
		t.Fatalf("standalone addon + dual funding rejected: %v", e)
	}
	for _, mutate := range []func(*BillingProfile){
		func(p *BillingProfile) { p.IncludedCredits.Enabled = true; p.Subscription.Enabled = false },
		func(p *BillingProfile) { p.Usage.FundingModes = []string{"prepaid_cash"} },
		func(p *BillingProfile) { p.Usage.DefaultFundingMode = "unknown" },
		func(p *BillingProfile) { p.Usage.FundingModes = []string{"credits", "credits"} },
		func(p *BillingProfile) { p.Usage.Enabled = false },
		func(p *BillingProfile) { p.Version = -1 },
	} {
		bad := p
		bad.Usage.FundingModes = append([]string(nil), p.Usage.FundingModes...)
		mutate(&bad)
		if e := bad.Validate(); !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid composition accepted: %+v %v", bad, e)
		}
	}
	if _, e := BillingPreset("superwagie", "unknown"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unknown preset: %v", e)
	}
}

func TestBillingProfileVersionedOperatorUpdatesAndConcurrentConflict(t *testing.T) {
	s := profileService(t)
	ctx := context.Background()
	legacy, e := s.Profile(ctx, "superwagie")
	if e != nil || legacy.Version != 0 || !legacy.IncludedCredits.Enabled || legacy.Usage.DefaultFundingMode != "credits" {
		t.Fatalf("legacy %+v %v", legacy, e)
	}
	p, _ := BillingPreset("superwagie", "prepaid")
	if _, e = s.SaveProfile(ctx, "stranger", "superwagie", p); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("nonoperator update %v", e)
	}
	saved, e := s.SaveProfile(ctx, "operator", "superwagie", p)
	if e != nil || saved.Version != 1 {
		t.Fatalf("save %+v %v", saved, e)
	}
	if _, e = s.SaveProfile(ctx, "operator", "superwagie", p); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale write %v", e)
	}
	p = saved
	p.Subscription.Enabled = true
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.SaveProfile(ctx, "operator", "superwagie", p); errs <- e }()
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("writes ok=%d conflict=%d", ok, conflict)
	}
	var count int
	var actor string
	if e = s.pool.QueryRow(ctx, `SELECT count(*),min(actor_id) FROM product_billing_profiles WHERE product_id='superwagie'`).Scan(&count, &actor); e != nil {
		t.Fatal(e)
	}
	if count != 2 || actor != "operator" {
		t.Fatalf("version history count=%d actor=%s", count, actor)
	}
	if _, e = s.Profile(ctx, "missing"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("missing product %v", e)
	}
	if _, e = s.SaveProfile(ctx, "operator", "agentwiki", p); !errors.Is(e, ErrInvalid) {
		t.Fatalf("mismatched product %v", e)
	}
}

func TestConfiguredBillingProfilesAreMonotonicAndAtomic(t *testing.T) {
	s := profileService(t)
	ctx := context.Background()
	p, _ := BillingPreset("llmrouter", "prepaid")
	p.Version = 3
	if e := s.ApplyConfiguredProfiles(ctx, []BillingProfile{p}); e != nil {
		t.Fatal(e)
	}
	if e := s.ApplyConfiguredProfiles(ctx, []BillingProfile{p}); e != nil {
		t.Fatalf("idempotent deployment: %v", e)
	}
	stale := p
	stale.Version = 2
	if e := s.ApplyConfiguredProfiles(ctx, []BillingProfile{stale}); !errors.Is(e, ErrConflict) {
		t.Fatalf("rollback accepted: %v", e)
	}
	altered := p
	altered.Subscription.Enabled = true
	if e := s.ApplyConfiguredProfiles(ctx, []BillingProfile{altered}); !errors.Is(e, ErrConflict) {
		t.Fatalf("same version overwrite accepted: %v", e)
	}
	good, _ := BillingPreset("agentwiki", "subscription")
	good.Version = 1
	if e := s.ApplyConfiguredProfiles(ctx, []BillingProfile{good, stale}); !errors.Is(e, ErrConflict) {
		t.Fatalf("mixed deployment: %v", e)
	}
	got, e := s.Profile(ctx, "agentwiki")
	if e != nil || got.Version != 0 {
		t.Fatalf("partial deployment: %+v %v", got, e)
	}
	if e := s.ApplyConfiguredProfiles(ctx, []BillingProfile{good, good}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("duplicate products accepted: %v", e)
	}
}

func TestPublishPureSubscriptionRevalidatesHistoricalDraftRefundPolicy(t *testing.T) {
	s := profileService(t)
	ctx := context.Background()
	p, _ := BillingPreset("superwagie", "subscription")
	if _, e := s.SaveProfile(ctx, "operator", p.ProductID, p); e != nil {
		t.Fatal(e)
	}
	o := Offer{ID: "legacy-pure-draft", ProductID: "superwagie", Name: "Legacy draft", AccountKind: "both", Kind: "membership", Period: "month", Price: "1000000", MonthlyCredit: "0", RefundRule: "unused_only_v1", Status: "draft"}
	if _, e := s.pool.Exec(ctx, `INSERT INTO billing_offers(id,product_id,status,snapshot) VALUES($1,$2,'draft',$3)`, o.ID, o.ProductID, encode(o)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.PublishOffer(ctx, "operator", o.ID); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unsafe historical draft published: %v", e)
	}
}
