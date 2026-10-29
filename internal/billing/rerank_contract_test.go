package billing

import (
	"testing"
	"unihub/internal/metering"
)

func TestRerankCashInputTokenCharge(t *testing.T) {
	price := metering.Price{Version: "metered_v2", Operation: "rerank", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "2"}}}}
	rate := Rate{Asset: CashAsset, Semantics: "metered_v2", Metered: &price}
	for _, tc := range []struct{ quantity, want string }{{"6", "3"}, {"3", "2"}, {"0", "0"}} {
		u := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "rerank", State: "confirmed", Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: tc.quantity}}}}}
		got, err := CalculateCharge(rate, u)
		if err != nil || got != tc.want {
			t.Fatalf("quantity %s: charge %s, error %v", tc.quantity, got, err)
		}
	}
}
