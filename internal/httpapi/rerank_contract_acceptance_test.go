package httpapi

import (
	"testing"
	"unihub/internal/billing"
	"unihub/internal/metering"
)

func TestRerankInputTokenHTTPSettlement(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("prepaid")
	f.recharge("llmrouter", "1000000", "rerank-cash")
	price := metering.Price{Version: "metered_v2", Operation: "rerank", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "2"}}}}
	rate := billing.Rate{ID: "rerank-input", Model: "acceptance-model", Asset: billing.CashAsset, Semantics: "metered_v2", Metered: &price}
	f.request("POST", "/ops/v1/rates", "alice", rate, "", 201, nil)
	grant := f.credential(billing.FundingPrepaidCash)
	reserve := billingModesReserve(grant, "rerank-call", rate.ID, "100")
	reserve["wire_operation"] = "rerank.create"
	reserve["instance_id"] = "default"
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, reserve, "", 200, nil)
	usage := billing.Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "rerank", State: "confirmed", Source: "supplier_fixture", Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "6"}}}}}
	var call billing.Call
	f.request("POST", "/internal/v1/calls/rerank-call/settle", billingModesServiceToken, map[string]any{"usage": usage, "provider_cost": "0"}, "", 200, &call)
	if call.Status != "SETTLED" || call.Charged != "3" || call.ChargeAsset != billing.CashAsset {
		t.Fatalf("unexpected settlement: %+v", call)
	}
	f.balance("llmrouter", 999997)
}
