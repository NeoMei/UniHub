package billing

import (
	"context"
	"testing"
	"time"
	"unihub/internal/ledger"
	"unihub/internal/metering"
	"unihub/internal/wallet"
)

func TestOCRLegacyPolicyCannotAuthorizeNewWireButHistoricalChargeRemains(t *testing.T) {
	r := Rate{Model: "legacy-ocr", Asset: CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "ocr", Policy: "sum", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
	u := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "ocr", State: "confirmed", Measurements: []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1"}}}}}
	got, err := CalculateCharge(r, u)
	if err != nil || got != "1" {
		t.Fatalf("historical charge %s %v", got, err)
	}
	if RateMatchesWire(r, "ocr.jobs.create") || RateMatchesWire(r, "dashscope.ocr.recognitions.create") {
		t.Fatal("legacy policy authorized new OCR operation")
	}
}

func TestLegacyOCRRateCannotBeRepublishedOrReservedButExistingCallSettles(t *testing.T) {
	f := prepaidFixture(t)
	ctx := context.Background()
	legacy := Rate{ID: "legacy-ocr-rate", Model: "legacy-ocr", Asset: CashAsset, Semantics: "metered_v2", Status: "published", Metered: &metering.Price{Version: "metered_v2", Operation: "ocr", Policy: "sum", Terms: []metering.Term{{Key: "requests", Unit: "request", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
	if _, err := f.s.PublishRate(ctx, f.actor, legacy); err == nil {
		t.Fatal("new legacy policy publication accepted")
	}
	if _, err := f.s.pool.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2)`, legacy.ID, encode(legacy)); err != nil {
		t.Fatal(err)
	}
	cid := id("mc_")
	if _, err := f.s.pool.Exec(ctx, `INSERT INTO model_credentials(id,token_hash,actor_id,account_id,origin_product_id,models,task_id,expires_at,funding_mode,asset,billing_profile_version) VALUES($1,$1,$2,$3,'superwagie','["legacy-ocr"]','ocr',$4,$5,$6,1)`, cid, f.actor, f.account, f.s.now().Add(time.Hour), FundingPrepaidCash, CashAsset); err != nil {
		t.Fatal(err)
	}
	fundCash(t, f, "llmrouter", 10)
	req := ReserveRequest{CallID: "new-legacy-ocr", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", RequestDigest: "digest", RateID: legacy.ID, Amount: "2", CredentialID: cid}
	if _, err := f.s.ReserveWithWire(ctx, "router", req, "default", "ocr", "ocr.jobs.create"); err == nil {
		t.Fatal("legacy policy authorized new wire")
	}
	if _, err := f.s.Reserve(ctx, "router", req); err == nil {
		t.Fatal("legacy policy authorized new wire-less reserve")
	}
	cashBalance(t, f, wallet.Address(f.account, "llmrouter"), 10)
	call := Call{ServiceID: "router", CallID: "old-legacy-ocr", ActorID: f.actor, AccountID: f.account, OriginProductID: "superwagie", Rate: legacy, Held: "2", Status: "RESERVED", CashHoldAddress: CashHoldAddress(f.account, "old-legacy-ocr"), ChargeAsset: CashAsset, FundingMode: FundingPrepaidCash, FundingProductID: "llmrouter", BillingProfileVersion: 1, CredentialID: cid}
	if _, err := f.s.pool.Exec(ctx, `INSERT INTO billing_calls(id,account_id,request_digest,snapshot) VALUES($1,$2,'legacy',$3)`, call.CallID, f.account, encode(call)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.l.Post(ctx, ledger.Command{Reference: "legacy-existing-hold", Postings: []ledger.Posting{{Source: "world", Destination: CashHoldAddress(f.account, call.CallID), Asset: CashAsset, Amount: 2}}}); err != nil {
		t.Fatal(err)
	}
	usage := Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: "ocr", State: "confirmed", Measurements: []metering.Measurement{{Key: "requests", Unit: "request", Quantity: metering.Quantity{Value: "1"}}}}}
	settled, err := f.s.Settle(ctx, "router", SettlementRequest{CallID: call.CallID, Usage: &usage})
	if err != nil || settled.Status != "SETTLED" || settled.Charged != "1" {
		t.Fatalf("historical settlement %+v %v", settled, err)
	}
}
