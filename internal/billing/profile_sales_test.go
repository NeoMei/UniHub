package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/ledger"
)

func setProfile(t *testing.T, f fixture, preset string) BillingProfile {
	t.Helper()
	ctx := context.Background()
	p, e := BillingPreset("superwagie", preset)
	if e != nil {
		t.Fatal(e)
	}
	current, e := f.s.Profile(ctx, p.ProductID)
	if e != nil {
		t.Fatal(e)
	}
	p.Version = current.Version
	p, e = f.s.SaveProfile(ctx, f.actor, p.ProductID, p)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func pureOffer(id string) Offer {
	return Offer{ID: id, ProductID: "superwagie", Name: "Service membership", AccountKind: "both", Kind: "membership", Period: "year", Price: "12000000", MonthlyCredit: "0", Rank: 1, UpgradeRule: "remaining_term_prorata_v1"}
}
func publishTestOffer(t *testing.T, f fixture, o Offer) Offer {
	t.Helper()
	ctx := context.Background()
	if _, e := f.s.SaveOffer(ctx, f.actor, o); e != nil {
		t.Fatal(e)
	}
	got, e := f.s.PublishOffer(ctx, f.actor, o.ID)
	if e != nil {
		t.Fatal(e)
	}
	return got
}

func TestPureSubscriptionHasRightsWithoutCreditBatchesOrMonthlyCommands(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	setProfile(t, f, "subscription")
	offer := publishTestOffer(t, f, pureOffer("pure-year"))
	o := f.buy(t, offer.ID, "pure-purchase")
	if o.GrantAmount != "0" || o.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("pure fulfillment %+v", o)
	}
	f.s.now = func() time.Time { return dt("2024-08-15T12:00:00Z") }
	if _, e := f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	subs, e := f.s.Subscriptions(ctx, f.actor, f.account)
	if e != nil || len(subs) != 1 || subs[0].Status != "active" || !subs[0].PaidThrough.Equal(dt("2025-01-31T12:00:00Z")) {
		t.Fatalf("paid rights %+v %v", subs, e)
	}
	var batches, monthly int
	if e = f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_batches WHERE account_id=$1`, f.account).Scan(&batches); e != nil {
		t.Fatal(e)
	}
	if e = f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_commands WHERE account_id=$1 AND kind='monthly'`, f.account).Scan(&monthly); e != nil {
		t.Fatal(e)
	}
	if batches != 0 || monthly != 0 {
		t.Fatalf("pure subscription issued batches=%d monthly commands=%d", batches, monthly)
	}
	r, e := f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "refund")
	if !errors.Is(e, ErrManualReview) || r.RefundStatus != "MANUAL_REVIEW" {
		t.Fatalf("pure refund %+v %v", r, e)
	}
}

func TestPureSubscriptionRenewalAndUpgradeKeepZeroCredit(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	setProfile(t, f, "subscription")
	o := pureOffer("pure-month")
	o.Period = "month"
	o.Price = "1000000"
	publishTestOffer(t, f, o)
	f.buy(t, o.ID, "pure-buy")
	subs, e := f.s.Subscriptions(ctx, f.actor, f.account)
	if e != nil {
		t.Fatal(e)
	}
	high := o
	high.ID = "pure-high"
	high.Rank = 2
	high.Price = "2000000"
	publishTestOffer(t, f, high)
	q, e := f.s.QuoteUpgrade(ctx, f.actor, f.account, subs[0].ID, high.ID)
	if e != nil || q.GrantAmount != "0" {
		t.Fatalf("pure upgrade quote %+v %v", q, e)
	}
	if _, e = f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, "upgrade"); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.SetAuthorization(ctx, f.actor, f.account, Authorization{ProductID: "superwagie", Purpose: "renewal", Enabled: true, MaxPerOperation: "10000000", MaxTotal: "100000000", ExpiresAt: dt("2026-01-01T00:00:00Z")})
	if e != nil {
		t.Fatal(e)
	}
	f.s.now = func() time.Time { return dt("2024-03-01T12:00:00Z") }
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	subs, e = f.s.Subscriptions(ctx, f.actor, f.account)
	if e != nil || len(subs) != 1 || !subs[0].PaidThrough.Equal(dt("2024-03-31T12:00:00Z")) {
		t.Fatalf("renewal %+v %v", subs, e)
	}
	var batches int
	if e = f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_batches WHERE account_id=$1`, f.account).Scan(&batches); e != nil {
		t.Fatal(e)
	}
	if batches != 0 {
		t.Fatalf("pure renew/upgrade issued %d batches", batches)
	}
}

func TestDisabledBillingComponentsStopNewSalesButKeepPaidAnnualTerms(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	annual := f.buy(t, "sandbox-sw-year", "paid-annual")
	// Publishing must recheck a draft against the current component policy.
	draft := f.offers[0]
	draft.ID = "draft-before-disable"
	draft.Status = "draft"
	if _, e := f.s.SaveOffer(ctx, f.actor, draft); e != nil {
		t.Fatal(e)
	}
	setProfile(t, f, "prepaid")
	if _, e := f.s.PublishOffer(ctx, f.actor, draft.ID); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("disabled subscription published: %v", e)
	}
	if _, e := f.s.SaveOffer(ctx, f.actor, pureOffer("disabled-save")); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("disabled subscription saved: %v", e)
	}
	if _, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: "sandbox-sw-month-1", Key: "disabled-buy"}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("disabled subscription purchased: %v", e)
	}
	replay := f.buy(t, "sandbox-sw-year", "paid-annual")
	if replay.ID != annual.ID {
		t.Fatal("historical purchase replay changed")
	}
	cat, e := f.s.Catalog(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, o := range cat {
		if o.ProductID == "superwagie" {
			t.Fatalf("disabled offer remains for sale: %+v", o)
		}
	}
	f.s.now = func() time.Time { return dt("2024-08-15T12:00:00Z") }
	if _, e = f.s.Maintain(ctx, f.s.now()); e != nil {
		t.Fatal(e)
	}
	bs, e := f.s.Batches(ctx, f.actor, f.account)
	if e != nil || len(bs) != 2 {
		t.Fatalf("paid annual grants lost %+v %v", bs, e)
	}
	for _, b := range bs {
		if b.Original != "1000000" {
			t.Fatalf("historical grant repriced: %+v", b)
		}
	}
}

func TestBillingOfferMonthlyCreditCoherenceAndIndependentAddons(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	setProfile(t, f, "subscription")
	credit := f.offers[0]
	credit.ID = "credit-after-pure"
	if _, e := f.s.SaveOffer(ctx, f.actor, credit); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("credits under pure profile accepted: %v", e)
	}
	setProfile(t, f, "hybrid")
	if _, e := f.s.SaveOffer(ctx, f.actor, pureOffer("pure-under-hybrid")); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("missing included credit accepted: %v", e)
	}
	p, _ := BillingPreset("superwagie", "hybrid")
	p.Subscription.Enabled = false
	p.IncludedCredits.Enabled = false
	p.Usage.RequireSubscription = false
	current, e := f.s.Profile(ctx, p.ProductID)
	if e != nil {
		t.Fatal(e)
	}
	p.Version = current.Version
	if _, e = f.s.SaveProfile(ctx, f.actor, p.ProductID, p); e != nil {
		t.Fatal(e)
	}
	addon := Offer{ID: "independent-addon", ProductID: "superwagie", Name: "Credits", AccountKind: "both", Kind: "addon", Price: "1000000", MonthlyCredit: "2000000", ValidityMonths: 1}
	publishTestOffer(t, f, addon)
	f.auth(t)
	f.buy(t, addon.ID, "addon")
	p = setProfile(t, f, "prepaid")
	if _, e = f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: addon.ID, Key: "disabled-addon"}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("disabled addon purchased: %v", e)
	}
}

func TestHistoricalPureSubscriptionCannotAutoRefundFromNoCreditConsumption(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	setProfile(t, f, "subscription")
	offer := publishTestOffer(t, f, pureOffer("historical-pure"))
	o := f.buy(t, offer.ID, "historical-buy")
	// Legacy/imported paid snapshots can predate today's authoring guard. Their
	// lack of credit consumption is never evidence that service rights are unused.
	if _, e := f.s.pool.Exec(ctx, `UPDATE billing_orders SET snapshot=jsonb_set(snapshot,'{offer,refund_rule}','"unused_only_v1"') WHERE id=$1`, o.ID); e != nil {
		t.Fatal(e)
	}
	got, e := f.s.RequestRefund(ctx, f.actor, f.account, o.ID, "pure-refund")
	if !errors.Is(e, ErrManualReview) || got.RefundStatus != "MANUAL_REVIEW" {
		t.Fatalf("pure service automatically refunded: %+v %v", got, e)
	}
}

// A policy change racing a balance read must be checked before the durable new
// payment is inserted; once persisted, that order instead owns immutable terms.
type pauseSaleBalance struct {
	ledger.Ledger
	entered, resume chan struct{}
	once            sync.Once
}

func (l *pauseSaleBalance) Balance(ctx context.Context, address, asset string) (int64, error) {
	l.once.Do(func() { close(l.entered); <-l.resume })
	return l.Ledger.Balance(ctx, address, asset)
}
func TestBillingProfileDisableSerializesWithNewOrderCreation(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	paused := &pauseSaleBalance{Ledger: f.l, entered: make(chan struct{}), resume: make(chan struct{})}
	f.s.ledger = paused
	done := make(chan error, 1)
	go func() {
		_, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: "sandbox-sw-month-1", Key: "racing-sale"})
		done <- e
	}()
	<-paused.entered
	setProfile(t, f, "prepaid")
	close(paused.resume)
	if e := <-done; !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("order committed after component disabled: %v", e)
	}
	var orders int
	if e := f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_orders WHERE account_id=$1`, f.account).Scan(&orders); e != nil {
		t.Fatal(e)
	}
	if orders != 0 {
		t.Fatalf("disabled sale persisted %d orders", orders)
	}
}

func TestLegacyRateRepublishPreservesIdempotencyAfterAssetDefault(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := Rate{ID: "legacy-asset-rate", Model: "test", Semantics: "exclusive_v1", Input: Rational{"1", "1"}, Output: Rational{"0", "1"}, CacheRead: Rational{"0", "1"}, CacheWrite: Rational{"0", "1"}, Reasoning: Rational{"0", "1"}}
	// Pre-upgrade snapshots did not store an asset field.
	r.Status = "published"
	if _, e := f.s.pool.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2)`, r.ID, encode(r)); e != nil {
		t.Fatal(e)
	}
	saved, e := f.s.PublishRate(ctx, f.actor, r)
	if e != nil || saved.ID != r.ID {
		t.Fatalf("legacy rate replay %+v %v", saved, e)
	}
}

func TestCatalogOmitsDisabledProductsWithoutHidingOtherProducts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, e := f.s.pool.Exec(ctx, `UPDATE products SET enabled=false WHERE id='superwagie'`); e != nil {
		t.Fatal(e)
	}
	offers, e := f.s.Catalog(ctx)
	if e != nil {
		t.Fatalf("one disabled product broke catalog: %v", e)
	}
	if len(offers) != 1 || offers[0].ID != "sandbox-addon" {
		t.Fatalf("enabled product offerings lost: %+v", offers)
	}
}
