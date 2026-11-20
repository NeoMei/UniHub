package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"unihub/internal/ledger"
	"unihub/internal/payments"
	"unihub/internal/wallet"
)

type unavailableUpgradeFundingLedger struct {
	ledger.Ledger
	checkoutCash string
	attempts     int
}

func (l *unavailableUpgradeFundingLedger) Post(ctx context.Context, command ledger.Command) (ledger.Transaction, error) {
	for _, posting := range command.Postings {
		if posting.Asset == CashAsset && posting.Source == l.checkoutCash && posting.Destination == "commercial:revenue:superwagie" {
			l.attempts++
			return ledger.Transaction{}, ledger.ErrInsufficientFunds
		}
	}
	return l.Ledger.Post(ctx, command)
}

func TestLegacyNetExternalInitialFundingFailureCanRefundOriginalPayment(t *testing.T) {
	ctx := t.Context()
	f, original, previousBatch, addonBatch, next, cutover := legacyNetUpgradeFixture(t)
	f.s.now = func() time.Time { return cutover }
	merchant := &checkoutProvider{}
	f.s.ConfigurePayments([]payments.Provider{merchant})
	quote, err := f.s.QuoteUpgradeWithLegacyNet(ctx, f.actor, f.account, original.SubscriptionID, next.ID, true)
	require.NoError(t, err)
	require.Equal(t, "5000000", quote.Amount)
	checkout, err := f.s.CreateCheckout(ctx, f.actor, f.account, CheckoutRequest{QuoteID: quote.ID, Provider: "wechat", Key: "funding-failure"})
	require.NoError(t, err)
	merchant.result = payments.Result{
		OrderID: checkout.ID, TransactionID: "fixture-funding-failure-payment", Amount: quote.Amount,
		Currency: "CNY", Status: "PAID", PaidAt: cutover,
	}
	// Reject the first atomic fulfillment posting without recording it.
	outer := &unavailableUpgradeFundingLedger{Ledger: f.l, checkoutCash: externalReceived(checkout.ID)}
	f.s.ledger = outer
	checkout, err = f.s.ReconcileCheckout(ctx, f.actor, f.account, checkout.ID)
	require.NoError(t, err)
	require.Equal(t, "REVIEW", checkout.PaymentStatus)
	require.Equal(t, "REVIEW", checkout.FulfillmentStatus)
	require.Equal(t, "upgrade_funding_unavailable", checkout.ErrorCode)
	require.Nil(t, checkout.Session)
	require.Equal(t, 1, outer.attempts)
	var status, commandID string
	err = f.s.pool.QueryRow(ctx, `SELECT status,id FROM billing_commands WHERE account_id=$1 AND kind='fulfillment' AND request_key=$2`, f.account, checkout.OrderID).Scan(&status, &commandID)
	require.NoError(t, err)
	require.Equal(t, "REJECTED", status)
	posted, err := f.l.Find(ctx, "unihub:billing:"+commandID)
	require.NoError(t, err)
	require.Nil(t, posted)
	cashBalance(t, f, outer.checkoutCash, 5_000_000)

	for range 2 {
		checkout, err = f.s.RefundCheckout(ctx, f.actor, f.account, checkout.ID, "refund-funding-failure", "upgrade not fulfilled")
		require.NoError(t, err)
		require.Equal(t, "SUCCEEDED", checkout.RefundStatus)
	}
	require.Equal(t, 1, merchant.refunds)
	require.Equal(t, quote.Amount, merchant.refund.Amount)
	require.Equal(t, merchant.result.TransactionID, merchant.refund.TransactionID)
	require.Equal(t, 1, outer.attempts)
	cashBalance(t, f, outer.checkoutCash, 0)
	cashBalance(t, f, "commercial:external:refunded:"+checkout.ID, 5_000_000)
	cashBalance(t, f, "commercial:revenue:superwagie", 3_000_000)
	cashBalance(t, f, wallet.Address(f.account, ""), 20_001_000)
	oldOrder, err := f.s.GetOrder(ctx, f.actor, f.account, original.ID)
	require.NoError(t, err)
	require.Equal(t, original, oldOrder)
	subs, err := f.s.Subscriptions(ctx, f.actor, f.account)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Equal(t, original.SubscriptionID, subs[0].ID)
	require.Equal(t, "active", subs[0].Status)
	creditWallet, err := f.s.ProductCreditWallet(ctx, f.actor, f.account, "superwagie")
	require.NoError(t, err)
	require.ElementsMatch(t, []Batch{previousBatch, addonBatch}, creditWallet.Batches)
}

type interruptedWalletUpgradeLedger struct {
	ledger.Ledger
	walletAddress, batchAddress string
	paid                        bool
}

func (l *interruptedWalletUpgradeLedger) Post(ctx context.Context, command ledger.Command) (ledger.Transaction, error) {
	result, err := l.Ledger.Post(ctx, command)
	if err == nil {
		for _, posting := range command.Postings {
			if posting.Asset == CashAsset && posting.Source == l.walletAddress {
				l.paid = true
			}
		}
	}
	return result, err
}

func (l *interruptedWalletUpgradeLedger) Balance(ctx context.Context, address, asset string) (int64, error) {
	if l.paid && address == l.batchAddress && asset == CreditAsset {
		return 0, ledger.ErrUnknown
	}
	return l.Ledger.Balance(ctx, address, asset)
}

func TestLegacyNetWalletUpgradeRecoveredAfterNewTermCanRefundNet(t *testing.T) {
	ctx := t.Context()
	f, original, oldBatch, _, next, cutover := legacyNetUpgradeFixture(t)
	current := cutover
	f.s.now = func() time.Time { return current }
	quote, err := f.s.QuoteUpgradeWithLegacyNet(ctx, f.actor, f.account, original.SubscriptionID, next.ID, false)
	require.NoError(t, err)
	walletAddress := wallet.Address(f.account, "")
	f.s.ledger = &interruptedWalletUpgradeLedger{
		Ledger: f.l, walletAddress: walletAddress, batchAddress: BatchAddress(oldBatch.ID, "available"),
	}
	paid, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, "interrupted-upgrade")
	require.ErrorIs(t, err, ledger.ErrUnknown)
	require.Equal(t, "PAID", paid.PaymentStatus)
	require.Equal(t, "PENDING", paid.FulfillmentStatus)
	cashBalance(t, f, walletAddress, 15_001_000)

	f.s.ledger = f.l
	current = next.monthAnchor(cutover, next.membershipMonths())
	_, err = f.s.Recover(ctx)
	require.NoError(t, err)
	review, err := f.s.GetOrder(ctx, f.actor, f.account, paid.ID)
	require.NoError(t, err)
	require.Equal(t, "REVIEW", review.FulfillmentStatus)
	refunded, err := f.s.RequestRefund(ctx, f.actor, f.account, paid.ID, "refund-interrupted-upgrade")
	require.NoError(t, err)
	require.Equal(t, "REFUNDED", refunded.RefundStatus)
	cashBalance(t, f, walletAddress, 20_001_000)
	cashBalance(t, f, "commercial:revenue:superwagie", 3_000_000)
	cashBalance(t, f, productComputePrepay(paid), 0)
	preserved, err := f.s.GetOrder(ctx, f.actor, f.account, original.ID)
	require.NoError(t, err)
	require.Equal(t, original, preserved)
}
