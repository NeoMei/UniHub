package metering

import "testing"

func TestGatewayWireOperationsMeteredContracts(t *testing.T) {
	cases := []struct {
		name, op, policy string
		terms            []Term
		measurements     []Measurement
		want             string
	}{
		{"rerank", "rerank", "provider_reported_v1", []Term{{Key: "input", Unit: "token", Price: Rational{"1", "2"}}}, []Measurement{{Key: "input", Unit: "token", Quantity: Quantity{Value: "6"}}}, "3"},
		{"multimodal embedding", "multimodal_embedding", "provider_reported_v1", []Term{{Key: "text", Unit: "token", Price: Rational{"1", "2"}}, {Key: "visual", Unit: "token", Price: Rational{"2", "1"}}}, []Measurement{{Key: "text", Unit: "token", Quantity: Quantity{Value: "6"}}, {Key: "visual", Unit: "token", Quantity: Quantity{Value: "2"}}}, "7"},
		{"token OCR", "ocr", "provider_tokens_v1", []Term{{Key: "input", Unit: "token", Price: Rational{"1", "2"}}, {Key: "output", Unit: "token", Price: Rational{"1", "1"}}}, []Measurement{{Key: "input", Unit: "token", Quantity: Quantity{Value: "6"}}, {Key: "output", Unit: "token", Quantity: Quantity{Value: "2"}}}, "5"},
		{"elevenlabs speech", "speech", "elevenlabs_character_cost_v1", []Term{{Key: "provider", Unit: "provider_unit", ProviderUnit: "elevenlabs.character_cost.multilingual_v2.v1", Price: Rational{"1", "2"}}}, []Measurement{{Key: "provider", Unit: "provider_unit", ProviderUnit: "elevenlabs.character_cost.multilingual_v2.v1", Quantity: Quantity{Value: "6"}}}, "3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Price{Version: "metered_v2", Operation: tc.op, Policy: tc.policy, Terms: tc.terms}
			u := Usage{Version: "metered_v2", Operation: tc.op, State: "confirmed", Measurements: tc.measurements}
			got, err := Calculate(p, u)
			if err != nil || got.RatString() != tc.want {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestGatewayMeteringRejectsAmbiguousPolicies(t *testing.T) {
	cases := []Price{
		{Version: "metered_v2", Operation: "rerank", Policy: "provider_reported_v1", Terms: []Term{{Key: "output", Unit: "token", Price: Rational{"1", "1"}}}},
		{Version: "metered_v2", Operation: "multimodal_embedding", Policy: "provider_reported_v1", Terms: []Term{{Key: "text", Unit: "token", Price: Rational{"1", "1"}}}},
		{Version: "metered_v2", Operation: "ocr", Policy: "provider_tokens_v1", Terms: []Term{{Key: "input", Unit: "token", Price: Rational{"1", "1"}}}},
		{Version: "metered_v2", Operation: "ocr", Policy: "sum", Terms: []Term{{Key: "input", Unit: "token", Price: Rational{"1", "1"}}}},
		{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []Term{{Key: "provider", Unit: "provider_unit", ProviderUnit: "elevenlabs.character_cost.multilingual_v2.v1", Price: Rational{"1", "1"}}}},
	}
	for _, p := range cases {
		if err := ValidatePrice(p); err == nil {
			t.Errorf("accepted ambiguous price %+v", p)
		}
	}
}
