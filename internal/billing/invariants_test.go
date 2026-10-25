package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

func rateFixture(t *testing.T, f fixture) {
	t.Helper()
	_, e := f.s.PublishRate(context.Background(), f.actor, Rate{ID: "r", Model: "fixture", Semantics: "exclusive_v1", Input: Rational{"1", "1"}, Output: Rational{"0", "1"}, CacheRead: Rational{"0", "1"}, CacheWrite: Rational{"0", "1"}, Reasoning: Rational{"0", "1"}})
	if e != nil {
		t.Fatal(e)
	}
}
func TestRepurchaseAfterRefundUsesNewGrantSource(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-month-1", "first")
	if _, e := f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "r"); e != nil {
		t.Fatal(e)
	}
	second := f.buy(t, "sandbox-sw-month-1", "second")
	if second.SubscriptionID == o.SubscriptionID {
		t.Fatal("new subscription reused refunded grant identity")
	}
	bs, e := f.s.Batches(ctx, f.actor, f.account)
	if e != nil || len(bs) != 2 {
		t.Fatalf("new grant %+v %v", bs, e)
	}
	if bs[0].SourceKey == bs[1].SourceKey {
		t.Fatal("grant identity collision")
	}
}
func TestRefundWithInflightCreditsFreezesNewReservations(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-month-1", "first")
	rateFixture(t, f)
	r := ReserveRequest{"call", f.actor, f.account, "superwagie", "d", "r", "500000", ""}
	if _, e := f.s.Reserve(ctx, "router", r); e != nil {
		t.Fatal(e)
	}
	ref, e := f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "r")
	if !errors.Is(e, ErrManualReview) || ref.RefundReason != "credits_reserved" {
		t.Fatalf("refund %+v %v", ref, e)
	}
	r.CallID = "second"
	if _, e = f.s.Reserve(ctx, "router", r); !errors.Is(e, ErrInsufficientCredits) {
		t.Fatalf("reserve frozen %v", e)
	}
	if _, e = f.s.Release(ctx, "router", "call", "pure_failure"); e != nil {
		t.Fatal(e)
	}
	n, e := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	if e != nil || n != 999000000 {
		t.Fatalf("premature cash refund %d %v", n, e)
	}
}
func TestMissingUsageKeepsHoldAndFinalExcessIsPlatformDifference(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-month-1", "first")
	rateFixture(t, f)
	r := ReserveRequest{"call", f.actor, f.account, "superwagie", "d", "r", "100", ""}
	if _, e := f.s.Reserve(ctx, "router", r); e != nil {
		t.Fatal(e)
	}
	c, e := f.s.Settle(ctx, "router", SettlementRequest{"call", nil, "20"})
	if e != nil || c.Status != "MISSING_USAGE" {
		t.Fatalf("missing %+v %v", c, e)
	}
	bs, _ := f.s.Batches(ctx, f.actor, f.account)
	if bs[0].Reserved != "100" {
		t.Fatal("missing usage released")
	}
	u := Usage{Input: "150", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
	c, e = f.s.Settle(ctx, "router", SettlementRequest{"call", &u, "20"})
	if e != nil || c.Charged != "100" || c.PlatformDifference != "50" {
		t.Fatalf("final %+v %v", c, e)
	}
	u.Input = "160"
	if _, e = f.s.Settle(ctx, "router", SettlementRequest{"call", &u, "20"}); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed retry %v", e)
	}
	bs, _ = f.s.Batches(ctx, f.actor, f.account)
	b := bs[0]
	if b.Available != "999900" || b.Reserved != "0" || b.Consumed != "100" {
		t.Fatal(b)
	}
}
func TestNoCreditsNeverFallsBackToCashAndServiceBinding(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	rateFixture(t, f)
	f.buy(t, "sandbox-sw-month-1", "first")
	r := ReserveRequest{"call", f.actor, f.account, "superwagie", "d", "r", "1000001", ""}
	if _, e := f.s.Reserve(ctx, "router", r); !errors.Is(e, ErrInsufficientCredits) {
		t.Fatalf("cash fallback %v", e)
	}
	r.Amount = "1"
	if _, e := f.s.Reserve(ctx, "router", r); e != nil {
		t.Fatal(e)
	}
	f.s.ConfigureTrustedServices([]string{"router", "other"})
	if _, e := f.s.GetCall(ctx, "other", "call"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("other service read %v", e)
	}
	r.RequestDigest = "different"
	if _, e := f.s.Reserve(ctx, "router", r); !errors.Is(e, ErrConflict) {
		t.Fatalf("digest accepted %v", e)
	}
}
func TestFundingAndRenewalAreSeparateAndKeepOriginalMonthAnchor(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-month-1", "first")
	f.s.now = func() time.Time { return dt("2024-02-29T12:00:00Z") }
	if _, e := f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	subs, _ := f.s.Subscriptions(ctx, f.actor, f.account)
	if !subs[0].PaidThrough.Equal(o.End) {
		t.Fatal("funding implied renewal")
	}
	_, e := f.s.SetAuthorization(ctx, f.actor, f.account, Authorization{ProductID: "superwagie", Purpose: "renewal", Enabled: true, MaxPerOperation: "1000000", MaxTotal: "2000000", ExpiresAt: dt("2025-01-01T00:00:00Z")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	subs, _ = f.s.Subscriptions(ctx, f.actor, f.account)
	if !subs[0].PaidThrough.Equal(dt("2024-03-31T12:00:00Z")) {
		t.Fatal(subs)
	}
	settings, _ := f.s.Settings(ctx, f.actor, f.account)
	for _, a := range settings {
		if a.Purpose == "funding" {
			a.MaxTotal = "3000000"
			a.MaxPerOperation = "3000000"
			updated, e := f.s.SetAuthorization(ctx, f.actor, f.account, a)
			if e != nil || updated.Used != "2000000" {
				t.Fatalf("reset cap %+v %v", updated, e)
			}
		}
	}
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	orders, _ := f.s.Orders(ctx, f.actor, f.account)
	if len(orders) != 2 {
		t.Fatal("renewal repeated")
	}
}
func TestCrossProductOriginUsesSharedBatchWithoutCashSpend(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	sw := f.buy(t, "sandbox-sw-month-1", "sw")
	_, e := f.l.Post(ctx, ledger.Command{Reference: "awfund", Postings: []ledger.Posting{{Source: "world", Destination: wallet.Address(f.account, "agentwiki"), Asset: wallet.Asset, Amount: 10}}})
	if e != nil {
		t.Fatal(e)
	}
	offer := Offer{ID: "aw", ProductID: "agentwiki", Name: "AW test", AccountKind: "both", Kind: "membership", Period: "month", Price: "1", MonthlyCredit: "1", Rank: 1}
	if _, e = f.s.SaveOffer(ctx, f.actor, offer); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishOffer(ctx, f.actor, "aw"); e != nil {
		t.Fatal(e)
	}
	f.buy(t, "aw", "aw")
	rateFixture(t, f)
	call, e := f.s.Reserve(ctx, "router", ReserveRequest{"call", f.actor, f.account, "agentwiki", "d", "r", "500000", ""})
	if e != nil {
		t.Fatal(e)
	}
	bs, _ := f.s.Batches(ctx, f.actor, f.account)
	found := false
	for _, a := range call.Allocations {
		for _, b := range bs {
			if a.BatchID == b.ID && b.OrderID == sw.ID {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("did not consume other product shared credits")
	}
	n, e := f.l.Balance(ctx, wallet.Address(f.account, "agentwiki"), wallet.Asset)
	if e != nil || n != 9 {
		t.Fatalf("model cash spend %d %v", n, e)
	}
}
func TestPaidUnfulfilledOrderBlocksSecondPurchase(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	o, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"sandbox-sw-month-1", "first"})
	if !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	var cmdID string
	if e = f.s.pool.QueryRow(ctx, `SELECT id FROM billing_commands WHERE kind='payment' AND request_key=$1`, o.ID).Scan(&cmdID); e != nil {
		t.Fatal(e)
	}
	if e = f.s.locked(ctx, f.account, func(c *pgxpool.Conn) error { return f.s.runLocked(ctx, c, cmdID) }); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"sandbox-sw-month-1", "second"}); !errors.Is(e, ErrPending) {
		t.Fatalf("second purchase %v", e)
	}
	if _, e = f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "refund"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	bs, _ := f.s.Batches(ctx, f.actor, f.account)
	if len(bs) != 0 {
		t.Fatal("late fulfillment resurrected refunded grants")
	}
}
func TestRefundPriorTermCannotRevokePaidRenewedTerm(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-month-1", "first")
	_, e := f.s.SetAuthorization(ctx, f.actor, f.account, Authorization{ProductID: "superwagie", Purpose: "renewal", Enabled: true, MaxPerOperation: "1000000", MaxTotal: "2000000", ExpiresAt: dt("2025-01-01T00:00:00Z")})
	if e != nil {
		t.Fatal(e)
	}
	f.s.now = func() time.Time { return dt("2024-03-01T00:00:00Z") }
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "refund"); !errors.Is(e, ErrManualReview) {
		t.Fatalf("prior-term refund %v", e)
	}
	subs, _ := f.s.Subscriptions(ctx, f.actor, f.account)
	if subs[0].Status != "active" || !subs[0].PaidThrough.Equal(dt("2024-03-31T12:00:00Z")) {
		t.Fatal("refund revoked paid renewed term", subs)
	}
}
func TestCreatedReservationRechecksAllocatedBatchExpiry(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-year", "annual")
	rateFixture(t, f)
	bs, _ := f.s.Batches(ctx, f.actor, f.account)
	call := Call{ServiceID: "router", CallID: "created", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "d", Rate: Rate{ID: "r"}, Held: "100", Charged: "0", PlatformDifference: "0", Status: "RESERVED", Allocations: []Allocation{{bs[0].ID, "100"}}}
	tx, e := f.s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'d',$3)`, call.CallID, f.account, encode(call)); e != nil {
		t.Fatal(e)
	}
	cmd, e := f.s.createCommand(ctx, tx, f.account, f.actor, "reserve", call.CallID, []ledger.Posting{{Source: BatchAddress(bs[0].ID, "available"), Destination: BatchAddress(bs[0].ID, "reserved"), Asset: CreditAsset, Amount: 100}}, effect{Call: &call})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	f.s.now = func() time.Time { return dt("2024-03-01T00:00:00Z") }
	if e = f.s.locked(ctx, f.account, func(c *pgxpool.Conn) error { return f.s.runLocked(ctx, c, cmd.ID) }); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = f.s.pool.QueryRow(ctx, `SELECT status FROM billing_commands WHERE id=$1`, cmd.ID).Scan(&status); e != nil || status != "CANCELED" {
		t.Fatalf("expired reserve %s %v", status, e)
	}
	n, e := f.l.Balance(ctx, BatchAddress(bs[0].ID, "reserved"), CreditAsset)
	if e != nil || n != 0 {
		t.Fatalf("reserved expired %d %v", n, e)
	}
}
func TestRevokedCredentialCannotStartOrIncreaseReservation(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-year", "annual")
	rateFixture(t, f)
	_, e := f.s.pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at) VALUES('cid','hash',$1,$2,'superwagie','["fixture"]','task',$3)`, f.actor, f.account, dt("2025-01-01T00:00:00Z"))
	if e != nil {
		t.Fatal(e)
	}
	r := ReserveRequest{"call", f.actor, f.account, "superwagie", "d", "r", "100", "cid"}
	if _, e = f.s.Reserve(ctx, "router", r); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.pool.Exec(ctx, `UPDATE model_credentials SET revoked_at=$1 WHERE id='cid'`, f.s.now()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.TopUp(ctx, "router", "call", "100", "more"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("topup revoked %v", e)
	}
	r.CallID = "new"
	if _, e = f.s.Reserve(ctx, "router", r); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("reserve revoked %v", e)
	}
	u := Usage{Input: "50", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
	if _, e = f.s.Settle(ctx, "router", SettlementRequest{"call", &u, "0"}); e != nil {
		t.Fatal("submitted could not finish", e)
	}
}
func TestFulfillmentRecoveryDoesNotDeadlockWithLaterWalletQueue(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	_, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"sandbox-sw-month-1", "first"})
	if !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	w := wallet.New(f.s.pool, f.s.accounts, f.l)
	op, e := w.Transfer(ctx, f.actor, f.account, "agentwiki", "1", "later")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	orders, _ := f.s.Orders(ctx, f.actor, f.account)
	if orders[0].FulfillmentStatus != "FULFILLED" {
		t.Fatal("earlier order blocked by later wallet command", orders)
	}
	if e = w.Process(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	balance, e := f.l.Balance(ctx, wallet.Address(f.account, "agentwiki"), wallet.Asset)
	if e != nil || balance != 1 {
		t.Fatalf("follower %d %v", balance, e)
	}
}
func TestTeamRemovalStopsNewHoldsButSubmittedCallFinishes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	team, e := f.s.accounts.CreateTeam(ctx, f.actor, "Team")
	if e != nil {
		t.Fatal(e)
	}
	u, e := f.s.accounts.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://billing-test", Subject: "member"})
	if e != nil {
		t.Fatal(e)
	}
	inv, e := f.s.accounts.Invite(ctx, f.actor, team.ID, u.ID, "member")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.accounts.AcceptInvitation(ctx, u.ID, inv.ID); e != nil {
		t.Fatal(e)
	}
	_, e = f.l.Post(ctx, ledger.Command{Reference: "teamfund", Postings: []ledger.Posting{{Source: "world", Destination: wallet.Address(team.ID, ""), Asset: wallet.Asset, Amount: 10000000}}})
	if e != nil {
		t.Fatal(e)
	}
	f.account = team.ID
	f.auth(t)
	f.buy(t, "sandbox-sw-month-1", "first")
	rateFixture(t, f)
	if _, e = f.s.Orders(ctx, u.ID, f.account); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("member finance %v", e)
	}
	if _, e = f.s.Batches(ctx, u.ID, f.account); e != nil {
		t.Fatal("member quota visibility", e)
	}
	r := ReserveRequest{"teamcall", u.ID, f.account, "superwagie", "d", "r", "100", ""}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, e = f.s.Reserve(ctx, "router", r); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	if e = f.s.accounts.RemoveMember(ctx, f.actor, f.account, u.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	r.CallID = "new"
	if _, e = f.s.Reserve(ctx, "router", r); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("removed reserve %v", e)
	}
	usage := Usage{Input: "50", Output: "0", CacheRead: "0", CacheWrite: "0", Reasoning: "0"}
	if _, e = f.s.Settle(ctx, "router", SettlementRequest{"teamcall", &usage, "0"}); e != nil {
		t.Fatal("submitted call lost payer", e)
	}
}
func TestTopupAndRefundResponseLossRecoverOriginalCommands(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-month-1", "first")
	rateFixture(t, f)
	if _, e := f.s.Reserve(ctx, "router", ReserveRequest{"call", f.actor, f.account, "superwagie", "d", "r", "100", ""}); e != nil {
		t.Fatal(e)
	}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, e := f.s.TopUp(ctx, "router", "call", "200", "top"); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	if _, e := f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	call, e := f.s.TopUp(ctx, "router", "call", "200", "top")
	if e != nil || call.Held != "300" {
		t.Fatalf("top %+v %v", call, e)
	}
	if _, e = f.s.TopUp(ctx, "router", "call", "201", "top"); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed topup %v", e)
	}
	if _, e = f.s.Release(ctx, "router", "call", "failed"); e != nil {
		t.Fatal(e)
	}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	if _, e = f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "refund"); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	ref, e := f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "refund")
	if e != nil || ref.RefundStatus != "REFUNDED" {
		t.Fatalf("refund %+v %v", ref, e)
	}
	bs, _ := f.s.Batches(ctx, f.actor, f.account)
	if bs[0].Withdrawn != "1000000" || bs[0].Available != "0" || bs[0].Reserved != "0" {
		t.Fatal(bs)
	}
	n, e := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	if e != nil || n != 1000000000 {
		t.Fatalf("refund cash %d %v", n, e)
	}
}
func TestDraftsSandboxAndImmutablePublishedVersions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	production := New(f.s.pool, f.s.accounts, f.l)
	production.ConfigureOperators([]string{f.actor})
	catalog, e := production.Catalog(ctx)
	if e != nil || len(catalog) != 0 {
		t.Fatalf("sandbox leaked %+v %v", catalog, e)
	}
	if _, e = production.SeedSandbox(ctx); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("production seed", e)
	}
	if _, e = production.Purchase(ctx, f.actor, f.account, PurchaseRequest{"sandbox-sw-month-1", "prod"}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("production sandbox purchase", e)
	}
	offer := Offer{ID: "draft", Name: "Configured draft", ProductID: "superwagie", Kind: "membership", Period: "month", AccountKind: "team", Price: "10", MonthlyCredit: "100", Rank: 1}
	if _, e = f.s.SaveOffer(ctx, f.actor, offer); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"draft", "d"}); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatalf("draft sold %v", e)
	}
	if _, e = f.s.PublishOffer(ctx, f.actor, "draft"); e != nil {
		t.Fatal(e)
	}
	offer.Price = "20"
	if _, e = f.s.SaveOffer(ctx, f.actor, offer); !errors.Is(e, ErrConflict) {
		t.Fatalf("published mutated %v", e)
	}
	if _, e = f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"draft", "team"}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("wrong account applicability %v", e)
	}
}

// uncertainRejection models a temporarily missing reference lookup followed by a
// rejected retry after the original real ledger write's response was lost.
type uncertainRejection struct {
	ledger.Ledger
	lost, retry bool
}

func (l *uncertainRejection) Post(ctx context.Context, c ledger.Command) (ledger.Transaction, error) {
	if !l.lost {
		tr, e := l.Ledger.Post(ctx, c)
		if e == nil {
			l.lost = true
			return ledger.Transaction{}, ledger.ErrUnknown
		}
		return tr, e
	}
	if !l.retry {
		l.retry = true
		return ledger.Transaction{}, ledger.ErrInsufficientFunds
	}
	return l.Ledger.Post(ctx, c)
}
func (l *uncertainRejection) Find(ctx context.Context, ref string) (*ledger.Transaction, error) {
	if l.lost && !l.retry {
		return nil, nil
	}
	return l.Ledger.Find(ctx, ref)
}
func TestUncertainPaymentDoesNotBecomeRejectedOnRetryBalanceError(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.s.ledger = &uncertainRejection{Ledger: f.l}
	o, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{"sandbox-sw-month-1", "uncertain"})
	if !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	orders, _ := f.s.Orders(ctx, f.actor, f.account)
	if orders[0].PaymentStatus != "PENDING" {
		t.Fatalf("uncertain became terminal %+v", orders)
	}
	if _, e = f.s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	orders, _ = f.s.Orders(ctx, f.actor, f.account)
	if orders[0].ID != o.ID || orders[0].PaymentStatus != "PAID" || orders[0].FulfillmentStatus != "FULFILLED" {
		t.Fatal(orders)
	}
}
func TestUnconfiguredRefundPolicyRequiresReviewAndOperatorCanReject(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	offer := Offer{ID: "configured", ProductID: "superwagie", Name: "Configured terms", AccountKind: "both", Kind: "membership", Period: "year", Price: "100", MonthlyCredit: "1000", Rank: 1}
	if _, e := f.s.SaveOffer(ctx, f.actor, offer); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.PublishOffer(ctx, f.actor, offer.ID); e != nil {
		t.Fatal(e)
	}
	o := f.buy(t, offer.ID, "real-configured")
	ref, e := f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "refund")
	if !errors.Is(e, ErrManualReview) || ref.RefundReason != "refund_policy_not_configured" {
		t.Fatalf("policy %+v %v", ref, e)
	}
	if _, e = f.s.ResolveRefundReview(ctx, "not-operator", f.account, o.ID, "reject", "reason"); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("nonoperator %v", e)
	}
	if _, e = f.s.ResolveRefundReview(ctx, f.actor, f.account, o.ID, "approve", "reason"); !errors.Is(e, ErrUnsupported) {
		t.Fatalf("unsafe approve %v", e)
	}
	ref, e = f.s.ResolveRefundReview(ctx, f.actor, f.account, o.ID, "reject", "No published refund condition")
	if e != nil || ref.RefundStatus != "REJECTED" {
		t.Fatalf("reject %+v %v", ref, e)
	}
	rateFixture(t, f)
	if _, e = f.s.Reserve(ctx, "router", ReserveRequest{"after", f.actor, f.account, "superwagie", "d", "r", "100", ""}); e != nil {
		t.Fatal("rejection did not thaw", e)
	}
	f.s.now = func() time.Time { return dt("2024-03-01T00:00:00Z") }
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	bs, _ := f.s.Batches(ctx, f.actor, f.account)
	if len(bs) != 2 {
		t.Fatal("rejected refund stopped paid monthly grants", bs)
	}
}
func TestOperatorQueueCannotBeReadByOrdinaryMember(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	o := f.buy(t, "sandbox-sw-month-1", "first")
	if _, e := f.s.OperatorOrders(ctx, "not-operator", ""); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("operator queue %v", e)
	}
	orders, e := f.s.OperatorOrders(ctx, f.actor, "")
	if e != nil || len(orders) != 1 || orders[0].ID != o.ID {
		t.Fatalf("queue %+v %v", orders, e)
	}
}
func TestOneBrokenRecoveryEffectDoesNotStarveOtherAccount(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	team, e := f.s.accounts.CreateTeam(ctx, f.actor, "Second account")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	bad := Subscription{ID: "bad", AccountID: f.account, ProductID: "missing-product"}
	if _, e = f.s.createCommand(ctx, tx, f.account, f.actor, "monthly", "bad", nil, effect{Subscription: &bad}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tx, e = f.s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	good := Subscription{ID: "good", AccountID: team.ID, ProductID: "agentwiki", Status: "active"}
	cmd, e := f.s.createCommand(ctx, tx, team.ID, f.actor, "monthly", "good", nil, effect{Subscription: &good})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Recover(ctx); e == nil {
		t.Fatal("broken metadata application omitted error")
	}
	var status string
	if e = f.s.pool.QueryRow(ctx, `SELECT status FROM billing_commands WHERE id=$1`, cmd.ID).Scan(&status); e != nil || status != "APPLIED" {
		t.Fatalf("unrelated recovery starved %s %v", status, e)
	}
}
