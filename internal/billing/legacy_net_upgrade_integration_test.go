package billing

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/wallet"
)

// Ledger postings commit atomically. An incoming posting can fund a later
// outgoing posting from the same holding account within one transaction.
type legacyNetTestLedger struct {
	*rateTestLedger
	mu sync.Mutex
}

func (l *legacyNetTestLedger) Post(_ context.Context, command ledger.Command) (ledger.Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.transactions[command.Reference]; ok {
		return existing, nil
	}
	balances := maps.Clone(l.balances)
	for _, posting := range command.Postings {
		if posting.Source != "world" {
			balances[posting.Source+"/"+posting.Asset] -= posting.Amount
		}
		balances[posting.Destination+"/"+posting.Asset] += posting.Amount
	}
	for _, balance := range balances {
		if balance < 0 {
			return ledger.Transaction{}, ledger.ErrInsufficientFunds
		}
	}
	tx := ledger.Transaction{ID: command.Reference, Reference: command.Reference, Postings: command.Postings}
	l.balances = balances
	l.transactions[command.Reference] = tx
	return tx, nil
}

func (l *legacyNetTestLedger) Find(ctx context.Context, reference string) (*ledger.Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rateTestLedger.Find(ctx, reference)
}

func (l *legacyNetTestLedger) Balance(ctx context.Context, address, asset string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rateTestLedger.Balance(ctx, address, asset)
}

func legacyNetUpgradeFixture(t *testing.T) (fixture, Order, Batch, Batch, Offer, time.Time) {
	t.Helper()
	ctx := t.Context()
	f, _ := nativeFixture(t)
	if memory, ok := f.l.(*rateTestLedger); ok {
		atomic := &legacyNetTestLedger{rateTestLedger: memory}
		f.l, f.s.ledger = atomic, atomic
	}
	start := dt("2030-04-01T00:00:00Z")
	f.s.now = func() time.Time { return start }
	fundCash(t, f, "superwagie", 3_000_000)
	fundCash(t, f, "", 20_000_000)

	profile, err := f.s.Profile(ctx, "superwagie")
	require.NoError(t, err)
	profile.Subscription.Enabled = true
	profile.IncludedCredits.Enabled = true
	profile.Addons.Enabled = true
	profile.Usage.Models = nil
	profile.Usage.FundingModes = []string{FundingCredits}
	profile.Usage.DefaultFundingMode = FundingCredits
	_, err = f.s.SaveProfile(ctx, f.actor, "superwagie", profile)
	require.NoError(t, err)
	rate := cashRate("legacy-upgrade-credit")
	rate.Asset = CreditAsset
	_, err = f.s.PublishRate(ctx, f.actor, rate)
	require.NoError(t, err)

	for _, offer := range []Offer{
		{ID: "legacy-upgrade-standard", ProductID: "superwagie", Name: "Fixture Standard", AccountKind: "personal", Kind: "membership",
			Period: "month", Price: "2000000", MonthlyCredit: "10000000", Rank: 1, UpgradeRule: "remaining_term_prorata_v1", RefundRule: "unused_only_v1"},
		{ID: "legacy-upgrade-addon", ProductID: "superwagie", Name: "Fixture Add-on", AccountKind: "personal", Kind: "addon",
			Price: "1000000", MonthlyCredit: "5000000", ValidityMonths: 12, RefundRule: "unused_only_v1"},
	} {
		_, err = f.s.SaveOffer(ctx, f.actor, offer)
		require.NoError(t, err)
		_, err = f.s.PublishOffer(ctx, f.actor, offer.ID)
		require.NoError(t, err)
	}
	original := f.buy(t, "legacy-upgrade-standard", "original-member")
	addon := f.buy(t, "legacy-upgrade-addon", "original-addon")
	require.Equal(t, "2000000", original.Amount)
	require.Equal(t, "wallet", original.PaymentSource)
	require.Equal(t, "2000000", original.ProductFunding)
	require.Empty(t, original.CreditScope)
	require.True(t, original.End.Equal(dt("2030-05-01T00:00:00Z")))

	base, _ := publishProductCreditOffers(t, f, "month")
	base.Rank = original.Offer.Rank
	next := publishUpgradeOffer(t, f, base)
	require.Equal(t, "6000000", next.Price)
	batches, err := f.s.Batches(ctx, f.actor, f.account)
	require.NoError(t, err)
	require.Len(t, batches, 2)
	request := LegacyCreditAdoptionRequest{ActorID: f.actor, AccountID: f.account, ProductID: "superwagie"}
	for _, batch := range batches {
		request.BatchIDs = append(request.BatchIDs, batch.ID)
	}
	plan, err := f.s.PreviewLegacyCreditAdoption(ctx, request)
	require.NoError(t, err)
	require.True(t, plan.CanApply, "%+v", plan.Blockers)
	applied, err := f.s.ApplyLegacyCreditAdoption(ctx, request, plan.Digest)
	require.NoError(t, err)
	var previousBatch, addonBatch Batch
	for _, batch := range applied.Current {
		switch batch.OrderID {
		case original.ID:
			previousBatch = batch
		case addon.ID:
			addonBatch = batch
		}
	}
	require.NotEmpty(t, previousBatch.ID)
	require.NotEmpty(t, addonBatch.ID)
	require.Equal(t, "10000000", previousBatch.Available)
	require.Equal(t, "5000000", addonBatch.Available)
	require.Equal(t, ProductCreditScopeV1, previousBatch.CreditScope)
	require.Equal(t, ProductCreditScopeV1, addonBatch.CreditScope)
	cashBalance(t, f, "commercial:revenue:superwagie", 3_000_000)
	cashBalance(t, f, wallet.Address(f.account, "superwagie"), 0)
	cashBalance(t, f, wallet.Address(f.account, ""), 20_001_000)
	return f, original, previousBatch, addonBatch, next, dt("2030-04-16T00:00:00Z")
}

func TestLegacyNetUpgradeCollectsOneProratedPaymentAndResetsTerm(t *testing.T) {
	for _, source := range []string{"wallet", "external"} {
		t.Run(source, func(t *testing.T) {
			ctx := t.Context()
			f, original, previousBatch, addonBatch, next, cutover := legacyNetUpgradeFixture(t)
			f.s.now = func() time.Time { return cutover }

			// Legacy payment evidence survives the cutover, with its cash still
			// held outside the active product revenue account.
			const revenue = "commercial:revenue:superwagie"
			const retainedCash = "fixture:retained:legacy-revenue"
			_, err := f.l.Post(ctx, ledger.Command{
				Reference: "fixture:retain-legacy-revenue",
				Postings:  []ledger.Posting{{Source: revenue, Destination: retainedCash, Asset: CashAsset, Amount: 3_000_000}},
			})
			require.NoError(t, err)
			cashBalance(t, f, revenue, 0)

			quote, err := f.s.QuoteUpgradeWithLegacyNet(ctx, f.actor, f.account, original.SubscriptionID, next.ID, source == "external")
			require.NoError(t, err)
			require.NotNil(t, quote.ProductCreditUpgrade)
			require.Equal(t, original.ID, quote.ProductCreditUpgrade.PreviousOrderID)
			require.Equal(t, "1000000", quote.ProductCreditUpgrade.AppliedCreditAmount)
			require.Equal(t, "0", quote.ProductCreditUpgrade.EstimatedRefundAmount)
			require.Equal(t, "5000000", quote.Amount)
			require.Equal(t, next.MonthlyCredit, quote.GrantAmount)

			merchant := &checkoutProvider{}
			f.s.ConfigurePayments([]payments.Provider{merchant})
			var upgraded Order
			if source == "wallet" {
				type result struct {
					order Order
					err   error
				}
				results := make(chan result, 2)
				for range 2 {
					go func() {
						out, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, "same-upgrade")
						results <- result{out, err}
					}()
				}
				first, replay := <-results, <-results
				require.NoError(t, first.err)
				require.NoError(t, replay.err)
				upgraded = first.order
				require.Equal(t, upgraded.ID, replay.order.ID)
				_, err = f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, "second-upgrade")
				require.Error(t, err, "an already-consumed quote must not produce another payment")
				cashBalance(t, f, wallet.Address(f.account, ""), 15_001_000)
			} else {
				request := CheckoutRequest{QuoteID: quote.ID, Provider: "wechat", Key: "same-upgrade"}
				type result struct {
					checkout Checkout
					err      error
				}
				results := make(chan result, 2)
				for range 2 {
					go func() {
						out, err := f.s.CreateCheckout(ctx, f.actor, f.account, request)
						results <- result{out, err}
					}()
				}
				first, replay := <-results, <-results
				require.NoError(t, first.err)
				require.NoError(t, replay.err)
				checkout := first.checkout
				require.Equal(t, checkout.ID, replay.checkout.ID)
				require.Len(t, merchant.requests, 1)
				require.Equal(t, "5000000", merchant.requests[0].Amount)
				merchant.result = payments.Result{
					OrderID: checkout.ID, TransactionID: "fixture-net-payment", Amount: "5000000",
					Currency: "CNY", Status: "PAID", PaidAt: cutover,
				}
				for range 2 {
					checkout, err = f.s.ReconcileCheckout(ctx, f.actor, f.account, checkout.ID)
					require.NoError(t, err)
					require.Equal(t, "FULFILLED", checkout.FulfillmentStatus)
				}
				var receipts int
				err = f.s.pool.QueryRow(ctx, "SELECT count(*) FROM billing_payment_receipts WHERE checkout_id=$1", checkout.ID).Scan(&receipts)
				require.NoError(t, err)
				require.Equal(t, 1, receipts)
				upgraded, err = f.s.GetOrder(ctx, f.actor, f.account, checkout.OrderID)
				require.NoError(t, err)
				cashBalance(t, f, externalReceived(checkout.ID), 0)
				cashBalance(t, f, wallet.Address(f.account, ""), 20_001_000)
			}

			require.Equal(t, "PAID", upgraded.PaymentStatus)
			require.Equal(t, "FULFILLED", upgraded.FulfillmentStatus)
			require.Equal(t, "5000000", upgraded.Amount)
			require.Equal(t, source, upgraded.PaymentSource)
			require.NotEqual(t, original.SubscriptionID, upgraded.SubscriptionID)
			require.True(t, upgraded.Start.Equal(cutover))
			require.True(t, upgraded.End.Equal(dt("2030-05-16T00:00:00Z")))
			cashBalance(t, f, wallet.Address(f.account, "superwagie"), 0)
			cashBalance(t, f, revenue, 2_000_000)
			cashBalance(t, f, retainedCash, 3_000_000)
			cashBalance(t, f, productComputePrepay(upgraded), 3_000_000)

			subs, err := f.s.Subscriptions(ctx, f.actor, f.account)
			require.NoError(t, err)
			require.Len(t, subs, 1)
			require.Equal(t, upgraded.SubscriptionID, subs[0].ID)
			require.Equal(t, next.ID, subs[0].Offer.ID)
			require.Equal(t, "active", subs[0].Status)
			require.True(t, subs[0].Anchor.Equal(cutover))
			require.True(t, subs[0].PaidThrough.Equal(upgraded.End))
			creditWallet, err := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
			require.NoError(t, err)
			require.Equal(t, "305000000", creditWallet.Available)
			require.Len(t, creditWallet.Batches, 3)
			for _, batch := range creditWallet.Batches {
				switch batch.OrderID {
				case original.ID:
					require.Equal(t, previousBatch.ID, batch.ID)
					require.Equal(t, "0", batch.Available)
					require.Equal(t, previousBatch.Available, batch.Withdrawn)
					require.True(t, batch.Frozen)
				case addonBatch.OrderID:
					require.Equal(t, addonBatch, batch)
				case upgraded.ID:
					require.Equal(t, next.MonthlyCredit, batch.Available)
					require.True(t, batch.StartsAt.Equal(cutover))
					require.True(t, batch.ExpiresAt.Equal(upgraded.End))
				default:
					t.Fatalf("unexpected batch %s", batch.ID)
				}
			}
			old, err := f.s.GetOrder(ctx, f.actor, f.account, original.ID)
			require.NoError(t, err)
			require.Equal(t, original.Amount, old.Amount)
			require.Equal(t, original.PaymentSource, old.PaymentSource)
			require.Equal(t, original.ProductFunding, old.ProductFunding)
			require.Equal(t, original.Offer, old.Offer)
			require.NotNil(t, old.UpgradeReturn)
			require.Equal(t, upgraded.ID, old.UpgradeReturn.ReplacementOrderID)
			require.Equal(t, "1000000", old.UpgradeReturn.AppliedCreditAmount)
			require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
			require.Zero(t, merchant.refunds)
			_, err = f.s.RequestRefund(ctx, f.actor, f.account, original.ID, "refund-original-after-upgrade")
			require.Error(t, err, "the old order must not fund both an upgrade and a separate refund")

			higher := next
			higher.ID, higher.Name, higher.Rank = "next-upgrade-tier", "Fixture Highest", next.Rank+1
			higher.Price, higher.ServiceAmount = "8000000", "5000000"
			_, err = f.s.SaveOffer(ctx, f.actor, higher)
			require.NoError(t, err)
			_, err = f.s.PublishOffer(ctx, f.actor, higher.ID)
			require.NoError(t, err)
			futureQuote, err := f.s.QuoteUpgrade(ctx, f.actor, f.account, upgraded.SubscriptionID, higher.ID)
			require.NoError(t, err)
			require.NotNil(t, futureQuote.ProductCreditUpgrade)
			require.Equal(t, upgraded.Amount, futureQuote.ProductCreditUpgrade.EstimatedRefundAmount)
			future, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, futureQuote.ID, "future-scoped-upgrade")
			require.NoError(t, err)
			require.Equal(t, "8000000", future.Amount)
			for range 2 {
				require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
			}
			returned, err := f.s.GetOrder(ctx, f.actor, f.account, upgraded.ID)
			require.NoError(t, err)
			require.Equal(t, "UPGRADE_RETURNED", returned.RefundStatus)
			require.NotNil(t, returned.UpgradeReturn)
			require.Equal(t, upgraded.Amount, returned.UpgradeReturn.Amount)
			if source == "external" {
				require.Equal(t, 1, merchant.refunds)
				require.Equal(t, upgraded.Amount, merchant.refund.Amount)
			} else {
				require.Zero(t, merchant.refunds)
			}
			cashBalance(t, f, wallet.Address(f.account, ""), 12_001_000)
			cashBalance(t, f, retainedCash, 3_000_000)
		})
	}
}
