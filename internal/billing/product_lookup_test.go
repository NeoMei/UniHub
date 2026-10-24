package billing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestProductLookupChecksAccountBeforeReadingCommercialObjects(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.auth(t)
	order := f.buy(t, "sandbox-sw-month-1", "buy")
	sub, e := f.s.GetSubscription(ctx, f.actor, f.account, order.SubscriptionID)
	if e != nil || sub.ProductID != "superwagie" {
		t.Fatalf("sub %#v %v", sub, e)
	}
	q, e := f.s.QuoteUpgrade(ctx, f.actor, f.account, sub.ID, "sandbox-sw-month-2")
	if e != nil {
		t.Fatal(e)
	}
	if got, e := f.s.GetQuote(ctx, f.actor, f.account, q.ID); e != nil || got.Offer.ProductID != "superwagie" {
		t.Fatalf("quote %#v %v", got, e)
	}
	if _, e = f.s.GetQuote(ctx, "stranger", f.account, q.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("foreign quote lookup %v", e)
	}
	if _, e = f.s.GetOrder(ctx, f.actor, "another-account", order.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("foreign order lookup %v", e)
	}
	if _, e = f.s.GetQuote(ctx, f.actor, f.account, "missing"); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatalf("missing quote %v", e)
	}
}

func TestProductOrdersFilterBeforeLimit(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	s := New(p, a, nil)
	for i := range 202 {
		product := "agentwiki"
		when := time.Now().UTC()
		if i == 0 {
			product = "superwagie"
			when = when.Add(-time.Hour)
		}
		o := Order{ID: fmt.Sprint(i), AccountID: as[0].ID, ActorID: u.ID, ProductID: product, CreatedAt: when}
		if _, e = p.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,$1,'test',$4)`, o.ID, o.AccountID, u.ID, encode(o)); e != nil {
			t.Fatal(e)
		}
	}
	orders, e := s.ProductOrders(ctx, u.ID, as[0].ID, []string{"superwagie"})
	if e != nil || len(orders) != 1 || orders[0].ProductID != "superwagie" {
		t.Fatalf("orders %#v %v", orders, e)
	}
}
