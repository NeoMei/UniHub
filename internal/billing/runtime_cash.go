package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"unihub/internal/accounts"
	"unihub/internal/wallet"
)

const RuntimeCashUnitVersion = "lr-cny-v1"

// RuntimeCashPayment is separate from quota awards. Only the trusted payment
// service may submit it, after the product verified the provider callback.
func (s *Service) RuntimeCashPayment(ctx context.Context, scope RuntimeScope, r RuntimeAwardRequest) (RuntimeAdjustment, error) {
	var receipt struct {
		OrderID         int64  `json:"order_id"`
		TradeNo         string `json:"trade_no"`
		Provider        string `json:"provider"`
		ProviderTradeNo string `json:"provider_trade_no"`
		Currency        string `json:"currency"`
		PaidAmountMinor int64  `json:"paid_amount_minor"`
	}
	n, err := runtimeAmount(r.Amount)
	digest := sha256.Sum256(r.SourcePayload)
	if err != nil || n == 0 || r.Asset != wallet.Asset || r.UnitVersion != RuntimeCashUnitVersion || r.SourceKind != "payment" || !runtimeText(r.ReceiptID) || !runtimeText(r.Reason) || len(r.SourcePayload) > 64<<10 || json.Unmarshal(r.SourcePayload, &receipt) != nil || r.ReceiptDigest != hex.EncodeToString(digest[:]) {
		return RuntimeAdjustment{}, ErrInvalid
	}
	if receipt.OrderID <= 0 || !runtimeText(receipt.TradeNo) || !runtimeText(receipt.Provider) || !runtimeText(receipt.ProviderTradeNo) || receipt.Currency != "CNY" || receipt.PaidAmountMinor <= 0 || receipt.PaidAmountMinor > math.MaxInt64/10000 || n != receipt.PaidAmountMinor*10000 {
		return RuntimeAdjustment{}, ErrInvalid
	}
	return s.runtimeAdjustment(ctx, scope, "", r.LocalUserID, r.AccountID, "payment", r.ReceiptID, r.Amount, hash(r), n, nil, "", r, wallet.Asset)
}

func runtimeCallUnits(asset, version string) bool {
	return runtimeUnits(asset, version) || asset == wallet.Asset && version == RuntimeCashUnitVersion
}

// Only call spending uses the shared cash wallet. Legacy grants, imports and
// adjustments remain in their original product asset and cannot issue cash.
func runtimeCallAddress(scope RuntimeScope, account, asset, bucket string) string {
	if asset == wallet.Asset && bucket == "available" {
		return wallet.Address(account, "")
	}
	return RuntimeAddress(scope, account, bucket)
}

func (s *Service) runtimeCallAuthority(ctx context.Context, q accounts.Querier, scope RuntimeScope, local, account, asset string) error {
	b, err := s.runtimeAuthority(ctx, q, scope, local, account)
	if err != nil {
		return err
	}
	// Product-issued keys currently bind the verified personal payer. Team cash
	// spending requires a separate explicit mandate; membership alone is not one.
	if asset == wallet.Asset && b.PersonalAccountID != account {
		return accounts.ErrForbidden
	}
	return nil
}

func (s *Service) RuntimeBalanceForAsset(ctx context.Context, scope RuntimeScope, local, account, asset, version string) (out RuntimeBalance, err error) {
	if runtimeUnits(asset, version) {
		return s.RuntimeBalance(ctx, scope, local, account)
	}
	if !runtimeCallUnits(asset, version) {
		return out, ErrInvalid
	}
	if err = s.runtimeCallAuthority(ctx, s.pool, scope, local, account, asset); err != nil {
		return out, err
	}
	out = RuntimeBalance{RuntimeScope: scope, AccountID: account, Asset: asset, UnitVersion: version}
	values := []*string{&out.Available, &out.Held, &out.Consumed}
	for i, bucket := range []string{"available", "reserved", "consumed"} {
		n, e := s.ledger.Balance(ctx, runtimeCallAddress(scope, account, asset, bucket), asset)
		if e != nil {
			return out, e
		}
		*values[i] = str(n)
	}
	return out, nil
}

// RuntimeFundingAsset is a scoped metadata read for service-policy checks. It
// does not reconcile a command or post any ledger movement before authorization.
func (s *Service) RuntimeFundingAsset(ctx context.Context, scope RuntimeScope, callID string) (string, error) {
	if !validRuntimeScope(scope) {
		return "", accounts.ErrForbidden
	}
	call, err := s.runtimeCallOn(ctx, s.pool, scope, callID)
	return call.Asset, err
}
