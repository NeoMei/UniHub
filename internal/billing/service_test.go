package billing

import (
	"context"
	"errors"
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
	"unihub/internal/wallet"
)

type fixture struct {
	s              *Service
	l              ledger.Ledger
	actor, account string
	offers         []Offer
}

func setup(t *testing.T) fixture {
	t.Helper()
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
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://billing-test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	l := ledger.NewFormance(base, fmt.Sprintf("billing-test-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
	if e = l.Ensure(ctx); e != nil {
		t.Fatal(e)
	}
	s := New(p, a, l)
	s.ConfigureSandbox(true)
	s.ConfigureOperators([]string{u.ID})
	s.ConfigureTrustedServices([]string{"router"})
	s.now = func() time.Time { return dt("2024-01-31T12:00:00Z") }
	offers, e := s.SeedSandbox(ctx)
	if e != nil {
		t.Fatal(e)
	}
	_, e = l.Post(ctx, ledger.Command{Reference: "fund", Postings: []ledger.Posting{{Source: "world", Destination: wallet.Address(as[0].ID, ""), Asset: wallet.Asset, Amount: 1000000000}}})
	if e != nil {
		t.Fatal(e)
	}
	return fixture{s, l, u.ID, as[0].ID, offers}
}
func (f fixture) auth(t *testing.T) {
	t.Helper()
	_, e := f.s.SetAuthorization(context.Background(), f.actor, f.account, Authorization{ProductID: "superwagie", Purpose: "funding", Enabled: true, MaxPerOperation: "1000000000", MaxTotal: "1000000000", ExpiresAt: dt("2026-01-01T00:00:00Z")})
	if e != nil {
		t.Fatal(e)
	}
}
func (f fixture) buy(t *testing.T, id, key string) Order {
	t.Helper()
	o, e := f.s.Purchase(context.Background(), f.actor, f.account, PurchaseRequest{id, key})
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func TestPurchaseReplayUpgradesSharedPoolAndExpiry(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-month-1", "buy")
	if o.PaymentStatus != "PAID" || o.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("order %+v", o)
	}
	re := f.buy(t, "sandbox-sw-month-1", "buy")
	if re.ID != o.ID {
		t.Fatal("replay recharged")
	}
	subs, e := f.s.Subscriptions(ctx, f.actor, f.account)
	if e != nil || len(subs) != 1 {
		t.Fatalf("%+v %v", subs, e)
	}
	for i, id := range []string{"sandbox-sw-month-2", "sandbox-sw-month-3"} {
		q, e := f.s.QuoteUpgrade(ctx, f.actor, f.account, subs[0].ID, id)
		if e != nil {
			t.Fatal(e)
		}
		if q.GrantAmount != "1000000" {
			t.Fatalf("upgrade %d grants %s", i, q.GrantAmount)
		}
		if _, e = f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, fmt.Sprint(i)); e != nil {
			t.Fatal(e)
		}
	}
	bs, e := f.s.Batches(ctx, f.actor, f.account)
	if e != nil {
		t.Fatal(e)
	}
	var orig int64
	for _, b := range bs {
		n, _ := integer(b.Original, false)
		orig += n
	}
	if orig != 3000000 {
		t.Fatalf("issued %d", orig)
	}
	r := Rate{ID: "rate-test", Model: "test", Semantics: "exclusive_v1", Input: Rational{"1", "1"}, Output: Rational{"0", "1"}, CacheRead: Rational{"0", "1"}, CacheWrite: Rational{"0", "1"}, Reasoning: Rational{"0", "1"}}
	if _, e = f.s.PublishRate(ctx, f.actor, r); e != nil {
		t.Fatal(e)
	}
	call, e := f.s.Reserve(ctx, "router", ReserveRequest{"call", f.actor, f.account, "superwagie", "digest", "rate-test", "2000000", ""})
	if e != nil {
		t.Fatal(e)
	}
	if call.Held != "2000000" {
		t.Fatal(call)
	}
	f.s.now = func() time.Time { return dt("2024-03-01T00:00:00Z") }
	u := Usage{Input: "500000", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
	c, e := f.s.Settle(ctx, "router", SettlementRequest{"call", &u, "0"})
	if e != nil || c.Charged != "500000" {
		t.Fatalf("settle %+v %v", c, e)
	}
	if _, e = f.s.Settle(ctx, "router", SettlementRequest{"call", &u, "0"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	bs, e = f.s.Batches(ctx, f.actor, f.account)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range bs {
		if b.Available != "0" || b.Reserved != "0" {
			t.Fatalf("expired %+v", b)
		}
	}
}
func TestAnnualLateMaintenanceAndRefundFreeze(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-year", "annual")
	f.s.now = func() time.Time { return dt("2024-08-15T12:00:00Z") }
	if _, e := f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	bs, e := f.s.Batches(ctx, f.actor, f.account)
	if e != nil || len(bs) != 2 {
		t.Fatalf("late batches %+v %v", bs, e)
	}
	for _, b := range bs {
		if b.StartsAt.Month() != time.January && b.StartsAt.Month() != time.July {
			t.Fatal(b)
		}
	}
	ref, e := f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "refund")
	if e != nil || ref.RefundStatus != "REFUNDED" {
		t.Fatalf("refund %+v %v", ref, e)
	}
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	subs, _ := f.s.Subscriptions(ctx, f.actor, f.account)
	if subs[0].Status != "refunded" {
		t.Fatal(subs)
	}
	bs, _ = f.s.Batches(ctx, f.actor, f.account)
	if len(bs) != 2 {
		t.Fatal("late grants resurrected")
	}
}
func TestConcurrentPurchaseCannotSpendUnifiedTwice(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.auth(t)
	_, e := f.s.SetAuthorization(ctx, f.actor, f.account, Authorization{ProductID: "agentwiki", Purpose: "funding", Enabled: true, MaxPerOperation: "1000000000", MaxTotal: "1000000000", ExpiresAt: dt("2026-01-01T00:00:00Z")})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"race-sw", "race-aw"} {
		product := "superwagie"
		if id == "race-aw" {
			product = "agentwiki"
		}
		x := Offer{ID: id, ProductID: product, Name: "race", AccountKind: "both", Kind: "addon", Price: "800000000", MonthlyCredit: "1", ValidityMonths: 1}
		if _, e = f.s.SaveOffer(ctx, f.actor, x); e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.PublishOffer(ctx, f.actor, id); e != nil {
			t.Fatal(e)
		}
	}
	var wg sync.WaitGroup
	out := make(chan Order, 2)
	for _, id := range []string{"race-sw", "race-aw"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			o, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{id, id})
			if e != nil && !errors.Is(e, ledger.ErrInsufficientFunds) {
				t.Error(e)
			}
			out <- o
		}(id)
	}
	wg.Wait()
	close(out)
	paid := 0
	for o := range out {
		if o.PaymentStatus == "PAID" {
			paid++
		}
	}
	if paid != 1 {
		t.Fatalf("paid %d", paid)
	}
	n, e := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	if e != nil || n != 200000000 {
		t.Fatalf("unified %d %v", n, e)
	}
}

// responseLoss delegates real writes and only drops one successful response.
// It models process/network uncertainty, not ledger accounting behavior.
type responseLoss struct {
	ledger.Ledger
	remaining   int
	balanceFail bool
}

func (l *responseLoss) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	tr, e := l.Ledger.Post(ctx, c)
	if e == nil && l.remaining > 0 {
		l.remaining--
		return ledger.Transaction{}, ledger.ErrUnknown
	}
	return tr, e
}
func (l *responseLoss) Balance(ctx context.Context, a, b string) (int64, error) {
	if l.balanceFail {
		l.balanceFail = false
		return 0, ledger.ErrUnknown
	}
	return l.Ledger.Balance(ctx, a, b)
}
func TestPaymentResponseLossRecoversWithoutNewAllocationOrGrant(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	o, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"sandbox-sw-month-1", "lost"})
	if !errors.Is(e, ErrPending) || o.PaymentStatus != "PENDING" {
		t.Fatalf("lost %+v %v", o, e)
	}
	if _, e = f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"sandbox-addon", "other"}); !errors.Is(e, ErrPending) {
		t.Fatalf("new allocation allowed: %v", e)
	}
	f.s = New(f.s.pool, f.s.accounts, f.l)
	f.s.ConfigureSandbox(true)
	f.s.ConfigureOperators([]string{f.actor})
	f.s.ConfigureTrustedServices([]string{"router"})
	f.s.now = func() time.Time { return dt("2024-01-31T12:00:00Z") }
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	re := f.buy(t, "sandbox-sw-month-1", "lost")
	if re.ID != o.ID || re.PaymentStatus != "PAID" || re.FulfillmentStatus != "FULFILLED" {
		t.Fatal(re)
	}
	bs, e := f.s.Batches(ctx, f.actor, f.account)
	if e != nil || len(bs) != 1 || bs[0].Original != "1000000" {
		t.Fatalf("batches %+v %v", bs, e)
	}
	n, e := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	if e != nil || n != 999000000 {
		t.Fatalf("cash %d %v", n, e)
	}
}
func TestRefundFrozenWindowRecoversAndNeverIssuesLateGrant(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-year", "annual")
	f.s.ledger = &responseLoss{Ledger: f.l, balanceFail: true}
	_, e := f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "refund")
	if !errors.Is(e, ledger.ErrUnknown) {
		t.Fatalf("fault %v", e)
	}
	f.s.ledger = f.l
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	orders, e := f.s.Orders(ctx, f.actor, f.account)
	if e != nil || orders[0].RefundStatus != "REFUNDED" {
		t.Fatalf("recovery %+v %v", orders, e)
	}
	f.s.now = func() time.Time { return dt("2024-07-01T00:00:00Z") }
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	bs, e := f.s.Batches(ctx, f.actor, f.account)
	if e != nil || len(bs) != 1 || bs[0].Available != "0" || bs[0].Withdrawn != "1000000" {
		t.Fatalf("late %+v %v", bs, e)
	}
}
func TestCreatedCashCommandRechecksFundingRevocation(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	initial := f.buy(t, "sandbox-sw-month-1", "initial")
	// Persist a second immutable intent and its budget reservation, then simulate
	// restart before SUBMITTING. No ledger write exists for this new reference.
	o := initial
	o.ID = id("bo_")
	o.PaymentStatus = "PENDING"
	o.FulfillmentStatus = "PENDING"
	o.Kind = "addon"
	o.SubscriptionID = ""
	o.Offer.Kind = "addon"
	tx, e := f.s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO billing_orders(id,account_id,actor_id,request_key,request_digest,snapshot) VALUES($1,$2,$3,'created','digest',$4)`, o.ID, f.account, f.actor, encode(o)); e != nil {
		t.Fatal(e)
	}
	settings, e := f.s.Settings(ctx, f.actor, f.account)
	if e != nil {
		t.Fatal(e)
	}
	a := settings[0]
	a.Used = "2000000"
	if e = updateAuth(ctx, tx, f.account, a); e != nil {
		t.Fatal(e)
	}
	paid := o
	paid.PaymentStatus = "PAID"
	cmd, e := f.s.createCommand(ctx, tx, f.account, f.actor, "payment", o.ID, []ledger.Posting{{Source: wallet.Address(f.account, ""), Destination: wallet.Address(f.account, "superwagie"), Asset: wallet.Asset, Amount: 1000000}, {Source: wallet.Address(f.account, "superwagie"), Destination: "commercial:revenue:superwagie", Asset: wallet.Asset, Amount: 1000000}}, effect{Order: &paid, Finance: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	a.Enabled = false
	if _, e = f.s.SetAuthorization(ctx, f.actor, f.account, a); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = f.s.pool.QueryRow(ctx, `SELECT status FROM billing_commands WHERE id=$1`, cmd.ID).Scan(&status); e != nil || status != "CANCELED" {
		t.Fatalf("command %s %v", status, e)
	}
	n, e := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	if e != nil || n != 999000000 {
		t.Fatalf("revoked cash %d %v", n, e)
	}
	settings, _ = f.s.Settings(ctx, f.actor, f.account)
	if settings[0].Used != "1000000" {
		t.Fatal("rejected intent kept reserved cap", settings)
	}
}
