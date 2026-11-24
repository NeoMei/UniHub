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
	"unihub/internal/appintegrations"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/platformadmin"
	"unihub/internal/productauth"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestApplicationIntegrationSuperAdminAndLiveBrowserRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(pool)
	adminUser, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(`[{"product_id":"agentwiki","name":"AgentWiki","entry_url":"https://old-wiki.example/","version":1,"default_roles":[],"roles":[]}]`))
	if err != nil {
		t.Fatal(err)
	}
	const hub = "https://hub.example"
	users := productusers.New(pool, registry, hub, nil)
	admin := platformadmin.New(pool, users)
	if err := admin.Bootstrap(ctx, "deployment-cli", adminUser.ID, "test bootstrap"); err != nil {
		t.Fatal(err)
	}
	integrations := appintegrations.New(pool, admin, hub, false, registry, nil, nil, nil)
	users.ConfigureRegistryOverlay(integrations)
	authService := productauth.New(pool, a, users, hub)
	authService.ConfigureBrowserRegistryOverlay(integrations)
	handler := authService.BrowserHandler(New(verifier{}, a, wallet.New(pool, a, noLedger{}), Options{
		PlatformAdmin: admin, ProductUsers: users, ApplicationIntegrations: integrations, ProductAuth: authService,
	}))

	const route = "/admin/v1/application-integrations"
	const item = route + "/example-wiki/default"
	const runtime = "/internal/v1/product-runtime/identity/"
	productSession := httptest.NewRequest("GET", route, nil)
	productSession = productSession.WithContext(auth.WithBrowserClaims(ctx, auth.Claims{
		Issuer: "https://test", Subject: "bob", Name: "bob", ProductSession: true,
	}))
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, productSession)
	integrationStatus(t, blocked, 403)
	capability := httptest.NewRequest("GET", "/admin/v1/me", nil).WithContext(productSession.Context())
	settings := httptest.NewRecorder()
	handler.ServeHTTP(settings, capability)
	if principal := integrationBody[platformadmin.Principal](t, settings, 200); principal.Enabled || len(principal.Roles) > 0 {
		t.Fatal("product session inherited platform authority")
	}
	const input = `{"product_id":"example-wiki","name":"Example Wiki","origin":"https://wiki.example","entry_url":"https://wiki.example/auth/start","callback_url":"https://wiki.example/auth/callback","native_login_url":"https://wiki.example/native"}`
	for _, check := range []struct{ method, path string }{
		{"GET", route}, {"POST", route}, {"PUT", item}, {"POST", item + "/credential"}, {"POST", item + "/state"},
	} {
		integrationStatus(t, call(handler, check.method, check.path, "alice", input, ""), 403)
	}
	if _, err = admin.SetRoles(ctx, adminUser.ID, ordinary.ID, []string{"user_admin", "finance_admin", "auditor"}, 0, "role boundary"); err != nil {
		t.Fatal(err)
	}
	integrationStatus(t, call(handler, "GET", route, "alice", "", ""), 403)

	list := call(handler, "GET", route, "bob", "", "")
	integrationStatus(t, list, 200)
	if !strings.Contains(list.Body.String(), `"product_id":"agentwiki"`) ||
		!strings.Contains(list.Body.String(), `"source":"deployment"`) {
		t.Fatal(list.Body)
	}
	integrationStatus(t, call(handler, "POST", route, "bob", strings.Replace(input, "example-wiki", "agentwiki", 1), ""), 409)

	app := integrationBody[appintegrations.Application](t, call(handler, "POST", route, "bob", input, ""), 201)
	integrationStatus(t, call(handler, "POST", item+"/state", "bob", `{"version":1,"state":"pilot"}`, ""), 409)
	credentialResponse := call(handler, "POST", item+"/credential", "bob", `{"version":1}`, "")
	cred := integrationBody[appintegrations.Credential](t, credentialResponse, 201)
	if cred.Token == "" || credentialResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential missing or cacheable")
	}
	integrationStatus(t, call(handler, "POST", item+"/credential", "bob", `{"version":1}`, ""), 409)
	integrationStatus(t, call(handler, "POST", runtime+"source-lifecycle/status", cred.Token, `{"local_user_id":"user-1"}`, ""), 401)
	integrationStatus(t, call(handler, "POST", item+"/state", "bob", `{"version":2,"state":"pilot"}`, ""), 200)
	integrationStatus(t, call(handler, "POST", item+"/state", "bob", `{"version":3,"state":"active"}`, ""), 409)
	if w := call(handler, "GET", "/auth/login", "", "", ""); strings.Contains(w.Body.String(), app.NativeLoginURL) {
		t.Fatal("pilot shown in public login picker")
	}
	integrationStatus(t, call(handler, "POST", runtime+"import", cred.Token, `{}`, ""), 403)
	if _, err = admin.SetProductAccess(ctx, adminUser.ID, ordinary.ID, app.ProductID, app.InstanceID,
		[]string{"member"}, true, 0, "test assignment ahead of binding"); err != nil {
		t.Fatal(err)
	}
	if got := call(handler, "GET", "/v1/me/applications", "alice", "", ""); strings.Contains(got.Body.String(), app.ProductID) {
		t.Fatal("ordinary unlinked user received the application")
	}

	const local = "user-1"
	authn := productauth.Authentication{LocalUserID: local, AuthenticationID: "application-enroll-1", AuthVersion: 1,
		AuthenticatedAt: time.Now().Unix(), Method: "password"}
	identity := integrationBody[productauth.Identity](t, integrationRequest(handler, "POST", runtime+"enroll", cred.Token,
		productauth.NativeEnrollment{ClientID: app.ClientID, Authentication: authn, DisplayName: "Fixture", Email: "fixture@example.invalid", Automatic: true}, nil, ""), 200)
	if identity.Binding.LocalUserID != local || identity.Binding.UserID == ordinary.ID ||
		len(identity.Access.Permissions) != 1 || identity.Access.Permissions[0] != "product:use" {
		t.Fatalf("unexpected enrolled identity: %+v", identity)
	}
	authn.AuthenticationID = "application-authenticate-1"
	actor := integrationBody[productauth.Identity](t, integrationRequest(handler, "POST", runtime+"authenticate", cred.Token, authn, nil, ""), 200)
	state := strings.Repeat("a", 43)
	start := integrationRequest(handler, "GET", "/auth/product/start?client_id="+url.QueryEscape(app.ClientID)+"&state="+state, "", nil, nil, "")
	integrationStatus(t, start, 303)
	callback := integrationLocation(t, start)
	if callback.Host != "wiki.example" || callback.Path != "/auth/callback" {
		t.Fatal("unexpected callback")
	}
	approval := integrationBody[productauth.BrowserApprovalReceipt](t, integrationRequest(handler, "POST", runtime+"browser/approve", cred.Token,
		productauth.BrowserApproval{ClientID: app.ClientID, HandoffID: callback.Query().Get("handoff"), State: state,
			NativeSessionID: strings.Repeat("b", 64), NativeSessionExpiresAt: time.Now().Add(time.Hour).Unix(), NativeHandoffGeneration: "1"},
		nil, actor.ActorToken), 200)
	finishURL, err := url.Parse(approval.FinishURL)
	if err != nil {
		t.Fatal(err)
	}
	finish := integrationRequest(handler, "GET", finishURL.RequestURI(), "", nil, start.Result().Cookies(), "")
	integrationStatus(t, finish, 303)
	integrationStatus(t, call(handler, "POST", item+"/state", "bob", `{"version":3,"state":"active"}`, ""), 409)

	verifier := strings.Repeat("v", 43)
	challenge := sha256.Sum256([]byte(verifier))
	nonce := strings.Repeat("n", 43)
	values := url.Values{"client_id": {app.ClientID}, "redirect_uri": {app.CallbackURL}, "state": {state}, "nonce": {nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	authorize := integrationRequest(handler, "GET", "/auth/product/authorize?"+values.Encode(), "", nil, finish.Result().Cookies(), "")
	integrationStatus(t, authorize, 303)
	destination := integrationLocation(t, authorize)
	if destination.Query().Get("code") == "" {
		t.Fatalf("browser broker did not issue a code: %s", destination.Query().Get("error"))
	}
	session := integrationBody[productauth.ProductIdentitySession](t, integrationRequest(handler, "POST", runtime+"session/exchange", cred.Token,
		productauth.BrowserExchange{ClientID: app.ClientID, Code: destination.Query().Get("code"), Verifier: verifier,
			Nonce: nonce, RedirectURI: app.CallbackURL}, nil, ""), 200)
	if session.Identity.Binding.LocalUserID != local || session.Identity.Binding.UserID != identity.Binding.UserID {
		t.Fatal("original account changed")
	}

	live := integrationBody[appintegrations.Application](t, call(handler, "POST", item+"/state", "bob", `{"version":3,"state":"active"}`, ""), 200)
	if !live.InboundVerified || !live.OutboundVerified || live.State != "active" {
		t.Fatalf("missing verified receipt: %+v", live)
	}
	if w := call(handler, "GET", "/auth/login", "", "", ""); !strings.Contains(w.Body.String(), app.NativeLoginURL) {
		t.Fatal("published app is missing from login")
	}
	launches, err := users.Applications(ctx, accounts.User{ID: identity.Binding.UserID}, "", "")
	if err != nil || len(launches) != 1 || launches[0].EntryURL != app.EntryURL {
		t.Fatalf("missing linked user app: %+v %v", launches, err)
	}

	oldAuthorize := integrationRequest(handler, "GET", "/auth/product/authorize?"+values.Encode(), "", nil, finish.Result().Cookies(), "")
	integrationStatus(t, oldAuthorize, 303)
	oldCode := integrationLocation(t, oldAuthorize).Query().Get("code")
	if oldCode == "" {
		t.Fatal("failed to issue pre-pause authorization")
	}
	integrationStatus(t, call(handler, "POST", item+"/state", "bob", `{"version":4,"state":"paused"}`, ""), 200)
	integrationStatus(t, integrationRequest(handler, "POST", runtime+"session/verify", cred.Token,
		map[string]string{"source_proof_token": session.SourceProofToken, "central_user_id": identity.Binding.UserID}, nil, ""), 204)
	assignments, err := admin.UserProductAccess(ctx, adminUser.ID, identity.Binding.UserID)
	if err != nil || len(assignments) != 2 {
		t.Fatalf("paused access must remain manageable: %+v %v", assignments, err)
	}
	integrationStatus(t, call(handler, "POST", runtime+"authenticate", cred.Token, `{}`, ""), 401)
	integrationStatus(t, call(handler, "POST", runtime+"source-lifecycle/status", cred.Token, `{"local_user_id":"user-1"}`, ""), 200)
	if products, err := users.Applications(ctx, accounts.User{ID: identity.Binding.UserID}, "", ""); err != nil || len(products) != 0 {
		t.Fatalf("paused application is still offered: %+v %v", products, err)
	}
	second := integrationBody[appintegrations.Credential](t, call(handler, "POST", item+"/credential", "bob", `{"version":5}`, ""), 201)
	if second.Token == cred.Token {
		t.Fatal("reissued credential was identical")
	}
	integrationStatus(t, call(handler, "POST", runtime+"source-lifecycle/status", cred.Token, `{"local_user_id":"user-1"}`, ""), 401)
	integrationStatus(t, call(handler, "POST", item+"/state", "bob", `{"version":6,"state":"pilot"}`, ""), 200)
	integrationStatus(t, integrationRequest(handler, "POST", runtime+"session/exchange", second.Token,
		productauth.BrowserExchange{ClientID: app.ClientID, Code: oldCode, Verifier: verifier, Nonce: nonce, RedirectURI: app.CallbackURL}, nil, ""), 403)
	integrationStatus(t, call(handler, "POST", item+"/state", "bob", `{"version":7,"state":"active"}`, ""), 409)
	list = call(handler, "GET", route, "bob", "", "")
	if strings.Contains(list.Body.String(), second.Token) || strings.Contains(list.Body.String(), cred.Token) {
		t.Fatal("credential leaked in list")
	}
}

func integrationRequest(h http.Handler, method, path, token string, value any, cookies []*http.Cookie, actor string) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(value)
	r := httptest.NewRequest(method, path, strings.NewReader(string(payload)))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if actor != "" {
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
	}
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func integrationStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("wanted HTTP %d, got %d: %s", status, w.Code, w.Body.String())
	}
}

func integrationBody[T any](t *testing.T, w *httptest.ResponseRecorder, status int) T {
	t.Helper()
	integrationStatus(t, w, status)
	var value T
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func integrationLocation(t *testing.T, w *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	result, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return result
}
