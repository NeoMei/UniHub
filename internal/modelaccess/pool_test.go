package modelaccess

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestCredentialIssueUsesExistingTransactionWithSingleConnection(t *testing.T) {
	ctx := context.Background()
	base := testutil.Database(t)
	if e := database.Migrate(ctx, base); e != nil {
		t.Fatal(e)
	}
	cfg := base.Config()
	cfg.MaxConns = 1
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://pool-test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	b := billing.New(p, a, nil)
	b.ConfigureOperators([]string{u.ID})
	profile, _ := billing.BillingPreset("superwagie", "prepaid")
	profile.Version = 1
	if e = b.ApplyConfiguredProfiles(ctx, []billing.BillingProfile{profile}); e != nil {
		t.Fatal(e)
	}
	rate := billing.Rate{ID: "pool-cash", Model: "m", Asset: billing.CashAsset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "1", Denominator: "1"}, Output: billing.Rational{Numerator: "1", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}
	if _, e = b.PublishRate(ctx, u.ID, rate); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	g, e := New(p, a, b).Issue(deadline, u.ID, as[0].ID, Request{OriginProductID: "superwagie", Models: []string{"m"}, TaskID: "one-connection"})
	if e != nil || g.Asset != billing.CashAsset {
		t.Fatalf("single-connection credential issuance %+v %v", g.Claims, e)
	}
}
