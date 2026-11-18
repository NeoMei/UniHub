package cashtopup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/url"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

type fakeProvider struct {
	id        string
	merchant  string
	createErr error
	requests  map[string]payments.Request
	sessions  map[string]payments.Session
	paid      map[string]payments.Result
}

func newFakeProvider(id string) *fakeProvider {
	return &fakeProvider{id: id, merchant: "m-" + id, requests: map[string]payments.Request{}, sessions: map[string]payments.Session{}, paid: map[string]payments.Result{}}
}
func (p *fakeProvider) ID() string         { return p.id }
func (p *fakeProvider) MerchantID() string { return p.merchant }
func (p *fakeProvider) Create(_ context.Context, r payments.Request) (payments.Session, error) {
	if p.createErr != nil {
		return payments.Session{}, p.createErr
	}
	s := payments.Session{Kind: "redirect", URL: "https://pay.example/" + r.OrderID, ExpiresAt: r.ExpiresAt}
	p.sessions[r.OrderID] = s
	p.requests[r.OrderID] = r
	return s, nil
}
func (p *fakeProvider) Query(_ context.Context, id string) (payments.Result, error) {
	if r, ok := p.paid[id]; ok {
		return r, nil
	}
	return payments.Result{OrderID: id, Status: "CREATED"}, nil
}
func (p *fakeProvider) Close(context.Context, string) error { return nil }
func (p *fakeProvider) VerifyNotification(_ http.Header, body []byte) (payments.Result, error) {
	var r payments.Result
	if e := jsonUnmarshal(body, &r); e != nil {
		return r, e
	}
	return r, nil
}
func (p *fakeProvider) Refund(context.Context, payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{}, nil
}
func (p *fakeProvider) QueryRefund(context.Context, payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{}, nil
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

type testLedger struct {
	tx      map[string]ledger.Transaction
	balance map[string]int64
}

func (l *testLedger) Post(_ context.Context, c ledger.Command) (ledger.Transaction, error) {
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
	t := ledger.Transaction{ID: "tx" + c.Reference, Reference: c.Reference, Postings: c.Postings}
	l.tx[c.Reference] = t
	return t, nil
}
func (l *testLedger) Find(_ context.Context, r string) (*ledger.Transaction, error) {
	if t, ok := l.tx[r]; ok {
		return &t, nil
	}
	return nil, nil
}
func (l *testLedger) Balance(_ context.Context, a, asset string) (int64, error) {
	return l.balance[a], nil
}

type fixture struct {
	svc      *Service
	wallet   *wallet.Service
	ledger   *testLedger
	user     accounts.User
	account  accounts.Account
	provider *fakeProvider
}

func setup(t *testing.T) *fixture {
	t.Helper()
	p := testutil.Database(t)
	ctx := context.Background()
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob", Name: "Bob"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	l := &testLedger{tx: map[string]ledger.Transaction{}, balance: map[string]int64{}}
	w := wallet.New(p, a, l)
	ct := New(p, a, w)
	prov := newFakeProvider("wechat")
	ct.ConfigureProviders([]payments.Provider{prov})
	return &fixture{svc: ct, wallet: w, ledger: l, user: u, account: as[0], provider: prov}
}

func TestCreateCheckoutAndCredit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	x, e := f.svc.Create(ctx, f.user.ID, f.account.ID, Request{Provider: "wechat", ProductID: "llmrouter", Amount: "1000000", Key: "key-1"})
	if e != nil {
		t.Fatal(e)
	}
	if x.ID == "" || x.Session == nil || x.PaymentStatus != "CREATED" {
		t.Fatalf("bad checkout: %+v", x)
	}
	// Idempotent replay by key.
	x2, e := f.svc.Create(ctx, f.user.ID, f.account.ID, Request{Provider: "wechat", ProductID: "llmrouter", Amount: "1000000", Key: "key-1"})
	if e != nil || x2.ID != x.ID {
		t.Fatalf("replay %+v %v", x2, e)
	}

	// Verified notification credits the wallet.
	r := payments.Result{OrderID: x.ID, TransactionID: "txn-1", Amount: "1000000", Currency: "CNY", Status: "PAID", PaidAt: time.Now()}
	body, _ := json.Marshal(r)
	if e = f.svc.Notify(ctx, "wechat", http.Header{}, body); e != nil {
		t.Fatal(e)
	}
	cur, e := f.svc.Get(ctx, f.user.ID, f.account.ID, x.ID)
	if e != nil {
		t.Fatal(e)
	}
	if cur.PaymentStatus != "PAID" || cur.OperationID == "" {
		t.Fatalf("not settled: %+v", cur)
	}
	// Worker applies world->cash posting.
	if e = f.wallet.Process(ctx, cur.OperationID); e != nil {
		t.Fatal(e)
	}
	bal, e := f.ledger.Balance(ctx, wallet.Address(f.account.ID, "llmrouter"), wallet.Asset)
	if e != nil || bal != 1000000 {
		t.Fatalf("balance %d %v", bal, e)
	}

	// Duplicate notification must not double-credit.
	if e = f.svc.Notify(ctx, "wechat", http.Header{}, body); e != nil {
		t.Fatal(e)
	}
	bal2, _ := f.ledger.Balance(ctx, wallet.Address(f.account.ID, "llmrouter"), wallet.Asset)
	if bal2 != bal {
		t.Fatalf("double credit %d != %d", bal2, bal)
	}
}

func TestWrongAmountRejected(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	x, e := f.svc.Create(ctx, f.user.ID, f.account.ID, Request{Provider: "wechat", ProductID: "llmrouter", Amount: "1000000", Key: "key-2"})
	if e != nil {
		t.Fatal(e)
	}
	r := payments.Result{OrderID: x.ID, TransactionID: "txn-2", Amount: "2000000", Currency: "CNY", Status: "PAID"}
	body, _ := json.Marshal(r)
	if e = f.svc.Notify(ctx, "wechat", http.Header{}, body); !errors.Is(e, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", e)
	}
}

func TestInvalidInput(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, e := f.svc.Create(ctx, f.user.ID, f.account.ID, Request{Provider: "wechat", Amount: "123", Key: "k"}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("expected invalid fractional amount, got %v", e)
	}
	if _, e := f.svc.Create(ctx, f.user.ID, f.account.ID, Request{Provider: "bogus", Amount: "1000000", Key: "k"}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("expected invalid provider, got %v", e)
	}
}

func TestCheckoutReturnBindsOrderAndIdempotency(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	req := Request{Provider: "wechat", ProductID: "llmrouter", Amount: "20000000", Key: "return-1", ReturnURL: "https://llmrouter.quukk.com/wallet"}
	x, err := f.svc.Create(ctx, f.user.ID, f.account.ID, req)
	require.NoError(t, err)
	u, err := url.Parse(f.provider.requests[x.ID].ReturnURL)
	require.NoError(t, err)
	assert.Equal(t, "https://llmrouter.quukk.com", u.Scheme+"://"+u.Host)
	assert.Equal(t, "/wallet", u.Path)
	assert.Equal(t, x.ID, u.Query().Get("cash_checkout"))
	replay, err := f.svc.Create(ctx, f.user.ID, f.account.ID, req)
	require.NoError(t, err)
	assert.Equal(t, x.ID, replay.ID)
	req.ReturnURL = "https://other.example/wallet"
	_, err = f.svc.Create(ctx, f.user.ID, f.account.ID, req)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestNativeCheckoutCreditsUnifiedWalletExactlyOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	req := Request{Provider: "wechat", ProductID: "llmrouter", Amount: "20000000", Key: "native-1", UnifiedWallet: true}
	x, err := f.svc.Create(ctx, f.user.ID, f.account.ID, req)
	require.NoError(t, err)
	assert.Equal(t, "llmrouter", x.ProductID)
	assert.Equal(t, "unified", x.WalletTarget)

	r := payments.Result{OrderID: x.ID, TransactionID: "native-transaction", Amount: req.Amount, Currency: "CNY", Status: "PAID", PaidAt: time.Now()}
	body, err := json.Marshal(r)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, f.svc.Notify(ctx, "wechat", http.Header{}, body))
		cur, err := f.svc.Get(ctx, f.user.ID, f.account.ID, x.ID)
		require.NoError(t, err)
		require.NotEmpty(t, cur.OperationID)
		require.NoError(t, f.wallet.Process(ctx, cur.OperationID))
	}
	assert.Equal(t, int64(20000000), f.ledger.balance[wallet.Address(f.account.ID, "")])
	assert.Zero(t, f.ledger.balance[wallet.Address(f.account.ID, "llmrouter")])
	cur, err := f.svc.Get(ctx, f.user.ID, f.account.ID, x.ID)
	require.NoError(t, err)
	assert.Equal(t, "APPLIED", cur.CreditStatus)
	closed, err := f.svc.recordClosed(ctx, f.account.ID, x.ID)
	require.NoError(t, err)
	assert.Equal(t, "PAID", closed.PaymentStatus)
	assert.Equal(t, cur.OperationID, closed.OperationID)
	orders, err := f.svc.List(ctx, f.user.ID, f.account.ID, "llmrouter")
	require.NoError(t, err)
	require.Len(t, orders, 1)
	assert.Equal(t, x.ID, orders[0].ID)
	assert.Equal(t, "APPLIED", orders[0].CreditStatus)
	assert.Nil(t, orders[0].Session)
	orders, err = f.svc.List(ctx, f.user.ID, f.account.ID, "superwagie")
	require.NoError(t, err)
	assert.Empty(t, orders)
}
