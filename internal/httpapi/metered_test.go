package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/metering"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestNativeValidationHTTPErrorContract(t *testing.T) {
	price := metering.Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}}}
	for _, kind := range []string{"wrong-operation", "missing-measurement", "decimal", "duplicate-price"} {
		t.Run(kind, func(t *testing.T) {
			rate := billing.Rate{Semantics: "metered_v2", Metered: &price}
			u := metering.Usage{Version: "metered_v2", Operation: "speech", State: "confirmed", Measurements: []metering.Measurement{{Key: "characters", Unit: "character", Quantity: metering.Quantity{Value: "1"}}}}
			switch kind {
			case "wrong-operation":
				u.Operation = "chat"
			case "missing-measurement":
				u.Measurements = nil
			case "decimal":
				u.Measurements[0].Quantity.Value = "1.5"
			case "duplicate-price":
				p := price
				p.Terms = append(append([]metering.Term{}, price.Terms...), price.Terms...)
				rate.Metered = &p
			}
			_, err := billing.CalculateCharge(rate, billing.Usage{Metered: &u})
			if err == nil {
				t.Fatal("invalid contract accepted")
			}
			w := httptest.NewRecorder()
			writeError(w, err)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
}
func TestNativeRatePublicationRejectsInvalidPriceHTTP(t *testing.T) {
	h, _, _, _ := billingFixture(t)
	rate := billing.Rate{ID: "invalid-metered", Model: "speech", Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1.5", Denominator: "1"}}}}}
	b, err := json.Marshal(rate)
	if err != nil {
		t.Fatal(err)
	}
	w := call(h, "POST", "/ops/v1/rates", "ops-alice", string(b), "")
	if w.Code != 400 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestNativeInvalidSettlementKeepsReservedHTTP(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	u, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	as, err := a.ListAccounts(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := billing.New(p, a, noLedger{})
	b.ConfigureTrustedServices([]string{"llmrouter"})
	const key = "trusted-model-service-key-at-least-32-characters"
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ServiceToken: key})
	rate := billing.Rate{ID: "native", Asset: billing.CreditAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "speech", Policy: "sum", Terms: []metering.Term{{Key: "characters", Unit: "character", Price: metering.Rational{Numerator: "1", Denominator: "1000"}}}}}
	c := billing.Call{ServiceID: "llmrouter", CallID: "native-call", AccountID: as[0].ID, ActorID: u.ID, Status: "RESERVED", Held: "5", Rate: rate}
	snapshot, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'native-test',$3)`, c.CallID, c.AccountID, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, usage := range []string{`{"version":"metered_v2","operation":"chat","state":"confirmed","source":"provider","measurements":[]}`, `{"version":"metered_v2","operation":"speech","state":"confirmed","source":"provider","measurements":[]}`} {
		w := call(h, "POST", "/internal/v1/calls/native-call/settle", key, `{"usage":{"metered":`+usage+`}}`, "")
		if w.Code != 400 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	stored, err := b.GetCall(ctx, "llmrouter", c.CallID)
	if err != nil || stored.Status != "RESERVED" || stored.Held != "5" || stored.FinalDigest != "" {
		t.Fatalf("%+v %v", stored, err)
	}
}
