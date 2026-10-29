package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/metering"
	"unihub/internal/modelaccess"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestPlannedVideoRejectsNewWireAndLegacyReserveButPreservesReceipt(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := accounts.New(pool)
	user, err := store.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://wan-guard", Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	accountsList, err := store.ListAccounts(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	account := accountsList[0].ID
	ledger := &wireLedger{balances: map[string]int64{}, transactions: map[string]ledger.Transaction{}}
	b := billing.New(pool, store, ledger)
	b.ConfigureOperators([]string{user.ID})
	b.ConfigureTrustedServices([]string{"llmrouter"})
	if _, err = b.SaveProfile(ctx, user.ID, "superwagie", billing.BillingProfile{ProductID: "superwagie", Usage: billing.UsagePolicy{Enabled: true, FundingModes: []string{billing.FundingPrepaidCash}, DefaultFundingMode: billing.FundingPrepaidCash}}); err != nil {
		t.Fatal(err)
	}
	old := billing.Rate{ID: "old-wan", Model: "wan2.6-t2v", Asset: billing.CashAsset, Status: "published", Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "video", Policy: "ali_wan26_t2v_billable_duration_v1", Terms: []metering.Term{{Key: "video_720p", Unit: "video_second", Price: metering.Rational{Numerator: "3", Denominator: "5"}}, {Key: "video_1080p", Unit: "video_second", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
	snapshot, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	// The old two-tier tariff was already published by the deployed version;
	// current validation cannot publish it, so seed its immutable snapshot.
	if _, err = pool.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2)`, old.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ model, op string }{{"chat-model", "chat"}, {"embed-model", "embedding"}} {
		rate := billing.Rate{ID: tc.model + "-rate", Model: tc.model, Asset: billing.CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: tc.op, Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}}
		if _, err = b.PublishRate(ctx, user.ID, rate); err != nil {
			t.Fatal(err)
		}
	}
	ledger.balances[wallet.Address(account, "llmrouter")+"/"+billing.CashAsset] = 100
	access := modelaccess.New(pool, store, b)
	grant, err := access.Issue(ctx, user.ID, account, modelaccess.Request{OriginProductID: "superwagie", Models: []string{"wan2.6-t2v", "chat-model", "embed-model"}, TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	const serviceKey = "test-only-wan-guard-service-key-at-least-32-bytes"
	h := New(verifier{}, store, wallet.New(pool, store, ledger), Options{Billing: b, ModelAccess: access, ServiceToken: serviceKey})
	request := func(path string, body map[string]string, want int) {
		t.Helper()
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		w := call(h, "POST", path, serviceKey, string(encoded), "")
		if w.Code != want {
			t.Fatalf("%s status=%d want=%d body=%s", path, w.Code, want, w.Body.String())
		}
	}
	verify := map[string]string{"token": grant.Token, "model": "wan2.6-t2v", "wire_operation": "videos.create", "instance_id": "default"}
	request("/internal/v1/model-credentials/verify", verify, 403)
	verify["purpose"] = "receipt"
	request("/internal/v1/model-credentials/verify", verify, 200)
	delete(verify, "purpose")
	delete(verify, "wire_operation")
	request("/internal/v1/model-credentials/verify", verify, 200)
	reserve := map[string]string{"credential": grant.Token, "call_id": "wan-wire", "request_digest": "digest", "rate_id": old.ID, "amount": "10", "task_id": "task", "instance_id": "default", "wire_operation": "videos.create"}
	request("/internal/v1/calls/reserve", reserve, 403)
	if _, err := b.GetCall(ctx, "llmrouter", "wan-wire"); err == nil {
		t.Fatal("denied wire created a call")
	}
	if got := ledger.balances[wallet.Address(account, "llmrouter")+"/"+billing.CashAsset]; got != 100 {
		t.Fatalf("denied wire changed cash: %d", got)
	}
	// New no-wire reservations are gated too; historical replay is covered at
	// the billing boundary with a previously recorded command.
	delete(reserve, "wire_operation")
	reserve["call_id"] = "wan-legacy-new"
	request("/internal/v1/calls/reserve", reserve, 403)
	if _, err := b.GetCall(ctx, "llmrouter", "wan-legacy-new"); err == nil {
		t.Fatal("denied no-wire call created a record")
	}
	if got := ledger.balances[wallet.Address(account, "llmrouter")+"/"+billing.CashAsset]; got != 100 {
		t.Fatalf("denied no-wire call changed cash: %d", got)
	}
	for _, tc := range []struct{ model, wire, op string }{{"chat-model", "chat.completions.create", "chat"}, {"embed-model", "embeddings.create", "embedding"}} {
		request("/internal/v1/model-credentials/verify", map[string]string{"token": grant.Token, "model": tc.model, "wire_operation": tc.wire, "instance_id": "default"}, 200)
		request("/internal/v1/calls/reserve", map[string]string{"credential": grant.Token, "call_id": tc.model + "-call", "request_digest": "digest", "rate_id": tc.model + "-rate", "amount": "10", "task_id": "task", "instance_id": "default", "wire_operation": tc.wire}, 200)
		usage := billing.Usage{Metered: &metering.Usage{Version: "metered_v2", Operation: tc.op, State: "confirmed", Source: "provider", Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: "3"}}}}}
		settled, settleErr := b.Settle(ctx, "llmrouter", billing.SettlementRequest{CallID: tc.model + "-call", Usage: &usage})
		if settleErr != nil || settled.Status != "SETTLED" || settled.Charged != "3" {
			t.Fatalf("%s settle: %+v %v", tc.model, settled, settleErr)
		}
	}
}
