package wallet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/testutil"
)

type testLedger struct {
	mu       sync.Mutex
	tx       map[string]ledger.Transaction
	balance  map[string]int64
	loseNext bool
}

func (l *testLedger) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.tx[c.Reference]; ok {
		return t, nil
	}
	for _, p := range c.Postings {
		if p.Source != "world" && l.balance[p.Source] < p.Amount {
			return ledger.Transaction{}, ledger.ErrInsufficientFunds
		}
	}
	for _, p := range c.Postings {
		if p.Source != "world" {
			l.balance[p.Source] -= p.Amount
		}
		l.balance[p.Destination] += p.Amount
	}
	t := ledger.Transaction{ID: fmt.Sprint(len(l.tx)), Reference: c.Reference, Postings: c.Postings}
	l.tx[c.Reference] = t
	if l.loseNext {
		l.loseNext = false
		return ledger.Transaction{}, ledger.ErrUnknown
	}
	return t, nil
}
func (l *testLedger) Find(ctx context.Context, r string) (*ledger.Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.tx[r]; ok {
		return &t, nil
	}
	return nil, nil
}
func (l *testLedger) Balance(ctx context.Context, a, asset string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.balance[a], nil
}
func fixture(t *testing.T) (*Service, *testLedger, accounts.User, accounts.Account) {
	t.Helper()
	p := testutil.Database(t)
	ctx := context.Background()
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice", Name: "Alice"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	l := &testLedger{tx: map[string]ledger.Transaction{}, balance: map[string]int64{}}
	return New(p, a, l), l, u, as[0]
}
func deposit(t *testing.T, s *Service, u accounts.User, a accounts.Account) Operation {
	t.Helper()
	ctx := context.Background()
	o, e := s.CreateRecharge(ctx, u.ID, a.ID, "", "100000000", "recharge1", "sandbox")
	if e != nil {
		t.Fatal(e)
	}
	r := Receipt{Merchant: "sandbox", TransactionID: "pay1", OrderID: o.ID, Amount: o.Amount, Currency: "CNY", Status: "SUCCEEDED"}
	op, e := s.Receive(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Process(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	return op
}
func TestDuplicateReceiptAndWrongAmount(t *testing.T) {
	s, l, u, a := fixture(t)
	ctx := context.Background()
	o, e := s.CreateRecharge(ctx, u.ID, a.ID, "", "100000000", "r1", "sandbox")
	if e != nil {
		t.Fatal(e)
	}
	r := Receipt{Merchant: "sandbox", TransactionID: "pay1", OrderID: o.ID, Amount: "200000000", Currency: "CNY", Status: "SUCCEEDED"}
	if _, e = s.Receive(ctx, r); !errors.Is(e, ErrInvalid) {
		t.Fatalf("bad amount: %v", e)
	}
	r.Amount = o.Amount
	op, e := s.Receive(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Receive(ctx, r)
	if e != nil || op.ID != again.ID {
		t.Fatalf("duplicate %v %v", again, e)
	}
	s.Process(ctx, op.ID)
	s.Process(ctx, op.ID)
	if len(l.tx) != 1 {
		t.Fatalf("postings=%d", len(l.tx))
	}
	o2, e := s.CreateRecharge(ctx, u.ID, a.ID, "", "100000000", "r2", "sandbox")
	if e != nil {
		t.Fatal(e)
	}
	r.OrderID = o2.ID
	if _, e = s.Receive(ctx, r); !errors.Is(e, ErrConflict) {
		t.Fatalf("reused payment: %v", e)
	}
}
func TestIsolationIdempotencyAndConcurrentTransfers(t *testing.T) {
	s, l, u, a := fixture(t)
	ctx := context.Background()
	deposit(t, s, u, a)
	if _, e := s.Transfer(ctx, "stranger", a.ID, "superwagie", "80000000", "x"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("isolation: %v", e)
	}
	x, e := s.Transfer(ctx, u.ID, a.ID, "superwagie", "80000000", "x")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Transfer(ctx, u.ID, a.ID, "superwagie", "70000000", "x"); !errors.Is(e, ErrConflict) {
		t.Fatalf("idempotency: %v", e)
	}
	y, e := s.Transfer(ctx, u.ID, a.ID, "agentwiki", "80000000", "y")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for _, id := range []string{x.ID, y.ID} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); s.Process(ctx, id) }(id)
	}
	wg.Wait()
	ox, e := s.GetOperation(ctx, u.ID, a.ID, x.ID)
	if e != nil {
		t.Fatal(e)
	}
	oy, e := s.GetOperation(ctx, u.ID, a.ID, y.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !((ox.Status == "APPLIED" && oy.Status == "REJECTED") || (oy.Status == "APPLIED" && ox.Status == "REJECTED")) {
		t.Fatalf("states: %s %s", ox.Status, oy.Status)
	}
	if len(l.tx) != 2 {
		t.Fatalf("transactions %d", len(l.tx))
	}
	if _, e = s.GetOperation(ctx, "stranger", a.ID, x.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("read leak: %v", e)
	}
}
func TestCommittedResponseLostRecoveredAfterRestart(t *testing.T) {
	s, l, u, a := fixture(t)
	ctx := context.Background()
	l.loseNext = true
	o, e := s.CreateRecharge(ctx, u.ID, a.ID, "", "100000000", "r", "sandbox")
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.Receive(ctx, Receipt{Merchant: "sandbox", TransactionID: "pay", OrderID: o.ID, Amount: o.Amount, Currency: "CNY", Status: "SUCCEEDED"})
	if e != nil {
		t.Fatal(e)
	}
	_ = s.Process(ctx, op.ID)
	s2 := New(s.pool, s.accounts, l)
	if e = s2.Process(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	got, e := s2.GetOperation(ctx, u.ID, a.ID, op.ID)
	if e != nil || got.Status != "APPLIED" || len(l.tx) != 1 {
		t.Fatalf("recovery: %+v %v transactions=%d", got, e, len(l.tx))
	}
}
