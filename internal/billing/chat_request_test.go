package billing

import (
	"testing"
	"unihub/internal/metering"
)

func TestChatRequestTariffChargesOnceAndRejectsTokenMeasurements(t *testing.T) {
	r := Rate{ID: "flat", Model: "flat-chat", Asset: CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "chat", Policy: "sum", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "73", Denominator: "1"}}}}}
	u := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "chat", State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1", Scale: 0}}}}}
	if got, err := CalculateCharge(r, u); err != nil || got != "73" {
		t.Fatalf("one request charged %s: %v", got, err)
	}
	u.Input = "1"
	if _, err := CalculateCharge(r, u); err == nil {
		t.Fatal("mixed token/request usage accepted")
	}
	u.Input = ""
	u.Metered.Measurements = append(u.Metered.Measurements, metering.Measurement{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "100"}})
	if _, err := CalculateCharge(r, u); err == nil {
		t.Fatal("unpriced token dimension accepted")
	}
	r.Metered.Terms = append(r.Metered.Terms, metering.Term{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}})
	if err := validateRate(r); err == nil {
		t.Fatal("mixed tariff accepted")
	}
}
