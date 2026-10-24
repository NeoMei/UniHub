package wallet

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestProductOperationsNeverReturnUnknownUnifiedOrOtherTargets(t *testing.T) {
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
	account := as[0].ID
	s := New(p, a, &testLedger{})
	own, e := s.Transfer(ctx, u.ID, account, "superwagie", "10000", "one")
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.Transfer(ctx, u.ID, account, "agentwiki", "10000", "two")
	if e != nil {
		t.Fatal(e)
	}
	ops, e := s.ProductOperations(ctx, u.ID, account, []string{"superwagie"})
	if e != nil || len(ops) != 1 || ops[0].ID != own.ID {
		t.Fatalf("ops %#v %v", ops, e)
	}
	if _, e = s.ProductOperation(ctx, u.ID, account, other.ID, []string{"superwagie"}); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatalf("foreign detail %v", e)
	}
	if _, e = s.ProductOperation(ctx, "unknown", account, own.ID, []string{"superwagie"}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("foreign actor %v", e)
	}
}

func TestProductBalancesReadOnlyApprovedAddresses(t *testing.T) {
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
	s := New(p, a, &testLedger{})
	balances, e := s.BalancesForProducts(ctx, u.ID, as[0].ID, []string{"superwagie"})
	if e != nil || len(balances) != 2 || balances[0].ProductID != "" || balances[1].ProductID != "superwagie" {
		t.Fatalf("cash %#v %v", balances, e)
	}
}
