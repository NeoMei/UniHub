package httpapi

import (
	"testing"
	"unihub/internal/billing"
	"unihub/internal/metering"
)

// Native receipts travel through the real HTTP credential/reserve/settle
// contract into isolated PostgreSQL and Formance ledgers. Supplier execution
// is deliberately outside this accounting acceptance test.
func TestNativeMeteredUnitsAndAssetsHTTPAcceptance(t *testing.T) {
	for _, asset := range []string{billing.CashAsset, billing.CreditAsset} {
		t.Run(asset, func(t *testing.T) {
			f := newBillingModesFixture(t)
			mode := billing.FundingPrepaidCash
			if asset == billing.CashAsset {
				f.profile("prepaid")
				f.recharge("llmrouter", "1000000", "native-cash")
			} else {
				mode = billing.FundingCredits
				f.profile("hybrid")
				f.recharge("superwagie", "3000000", "native-membership-cash")
				f.offer("native-month", "month", "1000")
				f.purchase("native-month", "native-membership")
			}
			cases := []struct {
				name, operation, policy, key, unit, namespace, value string
				scale                                                int
				want                                                 string
			}{
				{"chat", "chat", "provider_reported_v1", "input", "token", "", "3", 0, "6"},
				{"embedding", "embedding", "provider_reported_v1", "input", "token", "", "2", 0, "4"},
				{"speech", "speech", "unicode_scalar_v1", "characters", "character", "", "5", 0, "10"},
				{"transcription", "transcription", "provider_reported_v1", "audio", "audio_second", "", "1001", 3, "3"},
				{"image", "image_generation", "successful_outputs_v1", "1024x1024", "image", "", "2", 0, "4"},
				{"ocr", "ocr", "success_request", "requests", "request", "", "1", 0, "2"},
				{"video-seconds", "video", "sora_completed_seconds_v1", "video", "video_second", "", "45", 1, "9"},
				{"video-units", "video", "kling_final_units_v1", "provider", "provider_unit", "kling.final_unit_deduction", "125", 2, "3"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					price := metering.Price{Version: "metered_v2", Operation: tc.operation, Policy: tc.policy, Terms: []metering.Term{{Key: tc.key, Unit: tc.unit, ProviderUnit: tc.namespace, Price: metering.Rational{Numerator: "2", Denominator: "1"}}}}
					rate := billing.Rate{ID: "native-" + tc.name, Model: "acceptance-model", Asset: asset, Semantics: "metered_v2", Metered: &price}
					f.request("POST", "/ops/v1/rates", "alice", rate, "", 201, nil)
					grant := f.credential(mode)
					id := "native-" + tc.name
					if tc.operation == "video" {
						f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, billingModesReserve(grant, id, rate.ID, "100"), "", 403, nil)
						f.request("GET", "/internal/v1/calls/"+id, billingModesServiceToken, nil, "", 404, nil)
						f.request("POST", "/ops/v1/rates/"+rate.ID+"/retire", "alice", nil, "", 204, nil)
						return
					}
					f.reserve(grant, id, rate.ID, "100")
					usage := billing.Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: tc.operation, State: "confirmed", Source: "supplier_fixture", Measurements: []metering.Measurement{{Key: tc.key, Unit: tc.unit, ProviderUnit: tc.namespace, Quantity: metering.Quantity{Value: tc.value, Scale: tc.scale}}}}}
					body := map[string]any{"usage": usage, "provider_cost": "0"}
					var first, replay billing.Call
					path := "/internal/v1/calls/" + id + "/settle"
					f.request("POST", path, billingModesServiceToken, body, "", 200, &first)
					f.request("POST", path, billingModesServiceToken, body, "", 200, &replay)
					if first.Status != "SETTLED" || first.Charged != tc.want || first.ChargeAsset != asset || first.FinalDigest == "" || first.FinalDigest != replay.FinalDigest || replay.Charged != first.Charged {
						t.Fatalf("receipt/replay: first=%+v replay=%+v", first, replay)
					}
					// Retiring a rate preserves this call snapshot, and prevents the
					// next modality's rate from creating an ambiguous active model price.
					f.request("POST", "/ops/v1/rates/"+rate.ID+"/retire", "alice", nil, "", 204, nil)
					// A final command with a different amount must not mutate money.
					usage.Metered.Measurements[0].Quantity = metering.Quantity{Value: "0"}
					f.request("POST", path, billingModesServiceToken, map[string]any{"usage": usage, "provider_cost": "0"}, "", 409, nil)
				})
			}
			if asset == billing.CashAsset {
				f.balance("llmrouter", 999971)
			} else {
				var batches []billing.Batch
				f.request("GET", "/v1/accounts/"+f.account+"/credits", "alice", nil, "", 200, &batches)
				if len(batches) != 1 || batches[0].Available != "971" || batches[0].Consumed != "29" || batches[0].Reserved != "0" {
					t.Fatalf("native credit balance/replay: %+v", batches)
				}
			}
		})
	}
}
