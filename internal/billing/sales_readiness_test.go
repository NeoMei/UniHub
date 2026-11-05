package billing

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
	"unihub/internal/metering"
)

func TestCreditSaleUnavailableHasNoFinancialEffects(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	if _, e := f.s.pool.Exec(ctx, `UPDATE billing_rates SET status='retired'`); e != nil {
		t.Fatal(e)
	}
	_, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: "sandbox-sw-month-1", Key: "no-credit-coverage"})
	if e == nil || e.Error() != "billing: credit_rates_unavailable" {
		t.Fatalf("unusable credits sold: %v", e)
	}
	for _, table := range []string{"billing_orders", "billing_commands", "billing_batches"} {
		var n int
		if e := f.s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE account_id=$1", f.account).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n != 0 {
			t.Fatalf("blocked sale persisted %s=%d", table, n)
		}
	}
}

func TestCreditSaleReadinessSelection(t *testing.T) {
	p, _ := BillingPreset("superwagie", "hybrid")
	valid := Rate{ID: "r", Model: "model", Status: "published", Semantics: "exclusive_v1", Input: Rational{"0", "1"}, Output: Rational{"0", "1"}, CacheRead: Rational{"0", "1"}, CacheWrite: Rational{"0", "1"}, Reasoning: Rational{"0", "1"}}
	for _, tc := range []struct {
		name   string
		models []string
		rates  []Rate
		ready  bool
	}{
		{"no rates", nil, nil, false}, {"legacy unrestricted", nil, []Rate{valid}, true}, {"deny all", []string{}, []Rate{valid}, false}, {"literal star", []string{"*"}, []Rate{valid}, false}, {"wrong model", []string{"other"}, []Rate{valid}, false}, {"allowed", []string{"model"}, []Rate{valid}, true},
		{"duplicate", nil, []Rate{valid, valid}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.Usage.Models = tc.models
			if got := creditSaleReadiness(p, tc.rates); got.Ready != tc.ready {
				t.Fatalf("%+v", got)
			}
		})
	}
	for _, change := range []struct {
		name  string
		apply func(*Rate)
	}{
		{"cash", func(r *Rate) { r.Asset = CashAsset }}, {"retired", func(r *Rate) { r.Status = "retired" }}, {"empty semantics", func(r *Rate) { r.Semantics = "" }}, {"unknown semantics", func(r *Rate) { r.Semantics = "unknown" }}, {"bad terms", func(r *Rate) { r.Input.Denominator = "0" }}, {"missing id", func(r *Rate) { r.ID = "" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			r := valid
			change.apply(&r)
			p.Usage.Models = nil
			if creditSaleReadiness(p, []Rate{r}).Ready {
				t.Fatal("unusable tariff accepted")
			}
		})
	}
	p.Usage.Models = nil
	p.Usage.DefaultFundingMode = FundingPrepaidCash
	p.Usage.FundingModes = append(p.Usage.FundingModes, FundingPrepaidCash)
	if !creditSaleReadiness(p, []Rate{valid}).Ready {
		t.Fatal("cash default hid credit mode")
	}
}

func TestCreditSaleRetirementKeepsPaidReplay(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	order := f.buy(t, "sandbox-sw-year", "paid-before-retirement")
	if e := f.s.RetireRate(ctx, f.actor, "fixture-sale-readiness"); e != nil {
		t.Fatal(e)
	}
	replay := f.buy(t, "sandbox-sw-year", "paid-before-retirement")
	if replay.ID != order.ID {
		t.Fatal("replay changed order")
	}
	if _, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: "sandbox-sw-month-2", Key: "new-after-retirement"}); e != ErrCreditRatesUnavailable {
		t.Fatalf("retired SQL status ignored: %v", e)
	}
	view, e := f.s.CatalogView(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, o := range view {
		if o.SalesReadiness.Ready || o.SalesReadiness.Reason != "credit_rates_unavailable" {
			t.Fatalf("unavailable offer hidden or ready: %+v", o)
		}
	}
}

func TestCreditSaleUpgradeAndRenewalRecheckReadiness(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-month-1", "initial")
	subs, e := f.s.Subscriptions(ctx, f.actor, f.account)
	if e != nil {
		t.Fatal(e)
	}
	q, e := f.s.QuoteUpgrade(ctx, f.actor, f.account, subs[0].ID, "sandbox-sw-month-2")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.RetireRate(ctx, f.actor, "fixture-sale-readiness"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.AcceptUpgrade(ctx, f.actor, f.account, q.ID, "unusable-upgrade"); e != ErrCreditRatesUnavailable {
		t.Fatalf("upgrade: %v", e)
	}
	if _, e = f.s.QuoteUpgrade(ctx, f.actor, f.account, subs[0].ID, "sandbox-sw-month-3"); e != ErrCreditRatesUnavailable {
		t.Fatalf("quote: %v", e)
	}
	_, e = f.s.SetAuthorization(ctx, f.actor, f.account, Authorization{ProductID: "superwagie", Purpose: "renewal", Enabled: true, MaxPerOperation: "10000000", MaxTotal: "100000000", ExpiresAt: dt("2026-01-01T00:00:00Z")})
	if e != nil {
		t.Fatal(e)
	}
	now := dt("2024-03-01T12:00:00Z")
	e = f.s.locked(ctx, f.account, func(c *pgxpool.Conn) error { return f.s.renewLocked(ctx, c, subs[0], now) })
	if e != ErrCreditRatesUnavailable {
		t.Fatalf("renewal: %v", e)
	}
	var n int
	if e = f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_orders WHERE account_id=$1`, f.account).Scan(&n); e != nil || n != 1 {
		t.Fatalf("new commitment created: %d %v", n, e)
	}
}

func TestCreditSalePendingRenewalReplaysAfterRetirement(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	f.buy(t, "sandbox-sw-month-1", "initial")
	subs, e := f.s.Subscriptions(ctx, f.actor, f.account)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.SetAuthorization(ctx, f.actor, f.account, Authorization{ProductID: "superwagie", Purpose: "renewal", Enabled: true, MaxPerOperation: "10000000", MaxTotal: "100000000", ExpiresAt: dt("2026-01-01T00:00:00Z")})
	if e != nil {
		t.Fatal(e)
	}
	now := dt("2024-03-01T12:00:00Z")
	f.s.now = func() time.Time { return now }
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	e = f.s.locked(ctx, f.account, func(c *pgxpool.Conn) error { return f.s.renewLocked(ctx, c, subs[0], now) })
	if !errors.Is(e, ErrPending) {
		t.Fatalf("expected persisted pending renewal: %v", e)
	}
	if e = f.s.RetireRate(ctx, f.actor, "fixture-sale-readiness"); e != nil {
		t.Fatal(e)
	}
	f.s.ledger = f.l
	if e = f.s.locked(ctx, f.account, func(c *pgxpool.Conn) error { return f.s.renewLocked(ctx, c, subs[0], now) }); e != nil {
		t.Fatalf("original renewal recovery blocked: %v", e)
	}
	orders, e := f.s.Orders(ctx, f.actor, f.account)
	if e != nil || len(orders) != 2 {
		t.Fatalf("orders=%+v %v", orders, e)
	}
	for _, o := range orders {
		if o.PaymentStatus != "PAID" || o.FulfillmentStatus != "FULFILLED" {
			t.Fatalf("not fulfilled: %+v", o)
		}
	}
}

func TestCreditSaleTariffRetirementDuringBalanceRead(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	paused := &pauseSaleBalance{Ledger: f.l, entered: make(chan struct{}), resume: make(chan struct{})}
	f.s.ledger = paused
	done := make(chan error, 1)
	go func() {
		_, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: "sandbox-sw-month-1", Key: "retirement-race"})
		done <- e
	}()
	<-paused.entered
	if e := f.s.RetireRate(ctx, f.actor, "fixture-sale-readiness"); e != nil {
		t.Fatal(e)
	}
	close(paused.resume)
	if e := <-done; e != ErrCreditRatesUnavailable {
		t.Fatalf("tariff removed before admission: %v", e)
	}
	var n int
	if e := f.s.pool.QueryRow(ctx, `SELECT count(*) FROM billing_orders WHERE account_id=$1`, f.account).Scan(&n); e != nil || n != 0 {
		t.Fatalf("persisted orders=%d %v", n, e)
	}
}

func TestCreditSaleReadinessOperations(t *testing.T) {
	p, _ := BillingPreset("superwagie", "hybrid")
	price := func(operation, unit, key string) Rate {
		return Rate{ID: operation, Model: "m", Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: operation, Policy: "fixture", Terms: []metering.Term{{Key: key, Unit: unit, Price: metering.Rational{Numerator: "0", Denominator: "1"}}}}}
	}
	for _, tc := range []struct {
		name  string
		rates []Rate
		ready bool
	}{
		{"video unavailable", []Rate{price("video", "video_second", "video")}, false},
		{"unknown operation", []Rate{price("future", "token", "input")}, false},
		{"operation unit mismatch", []Rate{price("speech", "token", "input")}, false},
		{"embedding", []Rate{price("embedding", "token", "input")}, true},
		{"distinct operations", []Rate{price("image_generation", "image", "images"), price("image_edit", "image", "images")}, true},
		{"duplicate operation", []Rate{price("embedding", "token", "input"), price("embedding", "token", "input")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := creditSaleReadiness(p, tc.rates); got.Ready != tc.ready {
				t.Fatalf("readiness=%+v", got)
			}
		})
	}
	p.Usage.Enabled = false
	if creditSaleReadiness(p, []Rate{price("embedding", "token", "input")}).Ready {
		t.Fatal("disabled usage accepted")
	}
	p.Usage.Enabled = true
	p.Usage.FundingModes = []string{FundingPrepaidCash}
	if creditSaleReadiness(p, []Rate{price("embedding", "token", "input")}).Ready {
		t.Fatal("cash-only policy accepted")
	}
}

func TestCreditSaleReadFailureFailsClosed(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	if _, e := f.s.pool.Exec(ctx, `UPDATE billing_rates SET snapshot='[]'::jsonb WHERE id='fixture-sale-readiness'`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Purchase(ctx, f.actor, f.account, PurchaseRequest{OfferID: "sandbox-sw-month-1", Key: "bad-catalog-read"}); e == nil {
		t.Fatal("bad catalog read admitted sale")
	}
	if _, e := f.s.CatalogView(ctx); e == nil {
		t.Fatal("bad catalog read advertised readiness")
	}
	for _, table := range []string{"billing_orders", "billing_commands", "billing_batches"} {
		var n int
		if e := f.s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE account_id=$1", f.account).Scan(&n); e != nil || n != 0 {
			t.Fatalf("%s=%d %v", table, n, e)
		}
	}
}
