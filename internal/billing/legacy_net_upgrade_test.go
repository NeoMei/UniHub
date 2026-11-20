package billing

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestLegacyNetUpgradePrice(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(3 * time.Hour)
	for _, tc := range []struct {
		name, oldPaid, gross string
		elapsed              time.Duration
		wantNet, wantCredit  string
	}{
		{"floors time credit", "100000000", "150000000", time.Hour, "83333334", "66666666"},
		{"permits zero credit", "100000000", "150000000", 3*time.Hour - time.Nanosecond, "150000000", "0"},
		{"uses exact wide arithmetic", str(math.MaxInt64 - 1), str(math.MaxInt64), 0, "1", str(math.MaxInt64 - 1)},
		{"refuses zero net", "100000000", "100000000", 0, "", ""},
		{"refuses zero gross", "100000000", "0", time.Hour, "", ""},
		{"refuses expired term", "100000000", "150000000", 3 * time.Hour, "", ""},
		{"refuses future term", "100000000", "150000000", -time.Nanosecond, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			net, credit, err := legacyNetUpgradePrice(tc.oldPaid, tc.gross, start, end, start.Add(tc.elapsed))
			if tc.wantNet == "" {
				if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrQuoteInvalidated) {
					t.Fatalf("expected quote rejection, got %v", err)
				}
				return
			}
			if err != nil || net != tc.wantNet || credit != tc.wantCredit {
				t.Fatalf("net=%q credit=%q err=%v, want net=%q credit=%q", net, credit, err, tc.wantNet, tc.wantCredit)
			}
		})
	}
}

func TestLegacyNetExternalUpgradeRoundPreservesGross(t *testing.T) {
	q := Quote{
		Offer: Offer{Price: "150000000"}, Amount: "83333334",
		ProductCreditUpgrade: &ProductCreditUpgrade{AppliedCreditAmount: "66666666"},
	}
	q, err := externalUpgradeQuote(q)
	if err != nil || q.Amount != "83340000" || q.OriginalAmount != "83333334" || q.ProductCreditUpgrade.AppliedCreditAmount != "66660000" {
		t.Fatalf("unexpected external quote: %+v, %v", q, err)
	}
	q.Offer.Price = "10001"
	q.Amount = "10001"
	q.ProductCreditUpgrade.AppliedCreditAmount = "0"
	if _, err := externalUpgradeQuote(q); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("rounding above the full price should be rejected, got %v", err)
	}
}
