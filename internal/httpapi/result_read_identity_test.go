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
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestResultReadRequiresLiveProductAccessToken(t *testing.T) {
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
	if _, err := billingService.PublishRate(ctx, user.ID, billing.Rate{ID: "embedding-rate", Model: "x", Asset: billing.CashAsset, Semantics: "metered_v2", Metered: &metering.Price{Version: "metered_v2", Operation: "embedding", Policy: "provider_reported_v1", Terms: []metering.Term{{Key: "input", Unit: "token", Price: metering.Rational{Numerator: "2", Denominator: "1"}}}}}); err != nil {
		t.Fatal(err)
	}
	access := modelaccess.New(pool, store, billingService)
	grant, err := access.Issue(ctx, user.ID, list[0].ID, modelaccess.Request{OriginProductID: "superwagie", Models: []string{"x"}, TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	registry := productusers.Registry{"superwagie/default": {ProductID: "superwagie", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Permissions: []string{"product:use"}}}}}
	users := productusers.New(pool, registry, issuer.URL, nil)
	identity := &profileFixture{}
	const keyService = "test-only-router-service-key-over-32-bytes"
	h := New(verifier{}, store, wallet.New(pool, store, noLedger{}), Options{ModelAccess: access, Billing: billingService, ServiceToken: keyService, Identity: identity, ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"sw-client": oidc}, ProductAccess: map[string]products.Client{"sw-client": {ClientID: "sw-client", ProductID: "superwagie", InstanceID: "default", Scopes: []string{"access:read"}}}})
	body, _ := json.Marshal(map[string]string{"token": grant.Token, "model": "x", "wire_operation": "embeddings.create", "instance_id": "default", "purpose": "result_read"})
	request := func(actor, service string, duplicate bool) int {
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
		return w.Code
	}
	good := token("alice", "access-token", "sw-client", time.Now().Add(time.Hour))
	cases := []struct {
		name, actor, service string
		duplicate            bool
		want                 int
	}{
		{"valid", good, keyService, false, 200},
		{"missing", "", keyService, false, 401},
		{"id token", token("alice", "id-token", "sw-client", time.Now().Add(time.Hour)), keyService, false, 401},
		{"refresh token", token("alice", "refresh-token", "sw-client", time.Now().Add(time.Hour)), keyService, false, 401},
		{"wrong actor", token("bob", "access-token", "sw-client", time.Now().Add(time.Hour)), keyService, false, 403},
		{"expired", token("alice", "access-token", "sw-client", time.Now().Add(-time.Minute)), keyService, false, 401},
		{"wrong audience", token("alice", "access-token", "other", time.Now().Add(time.Hour)), keyService, false, 401},
		{"duplicate", good, keyService, true, 401},
		{"untrusted gateway", good, "wrong-service-key", false, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := request(tc.actor, tc.service, tc.duplicate); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
	// Retiring a tariff prevents new dispatch, but a still-authorized actor
	// must retain access to the result of an already admitted task.
	if err := billingService.RetireRate(ctx, user.ID, "embedding-rate"); err != nil {
		t.Fatal(err)
	}
	if got := request(good, keyService, false); got != 200 {
		t.Fatalf("result denied after rate retirement: %d", got)
	}
	body, _ = json.Marshal(map[string]string{"token": grant.Token, "model": "x", "wire_operation": "embeddings.create", "instance_id": "default"})
	if got := request(good, keyService, false); got != 403 {
		t.Fatalf("new dispatch accepted retired rate: %d", got)
	}
	body, _ = json.Marshal(map[string]string{"token": grant.Token, "model": "x", "wire_operation": "embeddings.create", "instance_id": "default", "purpose": "result_read"})
	identity.revoked = true
	if got := request(good, keyService, false); got != 401 {
		t.Fatalf("revoked login accepted: %d", got)
	}
	identity.revoked = false
	if _, err := pool.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,product_role,version) VALUES($1,'superwagie','default','["member"]',false,1,1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(good, keyService, false); got != 403 {
		t.Fatalf("disabled product accepted: %d", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE product_user_access SET active=true WHERE user_id=$1 AND product_id='superwagie' AND instance_id='default'`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM account_members WHERE account_id=$1 AND user_id=$2`, list[0].ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(good, keyService, false); got != 403 {
		t.Fatalf("removed member accepted: %d", got)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'owner')`, list[0].ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := access.Revoke(ctx, user.ID, list[0].ID, grant.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(good, keyService, false); got != 403 {
		t.Fatalf("revoked capability accepted: %d", got)
	}
}
