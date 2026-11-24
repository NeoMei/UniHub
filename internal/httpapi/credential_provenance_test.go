package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/console"
	"unihub/internal/database"
	"unihub/internal/identityapi"
	"unihub/internal/modelaccess"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestCredentialMutationsUseVerifiedAuthenticationProvenance(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	require.NoError(t, database.Migrate(ctx, pool))
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			t.Errorf("unexpected identity provider request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize",
			"token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks",
		})
	}))
	t.Cleanup(issuer.Close)
	const hub = "https://hub.example"
	const brokerIssuer = hub + "/product-broker"
	store := accounts.New(pool)
	user, err := store.EnsureIdentity(ctx, accounts.Identity{Issuer: issuer.URL, Subject: "linked", Name: "Linked"})
	require.NoError(t, err)
	owned, err := store.ListAccounts(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, owned, 1)
	_, err = pool.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$2)`, brokerIssuer, user.ID)
	require.NoError(t, err)

	const product = "superwagie"
	users := productusers.New(pool, productusers.Registry{"superwagie/default": {
		ProductID: product, InstanceID: "default", Version: 1,
		Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}},
	}}, hub, nil)
	binding, err := users.EnsureRuntimeBinding(ctx, product, "default", "native-linked", "linked-fixture",
		accounts.Identity{Issuer: issuer.URL, Subject: "linked"}, user)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO product_auth_subjects(binding_id,auth_version,registration_key,registration_digest)
		VALUES($1,1,'linked-fixture','fixture')`, binding.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version)
		VALUES($1,'superwagie','default','["member"]',true,1)`, user.ID)
	require.NoError(t, err)

	const csrf = "credential-provenance-fixture"
	centralCookie := strings.Repeat("c", 43)
	brokerCookie := strings.Repeat("b", 43)
	for _, session := range []struct{ cookie, issuer, subject, binding string }{
		{centralCookie, issuer.URL, "linked", ""},
		{brokerCookie, brokerIssuer, user.ID, binding.ID},
	} {
		hash := sha256.Sum256([]byte(session.cookie))
		id := hex.EncodeToString(hash[:])
		_, err = pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at)
			VALUES($1,$2,$3,'Linked','',$4,now()+interval '1 hour')`, id, session.issuer, session.subject, csrf)
		require.NoError(t, err)
		if session.binding != "" {
			_, err = pool.Exec(ctx, `INSERT INTO product_browser_session_origins(session_id_hash,binding_id,auth_version) VALUES($1,$2,1)`, id, session.binding)
			require.NoError(t, err)
		}
	}

	provider, err := identityapi.New(ctx, pool, identityapi.Options{Issuer: issuer.URL})
	require.NoError(t, err)
	browser, err := console.New(ctx, pool, verifier{}, console.Options{Issuer: issuer.URL, ClientID: "console", PublicURL: hub, ProductUsers: users})
	require.NoError(t, err)
	nativeAuth := productauth.New(pool, store, users, hub)
	policy := products.Client{ClientID: "finance-fixture", ProductID: product, InstanceID: "default", Scopes: []string{"model-credentials:write"}}
	serviceToken := strings.Repeat("s", 40)
	api := New(verifier{}, store, wallet.New(pool, store, noLedger{}), Options{
		Identity: provider, ProductUsers: users, ProductAuth: nativeAuth,
		Billing: billing.New(pool, store, noLedger{}), ModelAccess: modelaccess.New(pool, store, productEligibility{}),
		ProductAccess: map[string]products.Client{policy.ClientID: policy},
		ProductRuntime: map[string]RuntimeService{"finance-fixture": {
			Token: serviceToken, ProductID: product, InstanceID: "default",
			FinanceClientID: policy.ClientID, AllowedOps: []string{"finance:write"},
		}},
	})
	h := browser.Handler(api)
	browserRequest := func(method, path, cookie, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, hub+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", hub)
		r.Header.Set("X-CSRF-Token", csrf)
		r.AddCookie(&http.Cookie{Name: "unihub_session", Value: cookie})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, session := range []struct {
		name, cookie string
		signedIn     bool
		canManage    bool
	}{
		{name: "central", cookie: centralCookie, signedIn: true, canManage: true},
		{name: "product", cookie: brokerCookie, signedIn: true},
		{name: "anonymous"},
	} {
		t.Run(session.name+" session capability", func(t *testing.T) {
			w := browserRequest("GET", "/auth/session", session.cookie, "")
			require.Equal(t, http.StatusOK, w.Code)
			var result struct {
				Authenticated bool  `json:"authenticated"`
				CanManage     *bool `json:"can_manage_central_credentials"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
			require.NotNil(t, result.CanManage)
			assert.Equal(t, session.signedIn, result.Authenticated)
			assert.Equal(t, session.canManage, *result.CanManage)
		})
	}
	require.Equal(t, 200, browserRequest("GET", "/v1/me", brokerCookie, "").Code)
	for _, path := range []string{"/v1/me/password", "/v1/me/mfa/enroll", "/v1/me/mfa/confirm", "/v1/me/mfa/disable"} {
		w := browserRequest("POST", path, brokerCookie, "{}")
		assert.Equal(t, 403, w.Code, "%s: %s", path, w.Body)
	}
	for _, path := range []string{"/v1/me/password", "/v1/me/mfa/confirm", "/v1/me/mfa/disable"} {
		w := browserRequest("POST", path, centralCookie, "{}")
		assert.Equal(t, 400, w.Code, "%s: %s", path, w.Body)
	}

	path := "/v1/accounts/" + owned[0].ID + "/api-keys"
	keyBody := `{"name":"Credential fixture","origin_product_id":"superwagie","models":["x"]}`
	created := browserRequest("POST", path, centralCookie, keyBody)
	require.Equal(t, 201, created.Code, created.Body.String())
	var key modelaccess.APIKey
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &key))
	require.NotEmpty(t, key.ID)
	for _, mutation := range []struct{ method, suffix, body string }{
		{"POST", "", "{}"},
		{"POST", "/" + key.ID + "/scope", "{}"},
		{"POST", "/" + key.ID + "/models", "{}"},
		{"DELETE", "/" + key.ID + "?product_id=superwagie", ""},
	} {
		w := browserRequest(mutation.method, path+mutation.suffix, brokerCookie, mutation.body)
		assert.Equal(t, 403, w.Code, "%s: %s", mutation.suffix, w.Body)
	}
	w := browserRequest("DELETE", path+"/"+key.ID+"?product_id=superwagie", centralCookie, "")
	require.Equal(t, 204, w.Code, w.Body.String())

	actor, err := nativeAuth.Authenticate(ctx, product, "default", productauth.Authentication{
		LocalUserID: binding.LocalUserID, AuthenticationID: "finance-fixture-signin", Method: "session",
		AuthenticatedAt: time.Now().Unix(), AuthVersion: 1,
	})
	require.NoError(t, err)
	financeRequest := httptest.NewRequest("POST", hub+"/internal/v1/product-finance/accounts/"+owned[0].ID+"/api-keys", strings.NewReader(keyBody))
	financeRequest.Header.Set("Authorization", "Bearer "+serviceToken)
	financeRequest.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor.ActorToken)
	financeRequest.Header.Set("Content-Type", "application/json")
	financeResponse := httptest.NewRecorder()
	h.ServeHTTP(financeResponse, financeRequest)
	require.Equal(t, 201, financeResponse.Code, financeResponse.Body.String())
}
