package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"unihub/internal/ledger"
	"unihub/internal/wallet"
)

func TestRuntimeCashChargesSharedWalletUsingProductCalculatedAmount(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	_, err := s.ledger.Post(ctx, ledger.Command{Reference: "cash-fixture", Postings: []ledger.Posting{
		{Source: "world", Destination: wallet.Address(b.PersonalAccountID, ""), Asset: wallet.Asset, Amount: 1000},
		{Source: "world", Destination: wallet.Address(b.PersonalAccountID, "llmrouter"), Asset: wallet.Asset, Amount: 400},
		{Source: "world", Destination: RuntimeAddress(scope, b.PersonalAccountID, "available"), Asset: RuntimeAsset, Amount: 100},
	}})
	if err != nil {
		t.Fatal(err)
	}
	in := RuntimeReserveRequest{CallID: "cash-call", LocalUserID: "42", LocalKeyID: "original-sk-key", AccountID: b.PersonalAccountID, RequestDigest: "request", Model: "product-priced-model", Operation: "product-operation", Asset: wallet.Asset, UnitVersion: "lr-cny-v1", PriceVersion: "lr-price-v1", PricingDigest: "frozen-product-price-and-conversion", Amount: "60"}
	for range 2 {
		if _, err = s.RuntimeReserve(ctx, scope, in); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err = s.RuntimeTopUp(ctx, scope, in.CallID, RuntimeTopUpRequest{RequestKey: "extension", Amount: "20"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err = s.RuntimeSettle(ctx, scope, in.CallID, RuntimeSettlementRequest{Amount: "37", PricingDigest: in.PricingDigest, Usage: []byte(`{"product_quota":3}`)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ledger.Balance(ctx, wallet.Address(b.PersonalAccountID, ""), wallet.Asset)
	if err != nil || got != 963 {
		t.Fatalf("shared cash=%d err=%v", got, err)
	}
	for _, check := range []struct {
		address, asset string
		want           int64
	}{
		{wallet.Address(b.PersonalAccountID, "llmrouter"), wallet.Asset, 400},
		{RuntimeAddress(scope, b.PersonalAccountID, "available"), RuntimeAsset, 100},
	} {
		got, err = s.ledger.Balance(ctx, check.address, check.asset)
		if err != nil || got != check.want {
			t.Fatalf("unrelated asset changed: got=%d want=%d err=%v", got, check.want, err)
		}
	}
	for range 2 {
		if _, err = s.RuntimeRefund(ctx, scope, in.CallID, RuntimeRefundRequest{RequestKey: "partial", Amount: "7", Reason: "product refund"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.RuntimeRefund(ctx, scope, in.CallID, RuntimeRefundRequest{RequestKey: "remainder", Amount: "30", Reason: "full refund"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RuntimeRefund(ctx, scope, in.CallID, RuntimeRefundRequest{RequestKey: "over", Amount: "1", Reason: "over refund"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("over refund: %v", err)
	}
	got, err = s.ledger.Balance(ctx, wallet.Address(b.PersonalAccountID, ""), wallet.Asset)
	if err != nil || got != 1000 {
		t.Fatalf("full cash refund=%d err=%v", got, err)
	}
}

func TestRuntimeVerifiedCnyPaymentCreditsSharedCashOnce(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	payload := json.RawMessage(`{"order_id":1,"trade_no":"paid-order","provider":"wechat_pay","provider_trade_no":"verified-provider-payment","currency":"CNY","paid_amount_minor":123}`)
	digest := sha256.Sum256(payload)
	in := RuntimeAwardRequest{ReceiptID: "payment-receipt", SourceKind: "payment", ReceiptDigest: hex.EncodeToString(digest[:]), Reason: "verified payment", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "1230000", Asset: wallet.Asset, UnitVersion: "lr-cny-v1", SourcePayload: payload}
	for range 2 {
		if _, err := s.RuntimeCashPayment(ctx, scope, in); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ledger.Balance(ctx, wallet.Address(b.PersonalAccountID, ""), wallet.Asset)
	if err != nil || got != 1230000 {
		t.Fatalf("cash receipt balance=%d err=%v", got, err)
	}
	in.ReceiptID = "forged-amount"
	in.Amount = "1230001"
	if _, err = s.RuntimeCashPayment(ctx, scope, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("receipt amount mismatch: %v", err)
	}
}

func TestRuntimeCashCannotSpendLegacyQuotaOrMintCashAwards(t *testing.T) {
	s, scope, b := runtimeFixture(t)
	ctx := context.Background()
	_, err := s.ledger.Post(ctx, ledger.Command{Reference: "quota-fixture", Postings: []ledger.Posting{{Source: "world", Destination: RuntimeAddress(scope, b.PersonalAccountID, "available"), Asset: RuntimeAsset, Amount: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	in := RuntimeReserveRequest{CallID: "cash-insufficient", LocalUserID: "42", AccountID: b.PersonalAccountID, RequestDigest: "request", Model: "m", Operation: "chat", Asset: wallet.Asset, UnitVersion: "lr-cny-v1", PriceVersion: "v", PricingDigest: "p", Amount: "1"}
	if _, err = s.RuntimeReserve(ctx, scope, in); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("cash reserve must reject empty cash despite legacy quota: %v", err)
	}
	if _, err = s.RuntimeAward(ctx, scope, RuntimeAwardRequest{ReceiptID: "signup", SourceKind: "signup", ReceiptDigest: "digest", Reason: "signup", LocalUserID: "42", AccountID: b.PersonalAccountID, Amount: "100", Asset: wallet.Asset, UnitVersion: "lr-cny-v1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("award minted cash: %v", err)
	}
}
