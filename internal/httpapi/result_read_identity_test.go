package httpapi

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/metering"
	"unihub/internal/modelaccess"
	"unihub/internal/platformadmin"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestResultReadRequiresLiveProductAccessToken(t *testing.T) {
	testTaskActorVerification(t, "result_read", "x", "embeddings.create", "embedding")
}

func TestKnowledgeDispatchRequiresCurrentPaidProductAccess(t *testing.T) {
	for _, tc := range []struct{ model, wire, operation string }{
		{"unihub-text-embedding-v4", "embeddings.create", "embedding"},
		{"qwen3-rerank", "rerank.create", "rerank"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			testTaskActorVerification(t, "knowledge_dispatch", tc.model, tc.wire, tc.operation)
		})
	}
}

// Share the real signed OIDC, current product authority, and isolated PostgreSQL
// fixture so both purposes prove the same original access-token prerequisite.
func testTaskActorVerification(t *testing.T, purpose, model, wire, operation string) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	oidc, err := auth.NewOIDC(ctx, issuer.URL, "sw-client")
	if err != nil {
		t.Fatal(err)
	}
	token := func(subject, kind, audience string, exp time.Time) string {
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key"})
		claims, _ := json.Marshal(map[string]any{"iss": issuer.URL, "sub": subject, "aud": audience, "iat": time.Now().Add(-time.Minute).Unix(), "exp": exp.Unix(), "tokenType": kind, "scope": "openid"})
		input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
		sum := sha256.Sum256([]byte(input))
		signature, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		if e != nil {
			t.Fatal(e)
		}
		return input + "." + base64.RawURLEncoding.EncodeToString(signature)
	}
	store := accounts.New(pool)
	user, err := store.EnsureIdentity(ctx, accounts.Identity{Issuer: issuer.URL, Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.ListAccounts(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	billingService := billing.New(pool, store, noLedger{})
	billingService.ConfigureOperators([]string{user.ID})
	if _, err := billingService.SaveProfile(ctx, user.ID, "superwagie", billing.BillingProfile{ProductID: "superwagie", Usage: billing.UsagePolicy{Enabled: true, FundingModes: []string{billing.FundingPrepaidCash}, DefaultFundingMode: billing.FundingPrepaidCash}}); err != nil {
		t.Fatal(err)
	}
	if _, err := billingService.PublishRate(ctx, user.ID, billing.Rate{ID: "embedding-rate", Model: model, Asset: billing.CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: operation, Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "2", Denominator: "1"}}}}}); err != nil {
		t.Fatal(err)
	}
	access := modelaccess.New(pool, store, billingService)
	grant, err := access.Issue(ctx, user.ID, list[0].ID, modelaccess.Request{OriginProductID: "superwagie", Models: []string{model}, TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	registry := productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Permissions: []string{"product:use"}}}}}
	users := productusers.New(pool, registry, issuer.URL, nil)
	identity := &profileFixture{}
	const keyService = "test-only-router-service-key-over-32-bytes"
	options := Options{PlatformAdmin: platformadmin.New(pool, nil), ModelAccess: access, Billing: billingService, ServiceToken: keyService, Identity: identity, ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"sw-client": oidc}, ProductAccess: map[string]products.Client{"sw-client": {ClientID: "sw-client", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"access:read"}}}}
	h := New(verifier{}, store, wallet.New(pool, store, noLedger{}), options)
	body, _ := json.Marshal(map[string]string{"token": grant.Token, "model": model, "wire_operation": wire, "instance_id": "default", "purpose": purpose})
	request := func(actor, service string, duplicate bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/internal/v1/model-credentials/verify", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+service)
		r.Header.Set("Content-Type", "application/json")
		if actor != "" {
			r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
		}
		if duplicate {
			r.Header.Add("X-UniHub-Actor-Authorization", "Bearer "+actor)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			var denied map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &denied); err != nil {
				t.Fatal(err)
			}
			if _, ok := denied["actor_expires_at"]; ok {
				t.Fatal("denied response disclosed actor expiry")
			}
		}
		return w
	}
	var credentialExpiry time.Time
	if err := pool.QueryRow(ctx, `SELECT expires_at FROM model_credentials WHERE id=$1`, grant.ID).Scan(&credentialExpiry); err != nil {
		t.Fatal(err)
	}
	actorExpiry := time.Now().Add(time.Hour).Truncate(time.Second)
	if actorExpiry.Unix() == credentialExpiry.Unix() {
		t.Fatal("fixture requires distinct actor and model credential expiries")
	}
	assertExpiry := func(t *testing.T, w *httptest.ResponseRecorder, wantActor int64) {
		t.Helper()
		var response struct {
			modelaccess.Claims
			ActorExpiresAt *int64 `json:"actor_expires_at"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if wantActor == 0 {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if _, ok := fields["actor_expires_at"]; ok {
				t.Fatal("ordinary credential response disclosed actor expiry")
			}
		} else if response.ActorExpiresAt == nil || *response.ActorExpiresAt != wantActor {
			t.Fatalf("actor expiry = %v, want signed access-token exp %d", response.ActorExpiresAt, wantActor)
		}
		if response.Audience != "llmrouter" || response.CredentialKind != "task" || response.ID != grant.ID || response.ActorID != user.ID || response.AccountID != list[0].ID || response.OriginProductID != "superwagie" || response.InstanceID != "default" || response.TaskID != "task" {
			t.Fatalf("verification changed original credential claims: %+v", response.Claims)
		}
		if ops := response.Operations[model]; len(ops) != 1 || ops[0] != wire {
			t.Fatalf("incorrect fixed route authority: %v", response.Operations)
		}
		if !response.ExpiresAt.Equal(credentialExpiry) {
			t.Fatalf("model credential expiry changed: got %v want %v", response.ExpiresAt, credentialExpiry)
		}
	}
	good := token("alice", "access-token", "sw-client", actorExpiry)
	cases := []struct {
		name, actor, service string
		duplicate            bool
		want                 int
	}{
		{"valid", good, keyService, false, 200},
		{"missing", "", keyService, false, 401},
		{"invalid", "invalid-access-token", keyService, false, 401},
		{"platform API key", "upa_test", keyService, false, 401},
		{"product business key", "ubp_test", keyService, false, 401},
		{"zero expiry", token("alice", "access-token", "sw-client", time.Unix(0, 0)), keyService, false, 401},
		{"negative expiry", token("alice", "access-token", "sw-client", time.Unix(-1, 0)), keyService, false, 401},
		{"id token", token("alice", "id-token", "sw-client", time.Now().Add(time.Hour)), keyService, false, 401},
		{"refresh token", token("alice", "refresh-token", "sw-client", time.Now().Add(time.Hour)), keyService, false, 401},
		{"wrong actor", token("bob", "access-token", "sw-client", time.Now().Add(time.Hour)), keyService, false, 403},
		{"expired", token("alice", "access-token", "sw-client", time.Now().Add(-time.Minute)), keyService, false, 401},
		{"wrong audience", token("alice", "access-token", "other", time.Now().Add(time.Hour)), keyService, false, 401},
		{"duplicate", good, keyService, true, 401},
		{"untrusted gateway", good, "wrong-service-key", false, 401},
		{"missing service", good, "", false, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(tc.actor, tc.service, tc.duplicate)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
			if w.Code == http.StatusOK {
				assertExpiry(t, w, actorExpiry.Unix())
			}
		})
	}
	if purpose == "knowledge_dispatch" {
		setBody := func(token, requestedModel, instance, requestedWire string) {
			body, _ = json.Marshal(map[string]string{"token": token, "model": requestedModel, "wire_operation": requestedWire, "instance_id": instance, "purpose": purpose})
		}
		for _, tc := range []struct{ name, token, model, instance, wire string }{
			{"missing credential", "", model, "default", wire},
			{"missing instance", grant.Token, model, "", wire},
			{"wrong instance", grant.Token, model, "other", wire},
			{"wrong model", grant.Token, "other-model", "default", wire},
			{"wrong wire", grant.Token, model, "default", "responses.create"},
			{"operation is not wire", grant.Token, "qwen3-rerank", "default", "rerank"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				setBody(tc.token, tc.model, tc.instance, tc.wire)
				if w := request(good, keyService, false); w.Code != 403 {
					t.Fatalf("invalid scope status %d: %s", w.Code, w.Body.String())
				}
			})
		}
		setBody(grant.Token, model, "default", wire)
		for _, tc := range []struct {
			name   string
			policy products.Client
		}{
			{"wrong configured client", products.Client{ClientID: "other", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"access:read"}}},
			{"wrong configured product", products.Client{ClientID: "sw-client", ProductID: "other", InstanceID: "default", Scopes: []string{"access:read"}}},
			{"wrong configured instance", products.Client{ClientID: "sw-client", ProductID: "superwagie", InstanceID: "other", Scopes: []string{"access:read"}}},
			{"missing configured scope", products.Client{ClientID: "sw-client", ProductID: "superwagie", InstanceID: "default"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				changed := options
				changed.ProductAccess = map[string]products.Client{"sw-client": tc.policy}
				h = New(verifier{}, store, wallet.New(pool, store, noLedger{}), changed)
				defer func() { h = New(verifier{}, store, wallet.New(pool, store, noLedger{}), options) }()
				if w := request(good, keyService, false); w.Code != 401 {
					t.Fatalf("configured actor scope status %d: %s", w.Code, w.Body.String())
				}
			})
		}
		t.Run("duplicate matching verifier", func(t *testing.T) {
			changed := options
			changed.ProductVerifiers = map[string]auth.Verifier{"sw-client": oidc, "second": oidc}
			changed.ProductAccess = map[string]products.Client{
				"sw-client": options.ProductAccess["sw-client"],
				"second":    {ClientID: "second", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"access:read"}},
			}
			h = New(verifier{}, store, wallet.New(pool, store, noLedger{}), changed)
			defer func() { h = New(verifier{}, store, wallet.New(pool, store, noLedger{}), options) }()
			if w := request(good, keyService, false); w.Code != 401 {
				t.Fatalf("ambiguous actor verifier status %d: %s", w.Code, w.Body.String())
			}
		})
		t.Run("duplicate service bearer", func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/internal/v1/model-credentials/verify", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+keyService)
			r.Header.Add("Authorization", "Bearer "+keyService)
			r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+good)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("duplicate service bearer status %d", w.Code)
			}
		})
		t.Run("unavailable platform cutoff authority", func(t *testing.T) {
			closed, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
			if err != nil {
				t.Fatal(err)
			}
			closed.Close()
			changed := options
			changed.PlatformAdmin = platformadmin.New(closed, nil)
			h = New(verifier{}, store, wallet.New(pool, store, noLedger{}), changed)
			defer func() { h = New(verifier{}, store, wallet.New(pool, store, noLedger{}), options) }()
			if w := request(good, keyService, false); w.Code != 503 {
				t.Fatalf("platform authority fault status %d: %s", w.Code, w.Body.String())
			}
		})
		exec := func(t *testing.T, query string, args ...any) {
			t.Helper()
			if _, err := pool.Exec(ctx, query, args...); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			name, change, restore string
			args                  []any
			want                  int
		}{
			{"wrong credential kind", `UPDATE model_credentials SET credential_kind='api_key' WHERE id=$1`, `UPDATE model_credentials SET credential_kind='task' WHERE id=$1`, []any{grant.ID}, 403},
			{"expired credential", `UPDATE model_credentials SET expires_at=now()-interval '1 second' WHERE id=$1`, `UPDATE model_credentials SET expires_at=$2 WHERE id=$1`, []any{grant.ID, credentialExpiry}, 403},
			{"revoked credential", `UPDATE model_credentials SET revoked_at=now() WHERE id=$1`, `UPDATE model_credentials SET revoked_at=NULL WHERE id=$1`, []any{grant.ID}, 403},
			{"inactive actor", `UPDATE users SET active=false WHERE id=$1`, `UPDATE users SET active=true WHERE id=$1`, []any{user.ID}, 403},
			{"global session cutoff", `UPDATE users SET session_not_before=now() WHERE id=$1`, `UPDATE users SET session_not_before='epoch' WHERE id=$1`, []any{user.ID}, 403},
			{"removed membership", `DELETE FROM account_members WHERE user_id=$1`, `INSERT INTO account_members(user_id,account_id,role) VALUES($1,$2,'owner')`, []any{user.ID, list[0].ID}, 403},
			{"product use denied", `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,product_role,version,permission_overrides) VALUES($1,'superwagie','default','["member"]',true,1,1,'{"product":{"use":false}}')`, `DELETE FROM product_user_access WHERE user_id=$1`, []any{user.ID}, 403},
			{"product disabled", `UPDATE products SET enabled=false WHERE id='superwagie' AND $1::text<>''`, `UPDATE products SET enabled=true WHERE id='superwagie' AND $1::text<>''`, []any{user.ID}, 403},
		} {
			t.Run(tc.name, func(t *testing.T) {
				// The change only uses its first argument; restores may need the
				// immutable original account or expiry as a second argument.
				exec(t, tc.change, tc.args[0])
				defer exec(t, tc.restore, tc.args...)
				if w := request(good, keyService, false); w.Code != tc.want {
					t.Fatalf("current authority status %d want %d: %s", w.Code, tc.want, w.Body.String())
				}
			})
		}
		t.Run("current billing eligibility denied", func(t *testing.T) {
			profile, err := billingService.Profile(ctx, "superwagie")
			if err != nil {
				t.Fatal(err)
			}
			changed := profile
			changed.Usage = billing.UsagePolicy{}
			saved, err := billingService.SaveProfile(ctx, user.ID, "superwagie", changed)
			if err != nil {
				t.Fatal(err)
			}
			profile.Version = saved.Version
			defer func() {
				if _, err := billingService.SaveProfile(ctx, user.ID, "superwagie", profile); err != nil {
					t.Fatal(err)
				}
			}()
			if w := request(good, keyService, false); w.Code != 403 {
				t.Fatalf("eligibility denial status %d: %s", w.Code, w.Body.String())
			}
		})
		identity.revoked = true
		if w := request(good, keyService, false); w.Code != 401 {
			t.Fatalf("revoked access: %d", w.Code)
		}
		identity.revoked = false
		// A table rename belongs only to this disposable schema and exposes
		// real database faults at the task, eligibility, and actor checks.
		for _, table := range []string{"model_credentials", "users", "products", "account_members", "product_billing_profiles", "billing_rates", "identities", "product_user_access"} {
			t.Run("unavailable authority/"+table, func(t *testing.T) {
				exec(t, `ALTER TABLE `+table+` RENAME TO unavailable_authority`)
				defer exec(t, `ALTER TABLE unavailable_authority RENAME TO `+table)
				if w := request(good, keyService, false); w.Code != 503 {
					t.Fatalf("authority fault status %d want 503: %s", w.Code, w.Body.String())
				}
			})
		}
		var credentials, calls int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM model_credentials),(SELECT count(*) FROM billing_calls)`).Scan(&credentials, &calls); err != nil {
			t.Fatal(err)
		}
		if credentials != 1 || calls != 0 {
			t.Fatalf("verification changed effects: credentials=%d calls=%d", credentials, calls)
		}
	}
	// Ordinary verification and receipt keep the credential-only response,
	// even when the caller also supplies a valid actor access token.
	for _, purpose := range []string{"", "receipt"} {
		t.Run("credential response/"+purpose, func(t *testing.T) {
			body, _ = json.Marshal(map[string]string{"token": grant.Token, "model": model, "wire_operation": wire, "instance_id": "default", "purpose": purpose})
			w := request(good, keyService, false)
			if w.Code != http.StatusOK {
				t.Fatalf("credential verification denied: %d", w.Code)
			}
			assertExpiry(t, w, 0)
		})
	}
	body, _ = json.Marshal(map[string]string{"token": grant.Token, "model": model, "wire_operation": wire, "instance_id": "default", "purpose": purpose})
	// Retiring a tariff prevents new dispatch, but a still-authorized actor
	// must retain access to the result of an already admitted task.
	if err := billingService.RetireRate(ctx, user.ID, "embedding-rate"); err != nil {
		t.Fatal(err)
	}
	w := request(good, keyService, false)
	want := http.StatusOK
	if purpose == "knowledge_dispatch" {
		want = http.StatusForbidden
	}
	if w.Code != want {
		t.Fatalf("%s after rate retirement: got %d want %d", purpose, w.Code, want)
	}
	if w.Code == http.StatusOK {
		assertExpiry(t, w, actorExpiry.Unix())
	}
	// The original result-read authority stays available after tariff retirement.
	body, _ = json.Marshal(map[string]string{"token": grant.Token, "model": model, "wire_operation": wire, "instance_id": "default", "purpose": "result_read"})
	w = request(good, keyService, false)
	if w.Code != http.StatusOK {
		t.Fatalf("result denied after rate retirement: %d", w.Code)
	}
	assertExpiry(t, w, actorExpiry.Unix())
	body, _ = json.Marshal(map[string]string{"token": grant.Token, "model": model, "wire_operation": wire, "instance_id": "default"})
	if got := request(good, keyService, false).Code; got != 403 {
		t.Fatalf("new dispatch accepted retired rate: %d", got)
	}
	body, _ = json.Marshal(map[string]string{"token": grant.Token, "model": model, "wire_operation": wire, "instance_id": "default", "purpose": "result_read"})
	identity.revoked = true
	if got := request(good, keyService, false).Code; got != 401 {
		t.Fatalf("revoked login accepted: %d", got)
	}
	identity.revoked = false
	if _, err := pool.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,product_role,version) VALUES($1,'superwagie','default','["member"]',false,1,1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(good, keyService, false).Code; got != 403 {
		t.Fatalf("disabled product accepted: %d", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE product_user_access SET active=true WHERE user_id=$1 AND product_id='superwagie' AND instance_id='default'`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM account_members WHERE account_id=$1 AND user_id=$2`, list[0].ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(good, keyService, false).Code; got != 403 {
		t.Fatalf("removed member accepted: %d", got)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'owner')`, list[0].ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := access.Revoke(ctx, user.ID, list[0].ID, grant.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(good, keyService, false).Code; got != 403 {
		t.Fatalf("revoked capability accepted: %d", got)
	}
}
