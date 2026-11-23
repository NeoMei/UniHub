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
	id            string
	merchant      string
	sessionKind   string
	sessionOffset time.Duration
	createErr     error
	closeCalls    int
	closeErr      error
	queryErr      error
	requests      map[string]payments.Request
	sessions      map[string]payments.Session
	paid          map[string]payments.Result
}

func newFakeProvider(id string) *fakeProvider {
	return &fakeProvider{id: id, merchant: "m-" + id, requests: map[string]payments.Request{}, sessions: map[string]payments.Session{}, paid: map[string]payments.Result{}}
}
func (p *fakeProvider) ID() string         { return p.id }
func (p *fakeProvider) MerchantID() string { return p.merchant }
func (p *fakeProvider) Create(_ context.Context, r payments.Request) (payments.Session, error) {
	p.requests[r.OrderID] = r
	if p.createErr != nil {
		return payments.Session{}, p.createErr
	}
	kind := p.sessionKind
	if kind == "" {
		kind = "redirect"
	}
	s := payments.Session{Kind: kind, URL: "https://pay.example/" + r.OrderID, ExpiresAt: r.ExpiresAt.Add(p.sessionOffset)}
	p.sessions[r.OrderID] = s
	return s, nil
}
func (p *fakeProvider) Query(_ context.Context, id string) (payments.Result, error) {
	if p.queryErr != nil {
		return payments.Result{}, p.queryErr
	}
	if r, ok := p.paid[id]; ok {
		return r, nil
	}
	return payments.Result{OrderID: id, Status: "CREATED"}, nil
}
func (p *fakeProvider) Close(context.Context, string) error {
	p.closeCalls++
	return p.closeErr
}
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

func TestDelayedCashReceiptUsesVerifiedPaymentTime(t *testing.T) {
	for _, tt := range []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"paid before expiration", 2 * time.Minute, "PAID"},
		{"paid after expiration", 16 * time.Minute, "REVIEW"},
		{"payment time missing", 0, "REVIEW"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			f.svc.now = func() time.Time { return now }
			x, err := f.svc.Create(ctx, f.user.ID, f.account.ID, Request{
				Provider: "wechat", ProductID: "llmrouter", Amount: "20000000", Key: "verify-paid-time", UnifiedWallet: true,
			})
			require.NoError(t, err)
			result := payments.Result{OrderID: x.ID, TransactionID: "verified-receipt", Status: "PAID", Amount: x.Amount, Currency: "CNY"}
			if tt.offset > 0 {
				result.PaidAt = x.CreatedAt.Add(tt.offset)
			}
			now = x.ExpiresAt.Add(5 * time.Minute)
			body, err := json.Marshal(result)
			require.NoError(t, err)
			for range 2 {
				require.NoError(t, f.svc.Notify(ctx, "wechat", http.Header{}, body))
				require.NoError(t, f.wallet.Recover(ctx))
			}
			view, err := f.svc.Reconcile(ctx, f.user.ID, f.account.ID, x.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, view.PaymentStatus)
			balance := f.ledger.balance[wallet.Address(f.account.ID, "")]
			if tt.want == "PAID" {
				assert.Equal(t, "APPLIED", view.CreditStatus)
				assert.Equal(t, int64(20000000), balance)
			} else {
				assert.Empty(t, view.OperationID)
				assert.Zero(t, balance)
			}
		})
	}
}

func TestCashCreateRetriesSameMerchantOrderAfterLostResponse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.provider.createErr = payments.ErrUnavailable
	req := Request{Provider: "wechat", ProductID: "llmrouter", Amount: "20000000", Key: "unknown-result", UnifiedWallet: true}
	_, err := f.svc.Create(ctx, f.user.ID, f.account.ID, req)
	require.ErrorIs(t, err, payments.ErrUnavailable)
	require.Len(t, f.provider.requests, 1)
	f.provider.createErr = nil
	x, err := f.svc.Create(ctx, f.user.ID, f.account.ID, req)
	require.NoError(t, err)
	require.NotNil(t, x.Session)
	require.Len(t, f.provider.requests, 1, "a gateway timeout must not allocate another merchant order ID")
	require.Contains(t, f.provider.requests, x.ID)
	assert.Equal(t, x.ExpiresAt, f.provider.requests[x.ID].ExpiresAt)
}

func TestCashRecoveryQueriesBeforeClosingAndCreditsMissingCallback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	f.svc.now = func() time.Time { return now }
	req := Request{Provider: "wechat", ProductID: "llmrouter", Amount: "20000000", UnifiedWallet: true}
	checkouts := make(map[string]Checkout)
	for _, status := range []string{"PAID", "PENDING", "NOT_FOUND"} {
		req.Key = "recover-" + status
		checkout, err := f.svc.Create(ctx, f.user.ID, f.account.ID, req)
		require.NoError(t, err)
		checkouts[status] = checkout
		f.provider.paid[checkout.ID] = payments.Result{
			OrderID: checkout.ID, TransactionID: checkout.ID, Status: status, Amount: checkout.Amount, Currency: "CNY",
			PaidAt: checkout.CreatedAt.Add(time.Minute),
		}
	}
	now = now.Add(20 * time.Minute)
	f.provider.queryErr = payments.ErrUnavailable
	require.Error(t, f.svc.Recover(ctx))
	assert.Zero(t, f.provider.closeCalls)
	f.provider.queryErr = nil
	now = now.Add(10 * time.Minute)
	require.NoError(t, f.svc.Recover(ctx))
	require.NoError(t, f.wallet.Recover(ctx))
	paid, err := f.svc.Get(ctx, f.user.ID, f.account.ID, checkouts["PAID"].ID)
	require.NoError(t, err)
	assert.Equal(t, "PAID", paid.PaymentStatus)
	assert.Equal(t, "APPLIED", paid.CreditStatus)
	assert.Equal(t, int64(20000000), f.ledger.balance[wallet.Address(f.account.ID, "")])
	closed, err := f.svc.Get(ctx, f.user.ID, f.account.ID, checkouts["PENDING"].ID)
	require.NoError(t, err)
	assert.Equal(t, "CLOSED", closed.PaymentStatus)
	unconfirmed, err := f.svc.Get(ctx, f.user.ID, f.account.ID, checkouts["NOT_FOUND"].ID)
	require.NoError(t, err)
	assert.Equal(t, "CREATED", unconfirmed.PaymentStatus)
	assert.Equal(t, 1, f.provider.closeCalls)
}

func TestExpiredAlipayCashRedirectChecksMerchantBeforeClosing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alipay := newFakeProvider("alipay")
	f.svc.ConfigureProviders([]payments.Provider{alipay})
	now := time.Now().UTC()
	f.svc.now = func() time.Time { return now }
	req := Request{Provider: "alipay", ProductID: "llmrouter", Amount: "20000000", Key: "expired-alipay-page", UnifiedWallet: true}
	order, err := f.svc.Create(ctx, f.user.ID, f.account.ID, req)
	require.NoError(t, err)
	alipay.paid[order.ID] = payments.Result{OrderID: order.ID, Status: "NOT_FOUND"}
	require.NoError(t, f.svc.Recover(ctx))
	active, err := f.svc.Get(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	assert.Equal(t, "CREATED", active.PaymentStatus)
	now = order.ExpiresAt.Add(6 * time.Minute)
	alipay.queryErr = payments.ErrVerification
	require.Error(t, f.svc.Recover(ctx))
	active, err = f.svc.Get(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	assert.Equal(t, "CREATED", active.PaymentStatus)
	now = now.Add(6 * time.Minute)
	alipay.queryErr = nil
	require.NoError(t, f.svc.Recover(ctx))
	closed, err := f.svc.Get(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	assert.Equal(t, "CLOSED", closed.PaymentStatus)
	assert.Nil(t, closed.Session)
	assert.Zero(t, f.ledger.balance[wallet.Address(f.account.ID, "")])
}

func TestExpiredAlipayCashQRClosesAfterVerifiedNotFound(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alipay := newFakeProvider("alipay")
	alipay.sessionKind = "qr"
	f.svc.ConfigureProviders([]payments.Provider{alipay})
	now := time.Now().UTC()
	f.svc.now = func() time.Time { return now }
	order, err := f.svc.Create(ctx, f.user.ID, f.account.ID, Request{
		Provider: "alipay", ProductID: "llmrouter", Amount: "20000000", Key: "expired-alipay-qr", UnifiedWallet: true,
	})
	require.NoError(t, err)
	alipay.paid[order.ID] = payments.Result{OrderID: order.ID, Status: "NOT_FOUND"}
	now = order.ExpiresAt.Add(time.Minute)
	closed, err := f.svc.Close(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	assert.Equal(t, "CLOSED", closed.PaymentStatus)
	assert.Zero(t, alipay.closeCalls)
}

func TestAlipayCashQRCloseRemainsPendingUntilMerchantEvidence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alipay := newFakeProvider("alipay")
	alipay.sessionKind = "qr"
	alipay.sessionOffset = -30 * time.Second
	alipay.closeErr = payments.ErrUnavailable
	f.svc.ConfigureProviders([]payments.Provider{alipay})
	now := time.Now().UTC()
	f.svc.now = func() time.Time { return now }
	request := Request{Provider: "alipay", ProductID: "superwagie", UnifiedWallet: true,
		Amount: "1000000", Key: "cancel-alipay-cash-qr"}
	order, err := f.svc.Create(ctx, f.user.ID, f.account.ID, request)
	require.NoError(t, err)
	require.NotNil(t, order.Session)
	alipay.paid[order.ID] = payments.Result{OrderID: order.ID, Status: "NOT_FOUND"}

	pending, err := f.svc.Close(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	assert.True(t, pending.ClosePending)
	assert.Equal(t, "CREATED", pending.PaymentStatus)
	assert.Nil(t, pending.Session)
	assert.Equal(t, 1, alipay.closeCalls)
	listed, err := f.svc.List(ctx, f.user.ID, f.account.ID, "superwagie")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.True(t, listed[0].ClosePending)
	replayed, err := f.svc.Create(ctx, f.user.ID, f.account.ID, request)
	require.NoError(t, err)
	assert.Equal(t, order.ID, replayed.ID)
	assert.True(t, replayed.ClosePending)
	assert.Nil(t, replayed.Session)

	now = order.ExpiresAt.Add(time.Second)
	require.NoError(t, f.svc.Recover(ctx))
	closed, err := f.svc.Get(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	assert.Equal(t, "CLOSED", closed.PaymentStatus)
	assert.False(t, closed.ClosePending)
	assert.Nil(t, closed.Session)
	assert.Equal(t, 1, alipay.closeCalls)
	assert.Zero(t, f.ledger.balance[wallet.Address(f.account.ID, "")])
}

func TestAlipayCashQRClosePreservesPaymentConfirmedDuringCancellation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alipay := newFakeProvider("alipay")
	alipay.sessionKind = "qr"
	alipay.closeErr = payments.ErrUnavailable
	f.svc.ConfigureProviders([]payments.Provider{alipay})
	order, err := f.svc.Create(ctx, f.user.ID, f.account.ID, Request{
		Provider: "alipay", ProductID: "superwagie", UnifiedWallet: true, Amount: "1000000", Key: "paid-during-close",
	})
	require.NoError(t, err)
	pending, err := f.svc.Close(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	require.True(t, pending.ClosePending)

	alipay.paid[order.ID] = payments.Result{OrderID: order.ID, TransactionID: "alipay-original", Amount: order.Amount,
		Currency: "CNY", Status: "PAID", PaidAt: order.CreatedAt.Add(time.Second)}
	paid, err := f.svc.Reconcile(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	assert.Equal(t, "PAID", paid.PaymentStatus)
	assert.False(t, paid.ClosePending)
	assert.Nil(t, paid.Session)
	require.NotEmpty(t, paid.OperationID)
	require.NoError(t, f.wallet.Process(ctx, paid.OperationID))
	paid, err = f.svc.Close(ctx, f.user.ID, f.account.ID, order.ID)
	require.NoError(t, err)
	assert.Equal(t, "PAID", paid.PaymentStatus)
	require.NoError(t, f.wallet.Process(ctx, paid.OperationID))
	assert.EqualValues(t, 1_000_000, f.ledger.balance[wallet.Address(f.account.ID, "")])
}
