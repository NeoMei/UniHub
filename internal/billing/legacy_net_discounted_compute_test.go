package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"unihub/internal/wallet"
)

func TestLegacyNetDiscountedComputeSettlesFullGrantAndRefundsOnlyUnrecognizedCash(t *testing.T) {
	for _, tc := range []struct {
		name               string
		usage              []string
		recognized, refund int64
		remaining          string
	}{
		{"partial grant", []string{"100000000"}, 1_000_000, 1_500_000, "200000000"},
		{"full grant", []string{"250000000", "50000000"}, 2_500_000, 0, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			f, original, _, addon, discounted, cutover := legacyNetUpgradeFixture(t)
			f.s.now = func() time.Time { return cutover }
			discounted.ID, discounted.Name = "higher-discounted", "Higher discounted membership"
			discounted.Price, discounted.ServiceAmount = "3500000", "500000"
			_, err := f.s.SaveOffer(ctx, f.actor, discounted)
			require.NoError(t, err)
			_, err = f.s.PublishOffer(ctx, f.actor, discounted.ID)
			require.NoError(t, err)

			quote, err := f.s.QuoteUpgradeWithLegacyNet(ctx, f.actor, f.account, original.SubscriptionID, discounted.ID, false)
			require.NoError(t, err)
			require.Equal(t, "2500000", quote.Amount)
			require.Equal(t, "1000000", quote.ProductCreditUpgrade.AppliedCreditAmount)
			upgraded, err := f.s.AcceptUpgrade(ctx, f.actor, f.account, quote.ID, "discount-exceeds-service")
			require.NoError(t, err)
			walletCash := wallet.Address(f.account, "")
			cashBalance(t, f, walletCash, 17_501_000)
			require.Equal(t, "2500000", upgraded.ComputeAmount)
			require.Equal(t, "0", upgraded.ServiceAmount)
			require.Equal(t, "3000000", upgraded.Offer.ComputeAmount)
			require.Equal(t, "300000000", upgraded.GrantAmount)

			var credential string
			err = f.s.pool.QueryRow(ctx, `UPDATE model_credentials SET funding_mode='credits',asset=$1,
				pricing_source=$2,credit_scope=$3,expires_at=$4,max_per_call='300000000',max_total='300000000'
				WHERE account_id=$5 RETURNING id`, CreditAsset, NativeCreditPricingSource, ProductCreditScopeV1,
				cutover.Add(time.Hour), f.account).Scan(&credential)
			require.NoError(t, err)
			batches, err := f.s.Batches(ctx, f.actor, f.account)
			require.NoError(t, err)
			var grantID string
			for _, batch := range batches {
				if batch.OrderID == upgraded.ID {
					grantID = batch.ID
				}
			}
			require.NotEmpty(t, grantID)

			for i, quantity := range tc.usage {
				request := creditNativeRequest(f, credential, "discounted-compute-"+str(int64(i)), quantity, exactProductCreditTestConversion())
				request.CreditScope, request.SourceVerified = ProductCreditScopeV1, true
				held, err := f.s.ReserveNative(ctx, "llmrouter", request)
				require.NoError(t, err)
				require.Equal(t, []Allocation{{BatchID: grantID, Amount: quantity}}, held.Allocations)
				settlement := NativeSettlementRequest{CallID: held.CallID, PricingDigest: request.Quote.PricingDigest, NativeQuota: quantity, Amount: quantity}
				for range 2 {
					settled, err := f.s.SettleNative(ctx, "llmrouter", settlement)
					require.NoError(t, err)
					require.Equal(t, "SETTLED", settled.Status)
					require.Equal(t, quantity, settled.Charged)
				}
			}
			available, err := f.l.Balance(ctx, BatchAddress(grantID, "available"), CreditAsset)
			require.NoError(t, err)
			require.Equal(t, tc.remaining, str(available))
			cashBalance(t, f, productComputePrepay(upgraded), tc.refund)
			cashBalance(t, f, "commercial:compute_recognized:superwagie", tc.recognized)

			higher := publishUpgradeOffer(t, f, discounted)
			nextQuote, err := f.s.QuoteUpgrade(ctx, f.actor, f.account, upgraded.SubscriptionID, higher.ID)
			require.NoError(t, err)
			require.Equal(t, str(tc.refund), nextQuote.ProductCreditUpgrade.EstimatedRefundAmount)
			_, err = f.s.AcceptUpgrade(ctx, f.actor, f.account, nextQuote.ID, "replace-discounted-compute")
			require.NoError(t, err)
			require.NoError(t, f.s.RecoverProductUpgradeReturns(ctx))
			old, err := f.s.GetOrder(ctx, f.actor, f.account, upgraded.ID)
			require.NoError(t, err)
			require.NotNil(t, old.UpgradeReturn)
			require.Equal(t, str(tc.refund), old.UpgradeReturn.Amount)
			cashBalance(t, f, walletCash, 11_501_000+tc.refund)
			cashBalance(t, f, productComputePrepay(upgraded), 0)
			cashBalance(t, f, "commercial:compute_recognized:superwagie", tc.recognized)
			addonBalance, err := f.l.Balance(ctx, BatchAddress(addon.ID, "available"), CreditAsset)
			require.NoError(t, err)
			require.Equal(t, addon.Available, str(addonBalance))
		})
	}
}
