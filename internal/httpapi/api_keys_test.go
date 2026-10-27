package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
	"unihub/internal/productusers"
)

func TestDurableAPIKeyKeepsInstanceScopeAndNeverListsItsSecret(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("prepaid")
	f.rate("api-rate", billing.CashAsset, "1")
	path := "/v1/accounts/" + f.account + "/api-keys"
	body := map[string]any{"name": "Developer", "instance_id": "default", "models": []string{"acceptance-model"}, "funding_mode": "prepaid_cash", "max_total": "100"}
	var key map[string]any
	f.request("POST", path, "product-alice", body, "", 201, &key)
	token, _ := key["token"].(string)
	if !strings.HasPrefix(token, "uh_key_") || key["credential_kind"] != "api_key" || key["actor_id"] != f.actor.ID || key["origin_product_id"] != "superwagie" || key["instance_id"] != "default" {
		t.Fatalf("key scope: %+v", key)
	}
	if key["expires_at"] != nil {
		t.Fatal("nonexpiring developer key was truncated to a task/subscription lifetime")
	}
	var tokenHash string
	if err := f.pool.QueryRow(context.Background(), `SELECT token_hash FROM model_credentials WHERE id=$1`, key["id"]).Scan(&tokenHash); err != nil || len(tokenHash) != 64 || strings.Contains(tokenHash, token) {
		t.Fatalf("persisted bearer: %v", err)
	}
	var listed []map[string]any
	data := f.request("GET", path, "product-alice", nil, "", 200, &listed)
	if len(listed) != 1 || strings.Contains(string(data), token) || listed[0]["token"] != nil {
		t.Fatalf("secret leaked through list: %s", data)
	}
	verify := map[string]string{"token": token, "model": "acceptance-model", "instance_id": "default"}
	claim, verifyErr := modelaccess.New(f.pool, accounts.New(f.pool), f.billing).VerifyForInstance(context.Background(), token, "acceptance-model", "default")
	if verifyErr != nil {
		t.Fatalf("domain verify: %v", verifyErr)
	}
	if _, err := json.Marshal(claim); err != nil {
		t.Fatalf("domain verify serialization: expires=%v error=%v", claim.ExpiresAt, err)
	}
	var verified map[string]any
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, verify, "", 200, &verified)
	if verified["id"] != key["id"] || verified["expires_at"] != "9999-12-31T23:59:59Z" {
		t.Fatalf("invalid durable verification: %+v", verified)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,'superwagie','default','["member"]',false,1)`, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, verify, "", 403, nil)
	if _, err := f.pool.Exec(context.Background(), `UPDATE product_user_access SET active=true WHERE user_id=$1`, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	verify["instance_id"] = "other"
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, verify, "", 403, nil)
	verify["instance_id"] = "default"
	verify["model"] = "outside-scope"
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, verify, "", 403, nil)
	body["instance_id"] = "other"
	f.request("POST", path, "product-alice", body, "", 403, nil)
	// The old task-only endpoint cannot bypass the durable instance boundary.
	f.request("DELETE", "/v1/accounts/"+f.account+"/model-credentials/"+key["id"].(string), "product-alice", nil, "", 404, nil)
	f.request("DELETE", path+"/"+key["id"].(string), "product-alice", nil, "", 204, nil)
	verify["model"] = "acceptance-model"
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, verify, "", 403, nil)
	// Revocation removes public read authority as well as dispatch authority.
	verify["purpose"] = "receipt"
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, verify, "", 403, nil)
	var expiring modelaccess.APIKey
	f.request("POST", path, "product-alice", map[string]any{"name": "Expiring", "models": []string{"acceptance-model"}, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, "", 201, &expiring)
	if _, err := f.pool.Exec(context.Background(), `UPDATE model_credentials SET expires_at=now()-interval '1 second' WHERE id=$1`, expiring.ID); err != nil {
		t.Fatal(err)
	}
	verify["token"] = expiring.Token
	for _, purpose := range []string{"", "receipt"} {
		verify["purpose"] = purpose
		f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, verify, "", 403, nil)
	}
}

func TestAPIKeyMemberOwnershipAndBudgetFinanceRolesAreRechecked(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("prepaid")
	f.rate("member-api-rate", billing.CashAsset, "1")
	ctx := context.Background()
	a := accounts.New(f.pool)
	bob, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	team, err := a.CreateTeam(ctx, f.actor.ID, "API key team")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.Invite(ctx, f.actor.ID, team.ID, bob.ID, "billing_admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.AcceptInvitation(ctx, bob.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	path := "/v1/accounts/" + team.ID
	request := map[string]any{"name": "Developer", "models": []string{"acceptance-model"}}
	var ownerKey, memberKey map[string]any
	f.request("POST", path+"/api-keys", "product-alice", request, "", 201, &ownerKey)
	f.request("POST", path+"/api-keys", "product-bob", request, "", 201, &memberKey)
	budgetPath := path + "/usage-budget?product_id=superwagie&instance_id=default&funding_mode=prepaid_cash"
	f.request("PUT", budgetPath, "bob", map[string]any{"version": 0, "max_total": "100"}, "", 200, nil)
	if err = a.ChangeRole(ctx, f.actor.ID, team.ID, bob.ID, "member"); err != nil {
		t.Fatal(err)
	}
	f.request("PUT", budgetPath, "bob", map[string]any{"version": 1, "max_total": "1000"}, "", 403, nil)
	f.request("GET", budgetPath, "product-bob", nil, "", 403, nil)
	f.request("DELETE", path+"/api-keys/"+ownerKey["id"].(string), "product-bob", nil, "", 403, nil)
	var keys []map[string]any
	f.request("GET", path+"/api-keys", "product-bob", nil, "", 200, &keys)
	if len(keys) != 1 || keys[0]["id"] != memberKey["id"] {
		t.Fatalf("member saw another actor's keys: %+v", keys)
	}
	f.request("GET", path+"/api-keys?product_id=agentwiki", "product-bob", nil, "", 403, nil)
	if err = a.RemoveMember(ctx, f.actor.ID, team.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	f.request("GET", path+"/api-keys", "product-bob", nil, "", 403, nil)
	f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, map[string]string{"token": memberKey["token"].(string), "instance_id": "default", "model": "acceptance-model"}, "", 403, nil)
}

func TestManagedAPIKeyRechecksProductUsePermissionBeforeNewExposure(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("prepaid")
	f.rate("permission-api-rate", billing.CashAsset, "1")
	f.recharge("llmrouter", "1000000", "permission-api-fund")
	ctx := context.Background()
	var key map[string]any
	f.request("POST", "/v1/accounts/"+f.account+"/api-keys", "product-alice", map[string]any{"name": "Developer", "models": []string{"acceptance-model"}}, "", 201, &key)
	registry, err := productusers.Parse([]byte(`[{"product_id":"superwagie","instance_id":"default","name":"SuperWagie","entry_url":"https://product.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"viewer","name":"Viewer","permissions":["catalog:read"]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	users := productusers.New(f.pool, registry, "https://hub.test", nil)
	f.billing.ConfigureProductUsers(users)
	access := modelaccess.New(f.pool, accounts.New(f.pool), f.billing)
	access.ConfigureProductUsers(users)
	if _, err = access.VerifyForInstance(ctx, key["token"].(string), "", "default"); err != nil {
		t.Fatal(err)
	}
	reserve := map[string]string{"credential": key["token"].(string), "instance_id": "default", "call_id": "permission-call", "task_id": "request-task", "request_digest": "permission-digest", "rate_id": "permission-api-rate", "amount": "40"}
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, reserve, "", 200, nil)
	if _, err = f.pool.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,'superwagie','default','["viewer"]',true,1)`, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = access.VerifyForInstance(ctx, key["token"].(string), "acceptance-model", "default"); err == nil {
		t.Fatal("permissionless role retained key access")
	}
	reserve["call_id"] = "permission-blocked"
	reserve["request_digest"] = "permission-blocked-digest"
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, reserve, "", 403, nil)
	f.request("POST", "/internal/v1/calls/permission-call/topup", billingModesServiceToken, map[string]string{"amount": "1"}, "permission-revoked", 403, nil)
	f.request("POST", "/internal/v1/calls/permission-call/settle", billingModesServiceToken, map[string]any{"usage": map[string]string{"input": "10", "output": "0", "cache_read": "0", "cache_write": "0", "reasoning": "0"}}, "", 200, nil)
}

func TestAPIKeyAndSharedUsageBudgetSerializeConcurrentHoldsAndReleaseCapacity(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("prepaid")
	f.rate("budget-rate", billing.CashAsset, "1")
	f.recharge("llmrouter", "1000000", "api-fund")
	path := "/v1/accounts/" + f.account
	var key map[string]any
	f.request("POST", path+"/api-keys", "product-alice", map[string]any{"name": "CI", "instance_id": "default", "models": []string{"acceptance-model"}, "max_total": "90", "max_per_call": "80"}, "", 201, &key)
	budgetPath := path + "/usage-budget?product_id=superwagie&instance_id=default&funding_mode=prepaid_cash"
	var budget map[string]any
	f.request("GET", budgetPath, "product-alice", nil, "", 200, &budget)
	if budget["version"] != float64(0) || budget["max_total"] != "" || budget["used"] != "0" {
		t.Fatalf("initial budget: %+v", budget)
	}
	f.request("PUT", budgetPath, "alice", map[string]any{"version": 0, "max_total": "100", "max_per_call": "80"}, "", 200, &budget)
	f.request("PUT", budgetPath, "alice", map[string]any{"version": 0, "max_total": "100"}, "", 409, nil)
	token := key["token"].(string)
	reserve := func(id, amount string) map[string]string {
		return map[string]string{"credential": token, "instance_id": "default", "call_id": id, "request_digest": "digest-" + id, "rate_id": "budget-rate", "amount": amount, "task_id": "generated-" + id}
	}
	// Two independent requests both see enough wallet money; only one may claim
	// the lifetime key cap. The other must fail before acquiring a ledger hold.
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, id := range []string{"budget-a", "budget-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			data, _ := json.Marshal(reserve(id, "60"))
			r, _ := http.NewRequest("POST", f.server.URL+"/internal/v1/calls/reserve", bytes.NewReader(data))
			r.Header.Set("Authorization", "Bearer "+billingModesServiceToken)
			r.Header.Set("Content-Type", "application/json")
			response, err := f.client.Do(r)
			if err != nil {
				statuses <- 0
				return
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			statuses <- response.StatusCode
		}(id)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[403] != 1 {
		t.Fatalf("concurrent budget admits: %v", counts)
	}
	var winner string
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM billing_calls WHERE account_id=$1`, f.account).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	f.request("GET", budgetPath, "product-alice", nil, "", 200, &budget)
	if budget["held"] != "60" || budget["used"] != "0" || budget["remaining"] != "40" {
		t.Fatalf("reserved budget: %+v", budget)
	}
	var otherKey map[string]any
	f.request("POST", path+"/api-keys", "product-alice", map[string]any{"name": "Other CI", "models": []string{"acceptance-model"}}, "", 201, &otherKey)
	otherReserve := reserve("other-key-budget", "60")
	otherReserve["credential"] = otherKey["token"].(string)
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, otherReserve, "", 403, nil)
	// Retry is the same reservation and never consumes capacity twice.
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, reserve(winner, "60"), "", 200, nil)
	f.request("POST", "/internal/v1/calls/"+winner+"/topup", billingModesServiceToken, map[string]string{"amount": "31"}, "over-key", 403, nil)
	settlement := map[string]any{"usage": map[string]string{"input": "20", "output": "0", "cache_read": "0", "cache_write": "0", "reasoning": "0"}}
	for range 2 {
		f.request("POST", "/internal/v1/calls/"+winner+"/settle", billingModesServiceToken, settlement, "", 200, nil)
	}
	f.request("GET", budgetPath, "product-alice", nil, "", 200, &budget)
	if budget["held"] != "0" || budget["used"] != "20" || budget["remaining"] != "80" {
		t.Fatalf("settled budget: %+v", budget)
	}
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, reserve("budget-c", "70"), "", 200, nil)
	f.request("POST", "/internal/v1/calls/budget-c/release", billingModesServiceToken, map[string]string{"reason": "not-dispatched"}, "", 200, nil)
	f.request("POST", "/internal/v1/calls/reserve", billingModesServiceToken, reserve("budget-d", "70"), "", 200, nil)
	// A revoked key cannot increase exposure, but its dispatched call can settle.
	f.request("DELETE", path+"/api-keys/"+key["id"].(string), "product-alice", nil, "", 204, nil)
	f.request("POST", "/internal/v1/calls/budget-d/topup", billingModesServiceToken, map[string]string{"amount": "1"}, "revoked", 403, nil)
	f.request("POST", "/internal/v1/calls/budget-d/settle", billingModesServiceToken, settlement, "", 200, nil)
	f.request("GET", budgetPath, "product-alice", nil, "", 200, &budget)
	if budget["used"] != "40" || budget["held"] != "0" {
		t.Fatalf("final budget: %+v", budget)
	}
}

func TestAPIKeyRejectsExpiredOrMalformedRequests(t *testing.T) {
	f := newBillingModesFixture(t)
	f.profile("prepaid")
	f.rate("invalid-api-rate", billing.CashAsset, "1")
	path := "/v1/accounts/" + f.account + "/api-keys"
	for i, change := range []map[string]any{{"expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}, {"max_total": "-1"}, {"max_per_call": "9223372036854775808"}, {"name": " "}} {
		body := map[string]any{"name": "Developer", "instance_id": "default", "models": []string{"acceptance-model"}}
		for k, v := range change {
			body[k] = v
		}
		t.Run(fmt.Sprint(i), func(t *testing.T) { f.request("POST", path, "product-alice", body, "", 400, nil) })
	}
}
