package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/modelaccess"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

// Scope expansion needs real policy and credential state, but no ledger calls.
func newAPIKeyModelsFixture(t *testing.T, catalogs ...modelaccess.ProviderCatalog) *billingModesFixture {
	t.Helper()
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
	b := billing.New(p, a, nil)
	b.ConfigureOperators([]string{u.ID})
	profile, err := billing.BillingPreset("superwagie", "prepaid")
	if err != nil {
		t.Fatal(err)
	}
	profile.Usage.Models = []string{"chat", "embedding", "rerank", "image", "vision", "ocr"}
	if _, err = b.SaveProfile(ctx, u.ID, "superwagie", profile); err != nil {
		t.Fatal(err)
	}
	for _, model := range profile.Usage.Models {
		rate := billing.Rate{ID: "rate-" + model, Model: model, Asset: billing.CashAsset, Semantics: "exclusive_v1", Input: billing.Rational{Numerator: "1", Denominator: "1"}, Output: billing.Rational{Numerator: "0", Denominator: "1"}, CacheRead: billing.Rational{Numerator: "0", Denominator: "1"}, CacheWrite: billing.Rational{Numerator: "0", Denominator: "1"}, Reasoning: billing.Rational{Numerator: "0", Denominator: "1"}}
		if _, err = b.PublishRate(ctx, u.ID, rate); err != nil {
			t.Fatal(err)
		}
	}
	reg := productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}, {ID: "viewer", Name: "Viewer", Permissions: []string{"catalog:read"}}}}}
	users := productusers.New(p, reg, "https://hub.test", nil)
	policy := products.Client{ClientID: "sw", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"accounts:read", "catalog:read", "model-credentials:write"}}
	w := wallet.New(p, a, noLedger{})
	access := modelaccess.New(p, a, b)
	if len(catalogs) > 0 {
		access.ConfigureProviderCatalog(catalogs[0])
	}
	server := httptest.NewServer(New(verifier{}, a, w, Options{Billing: b, ModelAccess: access, ServiceToken: billingModesServiceToken, ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"sw": anyProductVerifier{}}, ProductAccess: map[string]products.Client{"sw": policy}}))
	t.Cleanup(server.Close)
	return &billingModesFixture{t: t, pool: p, billing: b, wallet: w, server: server, client: server.Client(), actor: u, account: as[0].ID}
}

func createScopeTestKey(t *testing.T, f *billingModesFixture, account, actor string) modelaccess.APIKey {
	t.Helper()
	var key modelaccess.APIKey
	f.request("POST", "/v1/accounts/"+account+"/api-keys", actor, map[string]any{"name": "Existing key", "models": []string{"chat"}, "max_total": "100", "max_per_call": "20"}, "", 201, &key)
	return key
}

func TestAPIKeyModelsAppendKeepsCredentialAndAuditsOnlyChanges(t *testing.T) {
	f := newAPIKeyModelsFixture(t)
	key := createScopeTestKey(t, f, f.account, "product-alice")
	ctx := context.Background()
	var originalHash string
	if err := f.pool.QueryRow(ctx, `SELECT token_hash FROM model_credentials WHERE id=$1`, key.ID).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	path := "/v1/accounts/" + f.account + "/api-keys/" + key.ID + "/models"
	body := map[string]any{"models": []string{"embedding", "rerank", "image", "vision", "ocr", "embedding"}}
	for range 2 {
		var got modelaccess.APIKey
		data := f.request("POST", path, "product-alice", body, "", 200, &got)
		if strings.Contains(string(data), key.Token) || strings.Contains(string(data), originalHash) || got.Token != "" {
			t.Fatal("scope update exposed credential material")
		}
		if !slices.Equal(got.Models, []string{"chat", "embedding", "image", "ocr", "rerank", "vision"}) {
			t.Fatalf("models=%v", got.Models)
		}
		expected := key
		expected.Token, expected.Models = "", got.Models
		if !reflect.DeepEqual(got, expected) {
			t.Fatal("scope update changed immutable key metadata or budgets")
		}
	}
	var count int
	var currentHash string
	if err := f.pool.QueryRow(ctx, `SELECT count(*),min(token_hash) FROM model_credentials WHERE account_id=$1`, f.account).Scan(&count, &currentHash); err != nil || count != 1 || currentHash != originalHash {
		t.Fatalf("credential rotated or duplicated: count=%d error=%v", count, err)
	}
	var detail struct {
		AddedModels []string `json:"added_models"`
		Models      []string `json:"models"`
	}
	var raw []byte
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE operation_id=$1 AND action='model_credential.models_added'`, key.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d error=%v", count, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT detail FROM audit_events WHERE operation_id=$1 AND action='model_credential.models_added'`, key.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &detail); err != nil || !slices.Equal(detail.AddedModels, []string{"embedding", "image", "ocr", "rerank", "vision"}) || len(detail.Models) != 6 {
		t.Fatalf("audit detail invalid: %v", err)
	}
	for _, model := range []string{"chat", "embedding", "rerank", "image", "vision", "ocr"} {
		f.request("POST", "/internal/v1/model-credentials/verify", billingModesServiceToken, map[string]string{"token": key.Token, "model": model, "instance_id": "default"}, "", 200, nil)
	}
	var listed modelListResponse
	f.request("GET", "/v1/models", key.Token, nil, "", 200, &listed)
	if len(listed.Data) != 6 {
		t.Fatalf("same key catalog models=%d", len(listed.Data))
	}
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM wallet_operations)+(SELECT count(*) FROM recharge_orders)+(SELECT count(*) FROM billing_calls)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("scope update wrote financial activity: count=%d error=%v", count, err)
	}
}

func TestAPIKeyModelsAppendRejectsInvalidScopeWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   map[string]any
		change func(*testing.T, *billingModesFixture, modelaccess.APIKey)
		want   int
	}{
		{"empty", map[string]any{"models": []string{}}, nil, 400},
		{"missing", map[string]any{}, nil, 400},
		{"too many", map[string]any{"models": make([]string, 33)}, nil, 400},
		{"alias", map[string]any{"models": []string{"Embedding"}}, nil, 403},
		{"unknown field", map[string]any{"models": []string{"embedding"}, "funding_mode": "credits"}, nil, 400},
		{"wrong product", map[string]any{"models": []string{"embedding"}, "origin_product_id": "llmrouter"}, nil, 403},
		{"wrong instance", map[string]any{"models": []string{"embedding"}, "instance_id": "other"}, nil, 403},
		{"retired rate", map[string]any{"models": []string{"embedding"}}, func(t *testing.T, f *billingModesFixture, _ modelaccess.APIKey) {
			if err := f.billing.RetireRate(context.Background(), f.actor.ID, "rate-embedding"); err != nil {
				t.Fatal(err)
			}
		}, 403},
		{"excluded model", map[string]any{"models": []string{"embedding"}}, func(t *testing.T, f *billingModesFixture, _ modelaccess.APIKey) {
			p, err := f.billing.Profile(context.Background(), "superwagie")
			if err != nil {
				t.Fatal(err)
			}
			p.Usage.Models = []string{"chat"}
			if _, err = f.billing.SaveProfile(context.Background(), f.actor.ID, "superwagie", p); err != nil {
				t.Fatal(err)
			}
		}, 403},
		{"revoked", map[string]any{"models": []string{"embedding"}}, func(t *testing.T, f *billingModesFixture, k modelaccess.APIKey) {
			f.request("DELETE", "/v1/accounts/"+f.account+"/api-keys/"+k.ID, "product-alice", nil, "", 204, nil)
		}, 403},
		{"expired", map[string]any{"models": []string{"embedding"}}, func(t *testing.T, f *billingModesFixture, k modelaccess.APIKey) {
			if _, err := f.pool.Exec(context.Background(), `UPDATE model_credentials SET expires_at=now()-interval '1 second' WHERE id=$1`, k.ID); err != nil {
				t.Fatal(err)
			}
		}, 403},
		{"product permission", map[string]any{"models": []string{"embedding"}}, func(t *testing.T, f *billingModesFixture, _ modelaccess.APIKey) {
			if _, err := f.pool.Exec(context.Background(), `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,'superwagie','default','["viewer"]',true,1)`, f.actor.ID); err != nil {
				t.Fatal(err)
			}
		}, 403},
		{"disabled actor", map[string]any{"models": []string{"embedding"}}, func(t *testing.T, f *billingModesFixture, _ modelaccess.APIKey) {
			if _, err := f.pool.Exec(context.Background(), `UPDATE users SET active=false WHERE id=$1`, f.actor.ID); err != nil {
				t.Fatal(err)
			}
		}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPIKeyModelsFixture(t)
			key := createScopeTestKey(t, f, f.account, "product-alice")
			if tc.change != nil {
				tc.change(t, f, key)
			}
			f.request("POST", "/v1/accounts/"+f.account+"/api-keys/"+key.ID+"/models", "product-alice", tc.body, "", tc.want, nil)
			var raw []byte
			var audits int
			if err := f.pool.QueryRow(context.Background(), `SELECT models,(SELECT count(*) FROM audit_events WHERE action='model_credential.models_added') FROM model_credentials WHERE id=$1`, key.ID).Scan(&raw, &audits); err != nil {
				t.Fatal(err)
			}
			var models []string
			if json.Unmarshal(raw, &models) != nil || !slices.Equal(models, []string{"chat"}) || audits != 0 {
				t.Fatalf("rejected append mutated state: models=%v audits=%d", models, audits)
			}
		})
	}
}

func TestAPIKeyModelsAppendRequiresAccountRoleAndLiveKeyHolder(t *testing.T) {
	f := newAPIKeyModelsFixture(t)
	ctx := context.Background()
	a := accounts.New(f.pool)
	bob, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	team, err := a.CreateTeam(ctx, f.actor.ID, "Scope team")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.Invite(ctx, f.actor.ID, team.ID, bob.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.AcceptInvitation(ctx, bob.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	ownerKey := createScopeTestKey(t, f, team.ID, "product-alice")
	memberKey := createScopeTestKey(t, f, team.ID, "product-bob")
	path := func(id string) string { return "/v1/accounts/" + team.ID + "/api-keys/" + id + "/models" }
	body := map[string]any{"models": []string{"embedding"}}
	f.request("POST", path(ownerKey.ID), "product-bob", body, "", 403, nil)
	f.request("POST", path(memberKey.ID), "product-bob", body, "", 200, nil)
	f.request("POST", path(memberKey.ID), "product-alice", map[string]any{"models": []string{"rerank"}}, "", 200, nil)
	if err = a.ChangeRole(ctx, f.actor.ID, team.ID, bob.ID, "billing_admin"); err != nil {
		t.Fatal(err)
	}
	f.request("POST", path(ownerKey.ID), "product-bob", body, "", 200, nil)
	f.request("POST", "/v1/accounts/"+f.account+"/api-keys/"+ownerKey.ID+"/models", "product-alice", body, "", 404, nil)
	f.request("POST", path(ownerKey.ID), "alice", body, "", 400, nil)
	f.request("POST", path(ownerKey.ID), "alice", map[string]any{"models": []string{"image"}, "origin_product_id": "superwagie", "instance_id": "default"}, "", 200, nil)
	if err = a.RemoveMember(ctx, f.actor.ID, team.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	f.request("POST", path(memberKey.ID), "product-bob", body, "", 403, nil)
	f.request("POST", path(memberKey.ID), "product-alice", map[string]any{"models": []string{"ocr"}}, "", 403, nil)
}

func TestAPIKeyModelsAppendSerializesConcurrentAdditions(t *testing.T) {
	f := newAPIKeyModelsFixture(t)
	key := createScopeTestKey(t, f, f.account, "product-alice")
	path := "/v1/accounts/" + f.account + "/api-keys/" + key.ID + "/models"
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for _, model := range []string{"embedding", "rerank", "image", "ocr", "vision", "embedding"} {
		wg.Add(1)
		go func(model string) {
			defer wg.Done()
			data, _ := json.Marshal(map[string]any{"models": []string{model}})
			r, _ := http.NewRequest("POST", f.server.URL+path, bytes.NewReader(data))
			r.Header.Set("Authorization", "Bearer product-alice")
			r.Header.Set("Content-Type", "application/json")
			resp, err := f.client.Do(r)
			if err != nil {
				results <- err
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != 200 {
				results <- fmt.Errorf("append returned %d", resp.StatusCode)
				return
			}
			results <- nil
		}(model)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var keys []modelaccess.APIKey
	f.request("GET", "/v1/accounts/"+f.account+"/api-keys", "product-alice", nil, "", 200, &keys)
	if len(keys) != 1 || !slices.Equal(keys[0].Models, []string{"chat", "embedding", "image", "ocr", "rerank", "vision"}) {
		t.Fatal("concurrent append lost an allowed model")
	}
	var audits int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='model_credential.models_added'`).Scan(&audits); err != nil || audits != 5 {
		t.Fatalf("concurrent audit count=%d error=%v", audits, err)
	}
}

func TestAPIKeyModelsRouteRequiresWriteScopeAndFinanceCredentials(t *testing.T) {
	f := newAPIKeyModelsFixture(t)
	key := createScopeTestKey(t, f, f.account, "product-alice")
	a := accounts.New(f.pool)
	policy := products.Client{ClientID: "sw", ProductID: "superwagie", Scopes: []string{"catalog:read"}}
	h := New(verifier{}, a, f.wallet, Options{Billing: f.billing, ModelAccess: modelaccess.New(f.pool, a, f.billing), ProductVerifiers: map[string]auth.Verifier{"sw": anyProductVerifier{}}, ProductAccess: map[string]products.Client{"sw": policy}, ProductRuntime: map[string]RuntimeService{"sw": {Token: strings.Repeat("s", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: "sw", AllowedOps: []string{"finance:write"}}}})
	for _, tc := range []struct {
		prefix, token string
		want          int
	}{{"/v1", "product-alice", 403}, {"/internal/v1/product-finance", strings.Repeat("s", 40), 401}} {
		r := httptest.NewRequest("POST", tc.prefix+"/accounts/"+f.account+"/api-keys/"+key.ID+"/models", strings.NewReader(`{"models":["embedding"]}`))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("scope boundary returned %d; want %d", w.Code, tc.want)
		}
	}
}

func TestAPIKeyModelsAppendWaitsForProductAccessRevocation(t *testing.T) {
	for _, lock := range []string{"unihub:platform-administration", "unihub:user-administration", "product-auth:superwagie/default", "runtime-access:superwagie/default", "assignment-row"} {
		t.Run(lock, func(t *testing.T) {
			f := newAPIKeyModelsFixture(t)
			key := createScopeTestKey(t, f, f.account, "product-alice")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if lock == "assignment-row" {
				if _, err := f.pool.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,'superwagie','default','["member"]',true,1)`, f.actor.ID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Rollback(tx)
			if lock != "assignment-row" {
				if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lock); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = tx.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,'superwagie','default','["member"]',false,1) ON CONFLICT(user_id,product_id,instance_id) DO UPDATE SET active=false`, f.actor.ID); err != nil {
				t.Fatal(err)
			}
			access := modelaccess.New(f.pool, accounts.New(f.pool), f.billing)
			result := make(chan error, 1)
			go func() {
				_, err := access.AddAPIKeyModels(ctx, f.actor.ID, f.account, key.ID, modelaccess.APIKeyModelsRequest{OriginProductID: "superwagie", InstanceID: "default", Models: []string{"embedding"}})
				result <- err
			}()
			waitForScopeBlock(t, ctx, f.pool, tx, result)
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; err != accounts.ErrForbidden {
				t.Fatalf("access revoked while waiting: %v", err)
			}
		})
	}
}

func TestAPIKeyModelsAppendUsesOneConnectionAndRollsBackAuditFailure(t *testing.T) {
	f := newAPIKeyModelsFixture(t)
	key := createScopeTestKey(t, f, f.account, "product-alice")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := f.pool.Config()
	cfg.MaxConns = 1
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	a := accounts.New(p)
	access := modelaccess.New(p, a, billing.New(p, a, nil))
	req := modelaccess.APIKeyModelsRequest{OriginProductID: "superwagie", InstanceID: "default", Models: []string{"embedding"}}
	if _, err = access.AddAPIKeyModels(ctx, f.actor.ID, f.account, key.ID, req); err != nil {
		t.Fatalf("single-connection append failed: %v", err)
	}
	if _, err = p.Exec(ctx, `CREATE FUNCTION reject_scope_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='model_credential.models_added' THEN RAISE EXCEPTION 'test audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_scope_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_scope_audit()`); err != nil {
		t.Fatal(err)
	}
	req.Models = []string{"rerank"}
	if _, err = access.AddAPIKeyModels(ctx, f.actor.ID, f.account, key.ID, req); err == nil {
		t.Fatal("scope change committed without audit")
	}
	var keys []modelaccess.APIKey
	f.request("GET", "/v1/accounts/"+f.account+"/api-keys", "product-alice", nil, "", 200, &keys)
	if len(keys) != 1 || !slices.Equal(keys[0].Models, []string{"chat", "embedding"}) {
		t.Fatal("failed audit left a broader key")
	}
}

func TestAPIKeyModelsAppendRejectsWrongAssetAmbiguityAndTaskKeys(t *testing.T) {
	for _, kind := range []string{"wrong-asset", "ambiguous", "task", "disabled-product"} {
		t.Run(kind, func(t *testing.T) {
			f := newAPIKeyModelsFixture(t)
			key := createScopeTestKey(t, f, f.account, "product-alice")
			ctx := context.Background()
			want := 403
			switch kind {
			case "wrong-asset", "ambiguous":
				rates, err := f.billing.Rates(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, rate := range rates {
					if rate.Model == "embedding" {
						rate.ID = "extra-embedding"
						if kind == "wrong-asset" {
							if err = f.billing.RetireRate(ctx, f.actor.ID, "rate-embedding"); err != nil {
								t.Fatal(err)
							}
							rate.Asset = billing.CreditAsset
						}
						if _, err = f.billing.PublishRate(ctx, f.actor.ID, rate); err != nil {
							t.Fatal(err)
						}
						break
					}
				}
			case "task":
				grant, err := modelaccess.New(f.pool, accounts.New(f.pool), f.billing).Issue(ctx, f.actor.ID, f.account, modelaccess.Request{OriginProductID: "superwagie", Models: []string{"chat"}, TaskID: "task-scope"})
				if err != nil {
					t.Fatal(err)
				}
				key.ID = grant.ID
				want = 404
			case "disabled-product":
				if _, err := f.pool.Exec(ctx, `UPDATE products SET enabled=false WHERE id='superwagie'`); err != nil {
					t.Fatal(err)
				}
			}
			f.request("POST", "/v1/accounts/"+f.account+"/api-keys/"+key.ID+"/models", "product-alice", map[string]any{"models": []string{"embedding"}}, "", want, nil)
			var audits int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='model_credential.models_added'`).Scan(&audits); err != nil || audits != 0 {
				t.Fatalf("rejection audit count=%d error=%v", audits, err)
			}
		})
	}
}

func TestAPIKeyModelsFinanceAppendUsesSameAuthorityAndCredential(t *testing.T) {
	f := newAPIKeyModelsFixture(t)
	ctx := context.Background()
	a := accounts.New(f.pool)
	reg := productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}}}
	users := productusers.New(f.pool, reg, "https://hub.test", nil)
	pa := productauth.New(f.pool, a, users, "https://hub.test")
	registered, err := pa.Register(ctx, "superwagie", "default", productauth.Registration{LocalUserID: "scope-user", OperationID: "scope-registration", Username: "scope-user", AuthVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := pa.Authenticate(ctx, "superwagie", "default", productauth.Authentication{LocalUserID: "scope-user", AuthenticationID: "scope-authentication", Method: "session", AuthenticatedAt: time.Now().Unix(), AuthVersion: registered.AuthVersion})
	if err != nil {
		t.Fatal(err)
	}
	access := modelaccess.New(f.pool, a, f.billing)
	access.ConfigureProductUsers(users)
	key, err := access.CreateAPIKey(ctx, identity.Binding.UserID, identity.Binding.PersonalAccountID, modelaccess.APIKeyRequest{Name: "Native key", OriginProductID: "superwagie", InstanceID: "default", Models: []string{"chat"}})
	if err != nil {
		t.Fatal(err)
	}
	policy := products.Client{ClientID: "sw-finance", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"model-credentials:write"}}
	h := New(verifier{}, a, f.wallet, Options{Billing: f.billing, ModelAccess: access, ProductUsers: users, ProductAuth: pa, ProductAccess: map[string]products.Client{policy.ClientID: policy}, ProductRuntime: map[string]RuntimeService{"write": {Token: strings.Repeat("s", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: policy.ClientID, AllowedOps: []string{"finance:write"}}, "read": {Token: strings.Repeat("r", 40), ProductID: "superwagie", InstanceID: "default", FinanceClientID: policy.ClientID, AllowedOps: []string{"finance:read"}}}})
	path := "/internal/v1/product-finance/accounts/" + identity.Binding.PersonalAccountID + "/api-keys/" + key.ID + "/models"
	for _, tc := range []struct {
		service, actor string
		want           int
	}{{strings.Repeat("r", 40), identity.ActorToken, 403}, {"", identity.ActorToken, 401}, {strings.Repeat("s", 40), identity.ActorToken, 200}} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"models":["embedding"]}`))
		if tc.service != "" {
			r.Header.Set("Authorization", "Bearer "+tc.service)
		}
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+tc.actor)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("finance scope status=%d body=%s", w.Code, w.Body)
		}
		if w.Code == 200 {
			var got modelaccess.APIKey
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Token != "" || got.ID != key.ID || !slices.Equal(got.Models, []string{"chat", "embedding"}) {
				t.Fatal("finance append changed credential or returned unsafe metadata")
			}
		}
	}
	if _, err = access.VerifyForInstance(ctx, key.Token, "embedding", "default"); err != nil {
		t.Fatalf("original key cannot use added model: %v", err)
	}
}

func waitForScopeBlock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker pgx.Tx, result <-chan error) {
	t.Helper()
	var pid uint32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-result:
			t.Fatalf("scope expansion bypassed concurrent policy mutation: %v", err)
		case <-ctx.Done():
			t.Fatal("scope expansion did not wait on policy mutation")
		case <-tick.C:
			var blocked bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				return
			}
		}
	}
}

func TestAPIKeyModelsAppendRechecksExpiryAfterCatalogWait(t *testing.T) {
	f := newAPIKeyModelsFixture(t)
	key := createScopeTestKey(t, f, f.account, "product-alice")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, `UPDATE model_credentials SET expires_at=now()+interval '1 second' WHERE id=$1`, key.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:billing-rate-catalog',0))`); err != nil {
		t.Fatal(err)
	}
	access := modelaccess.New(f.pool, accounts.New(f.pool), f.billing)
	result := make(chan error, 1)
	go func() {
		_, err := access.AddAPIKeyModels(ctx, f.actor.ID, f.account, key.ID, modelaccess.APIKeyModelsRequest{OriginProductID: "superwagie", InstanceID: "default", Models: []string{"embedding"}})
		result <- err
	}()
	waitForScopeBlock(t, ctx, f.pool, tx, result)
	if _, err = tx.Exec(ctx, `SELECT pg_sleep(1.1)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != accounts.ErrForbidden {
		t.Fatalf("expired while waiting for catalog: %v", err)
	}
}
