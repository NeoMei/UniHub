package wallet

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/testutil"
)

func TestRealFormanceRechargeTransferRecovery(t *testing.T) {
	base := os.Getenv("UNIHUB_TEST_LEDGER_URL")
	if base == "" {
		t.Skip("UNIHUB_TEST_LEDGER_URL unset")
	}
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "real-ledger", Name: "Alice"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	l := ledger.NewFormance(base, fmt.Sprintf("test-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if e = l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	s := New(p, a, l)
	deposit(t, s, u, as[0])
	x, e := s.Transfer(ctx, u.ID, as[0].ID, "superwagie", "80000000", "x")
	if e != nil {
		t.Fatal(e)
	}
	y, e := s.Transfer(ctx, u.ID, as[0].ID, "agentwiki", "80000000", "y")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for _, id := range []string{x.ID, y.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if e := s.Process(ctx, id); e != nil {
				t.Error(e)
			}
		}(id)
	}
	wg.Wait()
	for _, id := range []string{x.ID, y.ID} {
		if e = s.Process(ctx, id); e != nil {
			t.Fatal(e)
		}
	}
	ox, e := s.GetOperation(ctx, u.ID, as[0].ID, x.ID)
	if e != nil {
		t.Fatal(e)
	}
	oy, e := s.GetOperation(ctx, u.ID, as[0].ID, y.ID)
	if e != nil {
		t.Fatal(e)
	}
	if ox.Status != "APPLIED" || oy.Status != "REJECTED" {
		t.Fatalf("states %+v %+v", ox, oy)
	}
	balance, e := l.Balance(ctx, Address(as[0].ID, ""), Asset)
	if e != nil || balance != 20000000 {
		t.Fatalf("balance %d %v", balance, e)
	}
	s2 := New(p, a, l)
	if e = s2.Process(ctx, x.ID); e != nil {
		t.Fatal(e)
	}
	balance, e = l.Balance(ctx, Address(as[0].ID, "superwagie"), Asset)
	if e != nil || balance != 80000000 {
		t.Fatalf("product balance %d %v", balance, e)
	}
}
