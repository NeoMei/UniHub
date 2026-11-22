package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestAgentWikiIdentityBrowserRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(`[{"product_id":"agentwiki","name":"AgentWiki","entry_url":"https://wiki.test","version":1,"default_roles":[],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	a := accounts.New(pool)
	users := productusers.New(pool, registry, "https://hub.test", nil)
	auth := productauth.New(pool, a, users, "https://hub.test")
	client := products.Client{ClientID: "wiki", ProductID: "agentwiki", InstanceID: "default", BrowserSSO: true, IdentityLinkRole: "member", NativeHandoffGeneration: "1", RedirectURIs: []string{"https://wiki.test/auth/return"}}
	auth.ConfigureBrowserClients(map[string]products.Client{"wiki": client})
	serviceToken := strings.Repeat("s", 40)
	handler := auth.BrowserHandler(New(runtimeVerifier{}, a, wallet.New(pool, a, nil), Options{
		ProductUsers: users, ProductAuth: auth, ProductAccess: map[string]products.Client{"wiki": client},
		ProductRuntime: map[string]RuntimeService{"wiki": {
			Token: serviceToken, ProductID: "agentwiki", InstanceID: "default", IdentityClientID: "wiki",
			AllowedOps: []string{"identity:link", "identity:session", "identity:source-lifecycle", "identity:authenticate", "identity:browser"},
		}},
	}))
	request := func(method, path string, body any, cookie *http.Cookie, actor string) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "https://hub.test"+path, strings.NewReader(string(raw)))
		if strings.HasPrefix(path, "/internal/") {
			req.Header.Set("Authorization", "Bearer "+serviceToken)
		}
		if actor != "" {
			req.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	expect := func(out *httptest.ResponseRecorder, status int, body any) {
		t.Helper()
		if out.Code != status {
			t.Fatalf("status %d, want %d: %s", out.Code, status, out.Body.String())
		}
		if body != nil && json.Unmarshal(out.Body.Bytes(), body) != nil {
			t.Fatalf("invalid response: %s", out.Body.String())
		}
	}
	location := func(out *httptest.ResponseRecorder) *url.URL {
		t.Helper()
		expect(out, http.StatusSeeOther, nil)
		u, err := url.Parse(out.Header().Get("Location"))
		if err != nil || u.String() == "" {
			t.Fatalf("expected valid redirect: %v", err)
		}
		return u
	}
	getCookie := func(out *httptest.ResponseRecorder, name string) *http.Cookie {
		t.Helper()
		for _, c := range out.Result().Cookies() {
			if c.Name == name && c.Value != "" {
				return c
			}
		}
		t.Fatalf("cookie %s absent", name)
		return nil
	}

	const apiPath = "/internal/v1/product-runtime/identity"
	var missing struct {
		LocalUserID string `json:"local_user_id"`
		Found       *bool  `json:"found"`
	}
	expect(request(http.MethodPost, apiPath+"/source-lifecycle/status", map[string]string{
		"local_user_id": "not-yet-enrolled",
	}, nil, ""), http.StatusOK, &missing)
	if missing.LocalUserID != "not-yet-enrolled" || missing.Found == nil || *missing.Found {
		t.Fatalf("expected a subject-matched absence response: %+v", missing)
	}
	proof := productauth.Authentication{LocalUserID: "old-wiki-user", AuthenticationID: "native-enroll-001", Method: "password", AuthenticatedAt: time.Now().Unix(), AuthVersion: 1}
	var enrolled productauth.Identity
	expect(request(http.MethodPost, apiPath+"/enroll", productauth.NativeEnrollment{ClientID: "wiki", Authentication: proof}, nil, ""), http.StatusOK, &enrolled)
	proof.AuthenticationID = "native-session-001"
	proof.Method = "session"
	var actor productauth.Identity
	expect(request(http.MethodPost, apiPath+"/authenticate", proof, nil, ""), http.StatusOK, &actor)
	state := strings.Repeat("s", 43)
	start := request(http.MethodGet, "/auth/product/start?client_id=wiki&state="+state, nil, nil, "")
	handoffID := location(start).Query().Get("handoff")
	handoffCookie := getCookie(start, "unihub_product_handoff")
	var approved productauth.BrowserApprovalReceipt
	nativeSessionID := strings.Repeat("a", 64)
	nativeExpiry := time.Unix(time.Now().Add(3*time.Minute).Unix(), 0)
	approval := map[string]any{
		"client_id": "wiki", "state": state, "handoff_id": handoffID, "native_session_id": nativeSessionID,
	}
	expect(request(http.MethodPost, apiPath+"/browser/approve", approval, nil, actor.ActorToken), http.StatusBadRequest, nil)
	approval["native_session_expires_at"] = nativeExpiry.Unix()
	expect(request(http.MethodPost, apiPath+"/browser/approve", approval, nil, actor.ActorToken), http.StatusForbidden, nil)
	approval["native_handoff_generation"] = "1"
	expect(request(http.MethodPost, apiPath+"/browser/approve", approval, nil, actor.ActorToken), http.StatusOK, &approved)
	finish, err := url.Parse(approved.FinishURL)
	if err != nil {
		t.Fatal(err)
	}
	toHub := request(http.MethodGet, finish.RequestURI(), nil, handoffCookie, "")
	location(toHub)
	centralCookie := getCookie(toHub, "unihub_session")
	if !centralCookie.Expires.Equal(nativeExpiry) || centralCookie.MaxAge <= 0 || centralCookie.MaxAge > 180 {
		t.Fatalf("browser cookie outlived the native JWT: expires %v, max-age %d", centralCookie.Expires, centralCookie.MaxAge)
	}

	verifier, nonce := strings.Repeat("v", 43), strings.Repeat("n", 43)
	hash := sha256.Sum256([]byte(verifier))
	authorize := "/auth/product/authorize?" + url.Values{
		"client_id": {"wiki"}, "redirect_uri": {client.RedirectURIs[0]}, "state": {state}, "nonce": {nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"},
	}.Encode()
	callback := location(request(http.MethodGet, authorize, nil, centralCookie, ""))
	exchange := productauth.BrowserExchange{ClientID: "wiki", Code: callback.Query().Get("code"), Verifier: verifier, Nonce: nonce, RedirectURI: client.RedirectURIs[0]}
	match := map[string]string{
		"client_id": exchange.ClientID, "code": exchange.Code, "code_verifier": exchange.Verifier,
		"nonce": exchange.Nonce, "redirect_uri": exchange.RedirectURI, "local_user_id": proof.LocalUserID,
	}
	var preview struct{ Matches bool }
	expect(request(http.MethodPost, apiPath+"/session/matches", match, nil, ""), http.StatusOK, &preview)
	if !preview.Matches {
		t.Fatal("the active browser was not matched to the original product user")
	}
	match["local_user_id"] = "another-wiki-user"
	expect(request(http.MethodPost, apiPath+"/session/matches", match, nil, ""), http.StatusOK, &preview)
	if preview.Matches {
		t.Fatal("a different product user matched the browser")
	}
	var session productauth.ProductIdentitySession
	expect(request(http.MethodPost, apiPath+"/session/exchange", exchange, nil, ""), http.StatusOK, &session)
	if session.Identity.Binding.LocalUserID != proof.LocalUserID || session.Identity.Binding.UserID != enrolled.Binding.UserID || session.SourceProofToken == "" {
		t.Fatalf("original identity lost: %+v", session.Identity.Binding)
	}
	if session.SourceProofExpiresAt != nativeExpiry.Unix() {
		t.Fatalf("derived proof expiry %d, want %d", session.SourceProofExpiresAt, nativeExpiry.Unix())
	}
	verify := map[string]string{"central_user_id": enrolled.Binding.UserID, "source_proof_token": session.SourceProofToken}
	expect(request(http.MethodPost, apiPath+"/session/verify", verify, nil, ""), http.StatusNoContent, nil)

	revoke := map[string]any{"local_user_id": proof.LocalUserID, "native_session_id": nativeSessionID, "expires_at": nativeExpiry.Unix()}
	dispatch := request(http.MethodPost, apiPath+"/source-session/revoke", revoke, nil, "")
	expect(dispatch, http.StatusNoContent, nil)
	if dispatch.Body.Len() != 0 {
		t.Fatal("accepted revocation returned a body")
	}
	expect(request(http.MethodPost, apiPath+"/source-session/revoke", revoke, nil, ""), http.StatusNoContent, nil)
	revoke["expires_at"] = time.Now().Add(-time.Minute).Unix()
	expect(request(http.MethodPost, apiPath+"/source-session/revoke", revoke, nil, ""), http.StatusNoContent, nil)
	revoke["local_user_id"] = "unverified-native-binding"
	expect(request(http.MethodPost, apiPath+"/source-session/revoke", revoke, nil, ""), http.StatusNotFound, nil)
	expect(request(http.MethodPost, apiPath+"/session/verify", verify, nil, ""), http.StatusForbidden, nil)
	afterLogout := location(request(http.MethodGet, authorize, nil, centralCookie, ""))
	if afterLogout.Query().Get("error") != "login_required" {
		t.Fatal("native logout left the UniHub browser session usable")
	}
	expect(request(http.MethodPost, apiPath+"/source-lifecycle", productauth.SourceLifecycle{
		Lifecycle: productauth.Lifecycle{LocalUserID: proof.LocalUserID, OperationID: "native-lock-001", PreviousAuthVersion: 1, NextAuthVersion: 2},
		State:     productauth.SourceStateLocked,
	}, nil, ""), http.StatusOK, nil)
	expect(request(http.MethodPost, apiPath+"/session/verify", verify, nil, ""), http.StatusForbidden, nil)
	proof.AuthenticationID = "native-session-002"
	proof.AuthVersion = 2
	expect(request(http.MethodPost, apiPath+"/authenticate", proof, nil, ""), http.StatusForbidden, nil)
}
