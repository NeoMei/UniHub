package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/console"
	"unihub/internal/database"
	"unihub/internal/identityapi"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestNativeBrowserHandoffCreatesCentralSessionAndSecondProductBinding(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	reg, e := productusers.Parse([]byte(`[{"product_id":"llmrouter","name":"Router","entry_url":"https://router.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]},{"product_id":"second","name":"Second","entry_url":"https://second.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	if e != nil {
		t.Fatal(e)
	}
	users := productusers.New(p, reg, "https://hub.test", nil)
	pa := productauth.New(p, a, users, "https://hub.test")
	clients := map[string]products.Client{"router": {ClientID: "router", ProductID: "llmrouter", InstanceID: "default", BrowserSSO: true, RedirectURIs: []string{"https://router.test/api/unihub/callback"}}, "second": {ClientID: "second", ProductID: "second", InstanceID: "default", BrowserSSO: true, RedirectURIs: []string{"https://second.test/api/unihub/callback"}}}
	pa.ConfigureBrowserClients(clients)
	services := map[string]RuntimeService{"router": {Token: strings.Repeat("a", 32), ProductID: "llmrouter", InstanceID: "default", AllowedOps: []string{"identity:register", "identity:authenticate", "identity:browser", "identity:lookup", "identity:lifecycle", "bindings:write"}}, "second": {Token: strings.Repeat("b", 32), ProductID: "second", InstanceID: "default", FinanceClientID: "second", AllowedOps: []string{"identity:browser", "bindings:write", "access:read", "users:write"}}, "second-wrong-client": {Token: strings.Repeat("c", 32), ProductID: "second", InstanceID: "default", FinanceClientID: "router", AllowedOps: []string{"identity:browser"}}}
	profileProvider, e := identityapi.New(ctx, p, identityapi.Options{Issuer: "https://provider.test", Clients: clients})
	if e != nil {
		t.Fatal(e)
	}
	api := New(runtimeVerifier{}, a, wallet.New(p, a, nil), Options{ProductUsers: users, ProductAuth: pa, ProductAccess: clients, ProductRuntime: services, Identity: profileProvider})
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks"})
	}))
	defer issuer.Close()
	browser, e := console.New(ctx, p, runtimeVerifier{}, console.Options{Issuer: issuer.URL, PublicURL: "https://hub.test", ClientID: "console"})
	if e != nil {
		t.Fatal(e)
	}
	h := pa.BrowserHandler(browser.Handler(api))
	request := func(method, path, service, actor string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://hub.test"+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if service != "" {
			r.Header.Set("Authorization", "Bearer "+services[service].Token)
		}
		if actor != "" {
			r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, want int, out any) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("response %d want %d: %s", w.Code, want, w.Body.String())
		}
		if out != nil {
			if e := json.Unmarshal(w.Body.Bytes(), out); e != nil {
				t.Fatal(e)
			}
		}
	}
	var native productauth.Identity
	decode(request("POST", "/internal/v1/product-runtime/identity/register", "router", "", productauth.Registration{LocalUserID: "17", OperationID: "native-register-17", Username: "Native", AuthVersion: 1}), 201, &native)
	var proof productauth.Identity
	decode(request("POST", "/internal/v1/product-runtime/identity/authenticate", "router", "", productauth.Authentication{LocalUserID: "17", AuthenticationID: "native-authenticate-17", Method: "passkey", AuthenticatedAt: time.Now().Unix(), AuthVersion: 1}), 200, &proof)
	state := strings.Repeat("s", 43)
	start := request("GET", "/auth/product/start?client_id=router&state="+state, "", "", nil)
	decode(start, 303, nil)
	callback, e := url.Parse(start.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	handoff := callback.Query().Get("handoff")
	if handoff == "" {
		t.Fatal("source callback missing handoff")
	}
	var bindingCookie *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == "unihub_product_handoff" {
			bindingCookie = c
		}
	}
	if bindingCookie == nil || !bindingCookie.HttpOnly || !bindingCookie.Secure || bindingCookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("handoff browser binding absent")
	}
	finishPath := "/auth/product/finish?handoff=" + url.QueryEscape(handoff) + "&state=" + state
	decode(request("GET", finishPath, "", "", nil, bindingCookie), 401, nil)
	approval := productauth.BrowserApproval{ClientID: "router", HandoffID: handoff, State: state}
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/approve", "second", proof.ActorToken, approval), 403, nil)
	var receipt productauth.BrowserApprovalReceipt
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/approve", "router", proof.ActorToken, approval), 200, &receipt)
	decode(request("GET", finishPath, "", "", nil, &http.Cookie{Name: bindingCookie.Name, Value: strings.Repeat("w", 43)}), 401, nil)
	done := request("GET", finishPath, "", "", nil, bindingCookie)
	decode(done, 303, nil)
	decode(request("GET", finishPath, "", "", nil, bindingCookie), 401, nil)
	var central *http.Cookie
	for _, c := range done.Result().Cookies() {
		if c.Name == "unihub_session" {
			central = c
		}
	}
	if central == nil || !central.HttpOnly || !central.Secure || central.Value == bindingCookie.Value {
		t.Fatal("central session did not rotate")
	}
	var session struct {
		Authenticated bool          `json:"authenticated"`
		User          accounts.User `json:"user"`
		CSRF          string        `json:"csrf_token"`
	}
	decode(request("GET", "/auth/session", "", "", nil, central), 200, &session)
	if !session.Authenticated || session.User.ID != native.Binding.UserID {
		t.Fatal("native registration did not establish canonical central browser session")
	}
	var browserMeta map[string]any
	decode(request("GET", "/auth/session", "", "", nil, central), 200, &browserMeta)
	passwordRequest := httptest.NewRequest("POST", "https://hub.test/v1/me/password", strings.NewReader(`{"current_password":"unused","new_password":"unused-new"}`))
	passwordRequest.AddCookie(central)
	passwordRequest.Header.Set("Origin", "https://hub.test")
	passwordRequest.Header.Set("X-CSRF-Token", browserMeta["csrf_token"].(string))
	passwordRequest.Header.Set("Content-Type", "application/json")
	passwordResponse := httptest.NewRecorder()
	h.ServeHTTP(passwordResponse, passwordRequest)
	decode(passwordResponse, 409, nil)
	var profile identityapi.Profile
	decode(request("GET", "/v1/me/profile", "", "", nil, central), 200, &profile)
	if profile.ID != native.Binding.UserID || profile.CredentialSource != "product" || len(profile.CredentialManagement) != 1 || profile.CredentialManagement[0].URL != "https://router.test/profile" {
		t.Fatalf("native profile not usable: %+v", profile)
	}
	var me accounts.User
	decode(request("GET", "/v1/me", "", "", nil, central), 200, &me)
	if me.ID != native.Binding.UserID {
		t.Fatal("central API uses another subject")
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	nonce := strings.Repeat("n", 43)
	authorize := "/auth/product/authorize?" + url.Values{"client_id": {"second"}, "redirect_uri": {clients["second"].RedirectURIs[0]}, "state": {state}, "nonce": {nonce}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}.Encode()
	result := request("GET", authorize, "", "", nil, central)
	decode(result, 303, nil)
	target, _ := url.Parse(result.Header().Get("Location"))
	code := target.Query().Get("code")
	if code == "" {
		t.Fatal("second product needs no new registration but received error", target.Query().Get("error"))
	}
	exchange := productauth.BrowserExchange{ClientID: "second", Code: code, Verifier: verifier, Nonce: nonce, RedirectURI: clients["second"].RedirectURIs[0]}
	bad := exchange
	bad.Verifier = strings.Repeat("x", 43)
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/exchange", "second", "", bad), 403, nil)
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/exchange", "router", "", exchange), 403, nil)
	var grant productauth.BrowserGrant
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/exchange", "second", "", exchange), 200, &grant)
	if grant.Access.Subject != native.Binding.UserID || !strings.HasPrefix(grant.ProofToken, "ubp_") {
		t.Fatal("cross-product canonical subject lost")
	}
	if !strings.HasPrefix(grant.SourceProofToken, "ubsp_") || grant.SourceProofExpiresAt <= grant.ExpiresAt {
		t.Fatal("grant missing bounded source-session proof")
	}
	verifyBody := map[string]string{"source_proof_token": grant.SourceProofToken, "central_user_id": native.Binding.UserID}
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/source/verify", "second", "", verifyBody), 204, nil)
	wrongSourceBody := map[string]string{"source_proof_token": grant.SourceProofToken, "central_user_id": "another-user"}
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/source/verify", "second", "", wrongSourceBody), 403, nil)
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/source/verify", "second-wrong-client", "", verifyBody), 403, nil)
	verifyBodyBytes, _ := json.Marshal(verifyBody)
	originRequest := httptest.NewRequest("POST", "https://hub.test/internal/v1/product-runtime/identity/browser/source/verify", bytes.NewReader(verifyBodyBytes))
	originRequest.Header.Set("Authorization", "Bearer "+services["second"].Token)
	originRequest.Header.Set("Origin", "https://router.test")
	originResponse := httptest.NewRecorder()
	h.ServeHTTP(originResponse, originRequest)
	decode(originResponse, 403, nil)
	for _, body := range []string{
		`{"source_proof_token":"` + grant.SourceProofToken + `","source_proof_token":"` + grant.SourceProofToken + `","central_user_id":"` + native.Binding.UserID + `"}`,
		`{"source_proof_token":"` + grant.SourceProofToken + `"}`,
		`{"source_proof_token":"` + grant.SourceProofToken + `","central_user_id":"` + native.Binding.UserID + `","padding":"` + strings.Repeat("x", 5000) + `"}`,
	} {
		invalidRequest := httptest.NewRequest("POST", "https://hub.test/internal/v1/product-runtime/identity/browser/source/verify", strings.NewReader(body))
		invalidRequest.Header.Set("Authorization", "Bearer "+services["second"].Token)
		invalidResponse := httptest.NewRecorder()
		h.ServeHTTP(invalidResponse, invalidRequest)
		decode(invalidResponse, 403, nil)
	}
	duplicateAuthRequest := httptest.NewRequest("POST", "https://hub.test/internal/v1/product-runtime/identity/browser/source/verify", bytes.NewReader(verifyBodyBytes))
	duplicateAuthRequest.Header.Add("Authorization", "Bearer "+services["second"].Token)
	duplicateAuthRequest.Header.Add("Authorization", "Bearer "+services["second"].Token)
	duplicateAuthResponse := httptest.NewRecorder()
	h.ServeHTTP(duplicateAuthResponse, duplicateAuthRequest)
	decode(duplicateAuthResponse, 401, nil)
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/exchange", "second", "", exchange), 403, nil)
	decode(request("PATCH", "/internal/v1/product-runtime/users/other/access", "second", grant.ProofToken, map[string]any{"user_id": "other", "roles": []string{"member"}, "active": true, "version": 1}), 403, nil)
	var targetBinding productusers.RuntimeBinding
	bind := map[string]string{"local_user_id": "204", "binding_key": "second-jit-204"}
	decode(request("POST", "/internal/v1/product-runtime/bindings", "second", grant.ProofToken, bind), 200, &targetBinding)
	if targetBinding.UserID != native.Binding.UserID || targetBinding.LocalUserID != "204" {
		t.Fatal("second product binding changed subject")
	}
	decode(request("POST", "/internal/v1/product-runtime/bindings", "second", grant.ProofToken, bind), 403, nil)
	legacyAuthorize := "/auth/product/authorize?" + url.Values{"client_id": {"second"}, "redirect_uri": {clients["second"].RedirectURIs[0]}, "state": {strings.Repeat("l", 43)}, "nonce": {strings.Repeat("m", 43)}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}.Encode()
	legacyResult := request("GET", legacyAuthorize, "", "", nil, central)
	decode(legacyResult, 303, nil)
	legacyURL, _ := url.Parse(legacyResult.Header().Get("Location"))
	legacyCode := legacyURL.Query().Get("code")
	legacyCodeJSON, _ := json.Marshal(legacyCode)
	legacyCodeHash := sha256.Sum256(legacyCodeJSON)
	if _, e = p.Exec(ctx, `UPDATE product_browser_codes SET source_session_hash=NULL WHERE code_hash=$1`, hex.EncodeToString(legacyCodeHash[:])); e != nil {
		t.Fatal(e)
	}
	legacyExchange := productauth.BrowserExchange{ClientID: "second", Code: legacyCode, Verifier: verifier, Nonce: strings.Repeat("m", 43), RedirectURI: clients["second"].RedirectURIs[0]}
	var legacyGrant productauth.BrowserGrant
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/exchange", "second", "", legacyExchange), 200, &legacyGrant)
	if legacyGrant.SourceProofToken != "" || legacyGrant.SourceProofExpiresAt != 0 {
		t.Fatal("legacy pending code incorrectly gained source-session authority")
	}
	legacyBind := map[string]string{"local_user_id": "204", "binding_key": "second-jit-204"}
	decode(request("POST", "/internal/v1/product-runtime/bindings", "second", legacyGrant.ProofToken, legacyBind), 200, nil)
	var count int
	if e = p.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if count != 1 {
		t.Fatal("native->central->second product duplicated registration")
	}
	late := request("GET", authorize, "", "", nil, central)
	decode(late, 303, nil)
	lateURL, _ := url.Parse(late.Header().Get("Location"))
	pendingCode := exchange
	pendingCode.Code = lateURL.Query().Get("code")
	proofResponse := request("GET", authorize, "", "", nil, central)
	decode(proofResponse, 303, nil)
	proofURL, _ := url.Parse(proofResponse.Header().Get("Location"))
	proofExchange := exchange
	proofExchange.Code = proofURL.Query().Get("code")
	var pendingProof productauth.BrowserGrant
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/exchange", "second", "", proofExchange), 200, &pendingProof)
	decode(request("POST", "/internal/v1/product-runtime/identity/lifecycle", "router", "", productauth.Lifecycle{LocalUserID: "17", OperationID: "native-password-change", PreviousAuthVersion: 1, NextAuthVersion: 2}), 200, nil)
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/exchange", "second", "", pendingCode), 403, nil)
	decode(request("POST", "/internal/v1/product-runtime/bindings", "second", pendingProof.ProofToken, bind), 403, nil)
	decode(request("POST", "/internal/v1/product-runtime/identity/browser/source/verify", "second", "", verifyBody), 403, nil)
	decode(request("GET", "/auth/session", "", "", nil, central), 200, &session)
	if session.Authenticated {
		t.Fatal("source credential revocation left central browser session")
	}
	revoked := request("GET", authorize, "", "", nil, central)
	decode(revoked, 303, nil)
	revokedURL, _ := url.Parse(revoked.Header().Get("Location"))
	if revokedURL.Query().Get("error") != "login_required" {
		t.Fatal("revoked source browser can authorize target")
	}
}
