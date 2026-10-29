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

type wireLedger struct {
	balances     map[string]int64
	transactions map[string]ledger.Transaction
}

func (l *wireLedger) Post(_ context.Context, cmd ledger.Command) (ledger.Transaction, error) {
	if tx, ok := l.transactions[cmd.Reference]; ok {
		return tx, nil
	}
	for _, p := range cmd.Postings {
		if p.Source != "world" && l.balances[p.Source+"/"+p.Asset] < p.Amount {
			return ledger.Transaction{}, ledger.ErrInsufficientFunds
		}
	}
	for _, p := range cmd.Postings {
		if p.Source != "world" {
			l.balances[p.Source+"/"+p.Asset] -= p.Amount
		}
		l.balances[p.Destination+"/"+p.Asset] += p.Amount
	}
	tx := ledger.Transaction{ID: cmd.Reference, Reference: cmd.Reference, Postings: cmd.Postings}
	l.transactions[cmd.Reference] = tx
	return tx, nil
}
func (l *wireLedger) Find(_ context.Context, ref string) (*ledger.Transaction, error) {
	if tx, ok := l.transactions[ref]; ok {
		return &tx, nil
	}
	return nil, nil
}
func (l *wireLedger) Balance(_ context.Context, address, asset string) (int64, error) {
	return l.balances[address+"/"+asset], nil
}

func TestWireOperationHTTPContract(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	accountsStore := accounts.New(pool)
	user, err := accountsStore.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://wire", Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	accountList, err := accountsStore.ListAccounts(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	account := accountList[0].ID
	l := &wireLedger{balances: map[string]int64{}, transactions: map[string]ledger.Transaction{}}
	b := billing.New(pool, accountsStore, l)
	b.ConfigureOperators([]string{user.ID})
	b.ConfigureTrustedServices([]string{"llmrouter"})
	_, err = b.SaveProfile(ctx, user.ID, "superwagie", billing.BillingProfile{ProductID: "superwagie", Usage: billing.UsagePolicy{Enabled: true, FundingModes: []string{billing.FundingPrepaidCash}, DefaultFundingMode: billing.FundingPrepaidCash}})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"image_generation", "image_edit"} {
		_, err = b.PublishRate(ctx, user.ID, billing.Rate{ID: op, Model: "image", Asset: billing.CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: op, Policy: "successful_outputs_v1", Terms: []metering.Term{{Key: "images", Unit: "image", Price: metering.Rational{Numerator: "1", Denominator: "1"}}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	l.balances[wallet.Address(account, "llmrouter")+"/"+billing.CashAsset] = 100
	access := modelaccess.New(pool, accountsStore, b)
	grant, err := access.Issue(ctx, user.ID, account, modelaccess.Request{OriginProductID: "superwagie", Models: []string{"image"}, TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	const serviceKey = "test-only-router-service-credential-at-least-32-bytes"
	h := New(verifier{}, accountsStore, wallet.New(pool, accountsStore, l), Options{Billing: b, ModelAccess: access, ServiceToken: serviceKey})
	request := func(path string, body map[string]string, code int) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		w := call(h, "POST", path, serviceKey, string(encoded), "")
		if code != w.Code {
			t.Fatalf("status %d want %d: %s", w.Code, code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	verify := map[string]string{"token": grant.Token, "model": "image", "wire_operation": "images.generations.create", "instance_id": "default"}
	result := request("/internal/v1/model-credentials/verify", verify, 200)
	if operations, ok := result["operations"].(map[string]any); !ok || len(operations["image"].([]any)) == 0 || operations["image"].([]any)[0] != "images.generations.create" {
		t.Fatalf("missing authorization: %v", result)
	}
	verify["instance_id"] = "another-instance"
	request("/internal/v1/model-credentials/verify", verify, 403)
	verify["purpose"] = "receipt"
	request("/internal/v1/model-credentials/verify", verify, 403)
	delete(verify, "purpose")
	verify["instance_id"] = ""
	request("/internal/v1/model-credentials/verify", verify, 200)
	verify["instance_id"] = "default"
	verify["wire_operation"] = "videos.create"
	request("/internal/v1/model-credentials/verify", verify, 403)
	verify["wire_operation"] = "images.generations.create"
	reserve := map[string]string{"credential": grant.Token, "call_id": "image-call", "request_digest": "digest", "rate_id": "image_generation", "amount": "1", "task_id": "task", "instance_id": "default", "wire_operation": "images.generations.create"}
	reserve["instance_id"] = "another-instance"
	request("/internal/v1/calls/reserve", reserve, 403)
	reserve["instance_id"] = "default"
	out := request("/internal/v1/calls/reserve", reserve, 200)
	if out["wire_operation"] != "images.generations.create" {
		t.Fatalf("missing wire operation: %v", out)
	}
	out = request("/internal/v1/calls/reserve", reserve, 200)
	if out["wire_operation"] != "images.generations.create" {
		t.Fatalf("replay lost wire: %v", out)
	}
	readback := call(h, "GET", "/internal/v1/calls/image-call", serviceKey, "", "")
	if readback.Code != 200 {
		t.Fatalf("call readback: %d %s", readback.Code, readback.Body.String())
	}
	var stored billing.Call
	if err := json.Unmarshal(readback.Body.Bytes(), &stored); err != nil || stored.WireOperation != "images.generations.create" {
		t.Fatalf("stored wire: %+v %v", stored, err)
	}
	reserve["wire_operation"] = "dashscope.images.generate"
	request("/internal/v1/calls/reserve", reserve, 409)
	reserve["wire_operation"] = ""
	reserve["call_id"] = "legacy-call"
	request("/internal/v1/calls/reserve", reserve, 200)
	reserve["wire_operation"] = "images.generations.create"
	reserve["call_id"] = "wrong-rate"
	reserve["rate_id"] = "image_edit"
	request("/internal/v1/calls/reserve", reserve, 403)
	if err := b.RetireRate(ctx, user.ID, "image_generation"); err != nil {
		t.Fatal(err)
	}
	request("/internal/v1/model-credentials/verify", verify, 403)
	verify["purpose"] = "receipt"
	out = request("/internal/v1/model-credentials/verify", verify, 200)
	if operations, ok := out["operations"].(map[string]any); !ok || len(operations["image"].([]any)) == 0 || operations["image"].([]any)[0] != "images.generations.create" {
		t.Fatalf("missing receipt authorization: %v", out)
	}
	// Access to a private result has stricter authority than internal
	// reconciliation and must not depend on a currently published tariff.
	verify["purpose"] = "result_read"
	// Result reads require current user identity even after rate retirement.
	request("/internal/v1/model-credentials/verify", verify, 401)
	verify["purpose"] = "unexpected"
	request("/internal/v1/model-credentials/verify", verify, 403)
	verify["purpose"] = "result_read"
	encoded, err := json.Marshal(verify)
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated := call(h, "POST", "/internal/v1/model-credentials/verify", "wrong-service-key", string(encoded), "")
	if unauthenticated.Code != 401 && unauthenticated.Code != 403 {
		t.Fatalf("result authority accepted an unauthenticated caller: %d", unauthenticated.Code)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	request("/internal/v1/model-credentials/verify", verify, 403)
	verify["purpose"] = "receipt"
	request("/internal/v1/model-credentials/verify", verify, 200)
	if _, err := pool.Exec(ctx, `UPDATE users SET active=true WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	verify["purpose"] = "result_read"
	if err := access.Revoke(ctx, user.ID, account, grant.ID); err != nil {
		t.Fatal(err)
	}
	request("/internal/v1/model-credentials/verify", verify, 403)
	verify["token"] = "invalid"
	request("/internal/v1/model-credentials/verify", verify, 403)
}
