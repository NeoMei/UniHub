package metering

import "testing"

func TestRerankInputTokenPriceContract(t *testing.T) {
	term := Term{Key: "input", Unit: "token", Price: Rational{Numerator: "1", Denominator: "2"}}
	price := Price{Version: "metered_v2", Operation: "rerank", Policy: "provider_reported_v1", Terms: []Term{term}}
	if err := ValidatePrice(price); err != nil {
		t.Fatalf("valid rerank price: %v", err)
	}
	for _, tc := range []struct {
		name  string
		terms []Term
	}{
		{"none", nil},
		{"output", []Term{{Key: "output", Unit: "token", Price: term.Price}}},
		{"cache", []Term{{Key: "cache_read", Unit: "token", Price: term.Price}}},
		{"visual", []Term{{Key: "visual", Unit: "token", Price: term.Price}}},
		{"request", []Term{{Key: "requests", Unit: "request", Price: term.Price}}},
		{"duplicate", []Term{term, term}},
		{"multiple", []Term{term, {Key: "output", Unit: "token", Price: term.Price}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := price
			p.Terms = tc.terms
			if err := ValidatePrice(p); err == nil {
				t.Fatal("invalid rerank price accepted")
			}
		})
	}
	usage := Usage{Version: "metered_v2", Operation: "rerank", State: "confirmed", Measurements: []Measurement{{Key: "input", Unit: "token", Quantity: Quantity{Value: "6"}}}}
	got, err := Calculate(price, usage)
	if err != nil || got.RatString() != "3" {
		t.Fatalf("calculate: %v %v", got, err)
	}
	usage.Measurements[0].Quantity.Scale = 1
	if err := ValidateUsage(price, usage); err == nil {
		t.Fatal("fractional token accepted")
	}
	usage.Measurements[0].Quantity.Scale = 0
	usage.Measurements[0].Key = "output"
	if err := ValidateUsage(price, usage); err == nil {
		t.Fatal("wrong usage dimension accepted")
	}
}
