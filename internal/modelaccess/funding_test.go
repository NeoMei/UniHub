package modelaccess

import (
	"context"
	"errors"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

type fundingCatalog struct {
	catalog
	mode, asset string
	version     int64
	denied      bool
	rates       []billing.Rate
}

func (c *fundingCatalog) AuthorizeUsage(context.Context, string, string, string, string) (billing.UsageAuthorization, error) {
	if c.denied {
		return billing.UsageAuthorization{}, accounts.ErrForbidden
	}
	return billing.UsageAuthorization{FundingMode: c.mode, Asset: c.asset, ProfileVersion: c.version}, nil
}
func (c *fundingCatalog) Rates(context.Context) ([]billing.Rate, error) { return c.rates, nil }
func TestPrepaidCredentialBindsFundingAssetAndOriginalProfileVersion(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://funding", Subject: "prepaid"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	c := &fundingCatalog{mode: billing.FundingPrepaidCash, asset: billing.CashAsset, version: 1, rates: []billing.Rate{{Model: "same-model", Asset: billing.CreditAsset, Status: "published"}, {Model: "same-model", Asset: billing.CashAsset, Status: "published"}}}
	s := New(p, a, c)
	start := time.Now().UTC()
	g, e := s.Issue(ctx, u.ID, as[0].ID, Request{OriginProductID: "superwagie", Models: []string{"same-model"}, TaskID: "prepaid", FundingMode: billing.FundingPrepaidCash})
	if e != nil {
		t.Fatal(e)
	}
	if g.FundingMode != billing.FundingPrepaidCash || g.Asset != billing.CashAsset || g.BillingProfileVersion != 1 {
		t.Fatalf("grant %+v", g.Claims)
	}
	if g.ExpiresAt.Before(start.Add(9*time.Minute)) || g.ExpiresAt.After(time.Now().Add(10*time.Minute)) {
		t.Fatalf("unbounded expiry %s", g.ExpiresAt)
	}
	c.version = 2
	got, e := s.Verify(ctx, g.Token, "same-model")
	if e != nil || got.BillingProfileVersion != 1 || got.Asset != billing.CashAsset {
		t.Fatalf("verify rewrote original %+v %v", got, e)
	}
	c.denied = true
	if _, e = s.Verify(ctx, g.Token, "same-model"); !errors.Is(e, ErrCredential) {
		t.Fatalf("revoked policy %v", e)
	}
}
func TestCredentialRequiresPublishedRateForItsAsset(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://funding", Subject: "asset"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	c := &fundingCatalog{mode: billing.FundingPrepaidCash, asset: billing.CashAsset, version: 1, rates: []billing.Rate{{Model: "credit-only", Status: "published"}}}
	s := New(p, a, c)
	if _, e = s.Issue(ctx, u.ID, as[0].ID, Request{OriginProductID: "superwagie", Models: []string{"credit-only"}, TaskID: "wrong"}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("different denomination accepted %v", e)
	}
	// Legacy eligibility implementations cannot silently provide prepaid capabilities.
	legacy := New(p, a, &catalog{time.Now().Add(time.Hour)})
	if _, e = legacy.Issue(ctx, u.ID, as[0].ID, Request{OriginProductID: "superwagie", Models: []string{"test-model"}, TaskID: "legacy-cash", FundingMode: billing.FundingPrepaidCash}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("legacy granted cash %v", e)
	}
}

func TestRealBillingPolicyIssuesPrepaidCredentialWithoutSubscription(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://funding", Subject: "real-policy"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, nil)
	b.ConfigureOperators([]string{u.ID})
	profile, e := billing.BillingPreset("superwagie", "prepaid")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.SaveProfile(ctx, u.ID, profile.ProductID, profile); e != nil {
		t.Fatal(e)
	}
	rate := billing.Rate{ID: "real-cash", Model: "model", Asset: billing.CashAsset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "1", Denominator: "1"}, Output: billing.Rational{Numerator: "0", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}
	if _, e = b.PublishRate(ctx, u.ID, rate); e != nil {
		t.Fatal(e)
	}
	s := New(p, a, b)
	g, e := s.Issue(ctx, u.ID, as[0].ID, Request{OriginProductID: "superwagie", Models: []string{"model"}, TaskID: "real"})
	if e != nil {
		t.Fatal(e)
	}
	if g.FundingMode != billing.FundingPrepaidCash || g.Asset != billing.CashAsset || g.BillingProfileVersion != 1 {
		t.Fatalf("grant %+v", g.Claims)
	}
	profile, e = b.Profile(ctx, "superwagie")
	if e != nil {
		t.Fatal(e)
	}
	profile.Usage = billing.UsagePolicy{}
	if _, e = b.SaveProfile(ctx, u.ID, profile.ProductID, profile); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, g.Token, "model"); !errors.Is(e, ErrCredential) {
		t.Fatalf("disabled usage still verified %v", e)
	}
}
