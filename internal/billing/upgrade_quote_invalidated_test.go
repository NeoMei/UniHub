package billing

import (
	"context"
	"errors"
	"testing"
	"time"
	"unihub/internal/wallet"
)

func TestInvalidatedUpgradeQuoteDoesNotCreateOrderOrDebit(t *testing.T) {
	for _, reason := range []string{"expired", "period-start", "subscription-version"} {
		t.Run(reason, func(t *testing.T) {
			f := setup(t)
			f.auth(t)
			ctx := context.Background()
			membership := f.buy(t, "sandbox-sw-month-1", "membership")
			quote, err := f.s.QuoteUpgrade(ctx, f.actor, f.account, membership.SubscriptionID, "sandbox-sw-month-2")
			if err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "expired":
				f.s.now = func() time.Time { return quote.ExpiresAt }
			case "period-start":
				// Represent a persisted quote from a different billing period.
				quote.PeriodStart = quote.PeriodStart.AddDate(0, -1, 0)
				if _, err := f.s.pool.Exec(ctx, `UPDATE billing_quotes SET snapshot=$2 WHERE id=$1`, quote.ID, encode(quote)); err != nil {
					t.Fatal(err)
				}
			case "subscription-version":
				otherQuote, err := f.s.QuoteUpgrade(ctx, f.actor, f.account, membership.SubscriptionID, "sandbox-sw-month-2")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.s.AcceptUpgrade(ctx, f.actor, f.account, otherQuote.ID, "other-upgrade"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
			if err != nil {
				t.Fatal(err)
			}
			ordersBefore, err := f.s.Orders(ctx, f.actor, f.account)
			if err != nil {
				t.Fatal(err)
			}
			order, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, "invalidated-upgrade")
			if err == nil || err.Error() != "billing: quote_invalidated" || order.ID != "" {
				t.Fatalf("stale quote not identified as pre-payment rejection: order=%+v err=%v", order, err)
			}
			after, err := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
			if err != nil || after != before {
				t.Fatalf("stale quote debited wallet: before=%d after=%d err=%v", before, after, err)
			}
			ordersAfter, err := f.s.Orders(ctx, f.actor, f.account)
			if err != nil || len(ordersAfter) != len(ordersBefore) {
				t.Fatalf("stale quote persisted an order: before=%d after=%d err=%v", len(ordersBefore), len(ordersAfter), err)
			}
		})
	}
}

func TestConcurrentUpgradeQuoteAcceptInvalidatesOnlyUnsubmittedKey(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	membership := f.buy(t, "sandbox-sw-month-1", "membership")
	quote, err := f.s.QuoteUpgrade(ctx, f.actor, f.account, membership.SubscriptionID, "sandbox-sw-month-2")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		order Order
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, key := range []string{"upgrade-a", "upgrade-b"} {
		go func(key string) {
			<-start
			order, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, key)
			results <- result{order, err}
		}(key)
	}
	close(start)
	accepted, invalidated := 0, 0
	for range 2 {
		out := <-results
		switch {
		case out.err == nil && out.order.PaymentStatus == "PAID" && out.order.FulfillmentStatus == "FULFILLED":
			accepted++
		case out.err != nil && out.err.Error() == "billing: quote_invalidated" && out.order.ID == "":
			invalidated++
		default:
			t.Fatalf("unexpected concurrent result: %+v %v", out.order, out.err)
		}
	}
	if accepted != 1 || invalidated != 1 {
		t.Fatalf("accepted=%d invalidated=%d", accepted, invalidated)
	}
	balance, err := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	if err != nil || balance != 998000000 {
		t.Fatalf("concurrent upgrade debited more than once: balance=%d err=%v", balance, err)
	}
	orders, err := f.s.Orders(ctx, f.actor, f.account)
	if err != nil || len(orders) != 2 {
		t.Fatalf("concurrent upgrade created extra orders: %d %v", len(orders), err)
	}
}

func TestSubmittedUpgradeKeyRecoversAfterQuoteExpiry(t *testing.T) {
	f := setup(t)
	f.auth(t)
	ctx := context.Background()
	membership := f.buy(t, "sandbox-sw-month-1", "membership")
	quote, err := f.s.QuoteUpgrade(ctx, f.actor, f.account, membership.SubscriptionID, "sandbox-sw-month-2")
	if err != nil {
		t.Fatal(err)
	}
	f.s.ledger = &responseLoss{Ledger: f.l, remaining: 1}
	pending, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, "submitted-upgrade")
	if !errors.Is(err, ErrPending) || pending.ID == "" || pending.PaymentStatus != "PENDING" {
		t.Fatalf("expected uncertain submitted payment: %+v %v", pending, err)
	}
	f.s.now = func() time.Time { return quote.ExpiresAt }
	f.s.ledger = f.l
	recovered, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, "submitted-upgrade")
	if err != nil || recovered.ID != pending.ID || recovered.PaymentStatus != "PAID" || recovered.FulfillmentStatus != "FULFILLED" {
		t.Fatalf("expired quote prevented original-key recovery: %+v %v", recovered, err)
	}
	replayed, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, "submitted-upgrade")
	if err != nil || replayed.ID != pending.ID {
		t.Fatalf("completed upgrade replay failed: %+v %v", replayed, err)
	}
	if _, err = f.s.AcceptUpgrade(ctx, f.actor, f.account, "different-quote", "submitted-upgrade"); !errors.Is(err, ErrConflict) || errors.Is(err, ErrQuoteInvalidated) {
		t.Fatalf("existing payment key conflict became safe to replace: %v", err)
	}
	balance, err := f.l.Balance(ctx, wallet.Address(f.account, ""), wallet.Asset)
	if err != nil || balance != 998000000 {
		t.Fatalf("original-key recovery repeated debit: balance=%d err=%v", balance, err)
	}
}
