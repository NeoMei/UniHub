package productauth

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/console"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
)

func setupBrowserHandoffUI(t *testing.T) (*Service, http.Handler, map[string]products.Client, string) {
	t.Helper()
	ctx := context.Background()
	pool := testutil.Database(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	manifests, err := productusers.Parse([]byte(`[
		{"product_id":"agentwiki","name":"AgentWiki","entry_url":"https://wiki.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]},
		{"product_id":"llmrouter","name":"LLMRouter","entry_url":"https://router.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	clients, err := products.Parse([]byte(`[
		{"client_id":"wiki","product_id":"agentwiki","browser_sso":true,"identity_link_role":"member","redirect_uris":["https://wiki.test/return"],"origins":["https://wiki.test"],"native_login_url":"https://wiki.test/login?source=hub","native_login_label":"AgentWiki & Co"},
		{"client_id":"wiki-native","product_id":"agentwiki","browser_sso":true,"identity_link_role":"member","redirect_uris":["https://wiki.test/return"],"native_handoff_generation":"1"},
		{"client_id":"wiki-secondary","product_id":"agentwiki","browser_sso":true,"identity_link_role":"member","redirect_uris":["https://wiki-secondary.test/return"],"origins":["https://wiki-secondary.test"],"native_login_url":"https://wiki-secondary.test/login","native_login_label":"AgentWiki Secondary"},
		{"client_id":"wiki-no-picker","product_id":"agentwiki","browser_sso":true,"identity_link_role":"member","redirect_uris":["https://wiki.test/return"]},
		{"client_id":"router","product_id":"llmrouter","browser_sso":true,"redirect_uris":["https://router.test/return"]}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool, accounts.New(pool), productusers.New(pool, manifests, "https://hub.test", nil), "https://hub.test")
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize",
			"token_endpoint": provider.URL + "/token", "jwks_uri": provider.URL + "/jwks",
		})
	}))
	t.Cleanup(provider.Close)
	browser, err := console.New(ctx, pool, nil, console.Options{Issuer: provider.URL, PublicURL: "https://hub.test", ClientID: "console", ProductUsers: s.users})
	if err != nil {
		t.Fatal(err)
	}
	return s, s.BrowserHandler(browser.Handler(http.NotFoundHandler())), clients, provider.URL
}

func handoffUIRequest(t *testing.T, h http.Handler, want int, method, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "https://hub.test"+path, strings.NewReader(form.Encode()))
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// Navigation POSTs from a no-referrer page can carry an opaque Origin.
		req.Header.Set("Origin", "null")
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, req.URL.Path, w.Code, want, w.Body.String())
	}
	return w
}

func handoffUICookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("response missing %q cookie", name)
	return nil
}

func TestBrowserNativeLoginPicker(t *testing.T) {
	s, h, clients, provider := setupBrowserHandoffUI(t)
	login := func(path string) *url.URL {
		t.Helper()
		w := handoffUIRequest(t, h, http.StatusFound, http.MethodGet, path, nil)
		location, err := w.Result().Location()
		if err != nil || location.Scheme+"://"+location.Host+location.Path != provider+"/authorize" || location.Query().Get("client_id") != "console" {
			t.Fatalf("expected console OIDC authorization, got %v (%v)", location, err)
		}
		handoffUICookie(t, w, "unihub_login")
		return location
	}
	s.ConfigureBrowserClients(map[string]products.Client{"router": clients["router"]})
	login("/auth/login")

	s.ConfigureBrowserClients(map[string]products.Client{"wiki": clients["wiki"], "router": clients["router"]})
	single := handoffUIRequest(t, h, http.StatusOK, http.MethodGet, "/auth/login", nil)
	if !strings.Contains(single.Body.String(), `href="https://wiki.test/login?source=hub"`) {
		t.Fatal("single configured native login is absent")
	}

	s.ConfigureBrowserClients(clients)
	w := handoffUIRequest(t, h, http.StatusOK, http.MethodGet, "/auth/login?native_login_url=https://untrusted.test", nil)
	for _, value := range []string{
		`href="https://wiki.test/login?source=hub"`, "AgentWiki &amp; Co",
		`href="https://wiki-secondary.test/login"`, "AgentWiki Secondary",
		`href="/auth/login?method=central"`,
	} {
		if !strings.Contains(w.Body.String(), value) {
			t.Errorf("picker missing %q", value)
		}
	}
	if strings.Contains(w.Body.String(), "untrusted.test") || len(w.Result().Cookies()) != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("picker reflected request data, issued login cookies, or allowed caching")
	}
	login("/auth/login?method=central")
	if got := login("/auth/login?switch_account=1").Query().Get("prompt"); got != "login" {
		t.Fatalf("central reauthentication prompt = %q", got)
	}
	handoffUIRequest(t, h, http.StatusMethodNotAllowed, http.MethodPost, "/auth/login", url.Values{})
	handoffUIRequest(t, h, http.StatusBadRequest, http.MethodGet, "/auth/product/link", nil)
	handoffUIRequest(t, h, http.StatusMethodNotAllowed, http.MethodPost, "/auth/product/link", url.Values{})
}

var handoffUIFields = regexp.MustCompile(`name="(handoff|state|csrf)" value="([^"]+)"`)

func handoffUIForm(t *testing.T, w *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if !strings.Contains(w.Body.String(), `<form method="post" action="/auth/product/finish">`) {
		t.Fatal("missing account-switch POST form")
	}
	form := url.Values{}
	for _, field := range handoffUIFields.FindAllStringSubmatch(w.Body.String(), -1) {
		form.Add(field[1], html.UnescapeString(field[2]))
	}
	for _, name := range []string{"handoff", "state", "csrf"} {
		if len(form[name]) != 1 {
			t.Fatalf("expected one form field named %q", name)
		}
	}
	return form
}

func TestBrowserHandoffProtectsCentralAccount(t *testing.T) {
	s, h, clients, _ := setupBrowserHandoffUI(t)
	s.ConfigureBrowserClients(clients)
	ctx := context.Background()
	makeActor := func(product, local, name string) Identity {
		t.Helper()
		_, err := s.Register(ctx, product, "default", Registration{LocalUserID: local, OperationID: "register-" + local, DisplayName: name, AuthVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		actor, err := s.Authenticate(ctx, product, "default", Authentication{LocalUserID: local, AuthenticationID: "authenticate-" + local, Method: "password", AuthenticatedAt: time.Now().Unix(), AuthVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		return actor
	}
	native := makeActor("agentwiki", "wiki-native", "Native account")
	router := makeActor("llmrouter", "router-native", "Router account")
	central := accounts.Identity{Issuer: "https://issuer.test", Subject: "central-user", Name: "Central account"}
	canonical, err := s.accounts.EnsureIdentity(ctx, central)
	if err != nil {
		t.Fatal(err)
	}
	makeSession := func(identity accounts.Identity) *http.Cookie {
		t.Helper()
		cookie := &http.Cookie{Name: "unihub_session", Value: browserSecret()}
		_, err := s.pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at)
			VALUES($1,$2,$3,$4,$5,'fixture-csrf',now()+interval '10 minutes')`,
			browserSessionDigest(cookie.Value), identity.Issuer, identity.Subject, identity.Name, identity.Email)
		if err != nil {
			t.Fatal(err)
		}
		return cookie
	}
	checkSession := func(cookie *http.Cookie, wantUser string) {
		t.Helper()
		w := handoffUIRequest(t, h, http.StatusOK, http.MethodGet, "/auth/session", nil, cookie)
		var got struct {
			Authenticated bool          `json:"authenticated"`
			User          accounts.User `json:"user"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Authenticated != (wantUser != "") || got.User.ID != wantUser {
			t.Fatalf("expected session user %q, got %+v (%v)", wantUser, got, err)
		}
	}
	begin := func(client products.Client, actor Identity) (string, *http.Cookie) {
		t.Helper()
		state := browserSecret()
		start := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodGet, "/auth/product/start?client_id="+client.ClientID+"&state="+state, nil)
		callback, err := start.Result().Location()
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := s.ApproveBrowserHandoff(ctx, actor.ActorToken, client.ProductID, "default", BrowserApproval{ClientID: client.ClientID, HandoffID: callback.Query().Get("handoff"), State: state})
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimPrefix(receipt.FinishURL, "https://hub.test"), handoffUICookie(t, start, "unihub_product_handoff")
	}
	finishPath, binding := begin(clients["wiki"], native)
	old := makeSession(central)

	t.Run("confirm requires the rendered form and both original cookies", func(t *testing.T) {
		handoffUIRequest(t, h, http.StatusUnauthorized, http.MethodGet, finishPath, nil, old)
		w := handoffUIRequest(t, h, http.StatusOK, http.MethodGet, finishPath+"&switch_account=1&action=confirm", nil, old, binding)
		if !strings.Contains(w.Body.String(), central.Name) || !strings.Contains(w.Body.String(), "Native account") || len(w.Result().Cookies()) != 0 {
			t.Fatal("confirmation did not show both accounts while leaving cookies alone")
		}
		checkSession(old, canonical.ID)
		form := handoffUIForm(t, w)
		form.Set("action", "confirm")
		post := "/auth/product/finish"
		csrf := form.Get("csrf")
		form.Del("csrf")
		handoffUIRequest(t, h, http.StatusForbidden, http.MethodPost, post, form, old, binding)
		form.Set("csrf", csrf)
		handoffUIRequest(t, h, http.StatusUnauthorized, http.MethodPost, post, form, old)
		handoffUIRequest(t, h, http.StatusForbidden, http.MethodPost, post, form, makeSession(central), binding)
		checkSession(old, canonical.ID)

		done := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodPost, post, form, old, binding)
		if got := done.Header().Get("Location"); !strings.HasPrefix(got, "https://wiki.test/return?handoff_done=1&") {
			t.Fatalf("wrong completion redirect: %s", got)
		}
		checkSession(handoffUICookie(t, done, "unihub_session"), native.Binding.UserID)
		checkSession(old, "")
		handoffUIRequest(t, h, http.StatusUnauthorized, http.MethodGet, finishPath, nil, binding)
	})

	t.Run("cancel keeps the original session", func(t *testing.T) {
		old := makeSession(central)
		path, binding := begin(clients["wiki"], native)
		prompt := handoffUIRequest(t, h, http.StatusOK, http.MethodGet, path, nil, old, binding)
		form := handoffUIForm(t, prompt)
		form.Set("action", "cancel")
		done := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodPost, "/auth/product/finish", form, old, binding)
		if done.Header().Get("Location") != "/" || handoffUICookie(t, done, binding.Name).MaxAge >= 0 || len(done.Result().Cookies()) != 1 {
			t.Fatal("cancel did not return to UniHub with the handoff cookie cleared")
		}
		checkSession(old, canonical.ID)
		var consumed bool
		if err := s.pool.QueryRow(ctx, `SELECT consumed FROM product_browser_handoffs WHERE id_hash=$1`, digest(form.Get("handoff"))).Scan(&consumed); err != nil || consumed {
			t.Fatalf("cancel consumed handoff: %v (%v)", consumed, err)
		}
	})

	t.Run("identity link opt-in protects the account without a picker entry", func(t *testing.T) {
		old := makeSession(central)
		path, binding := begin(clients["wiki-no-picker"], native)
		prompt := handoffUIRequest(t, h, http.StatusOK, http.MethodGet, path, nil, old, binding)
		if len(prompt.Result().Cookies()) != 0 {
			t.Fatal("prompt changed the browser cookies")
		}
		checkSession(old, canonical.ID)
		form := handoffUIForm(t, prompt)
		form.Set("action", "confirm")
		done := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodPost, browserHandoffFinishPath, form, old, binding)
		checkSession(handoffUICookie(t, done, "unihub_session"), native.Binding.UserID)
		checkSession(old, "")
	})

	t.Run("a different identity for the same canonical account proceeds", func(t *testing.T) {
		alias := accounts.Identity{Issuer: "https://issuer.test", Subject: "linked-native", Name: "Linked native account"}
		if _, err := s.pool.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3)`, alias.Issuer, alias.Subject, native.Binding.UserID); err != nil {
			t.Fatal(err)
		}
		old := makeSession(alias)
		path, binding := begin(clients["wiki"], native)
		done := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodGet, path, nil, old, binding)
		if len(done.Result().Cookies()) != 1 || handoffUICookie(t, done, binding.Name).MaxAge >= 0 {
			t.Fatal("same account changed an existing central session")
		}
		checkSession(old, native.Binding.UserID)
	})

	t.Run("stale same-account GET cannot replace a confirmed switch", func(t *testing.T) {
		departing := makeActor("agentwiki", "wiki-departing", "Departing account")
		path, binding := begin(clients["wiki"], departing)
		signedIn := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodGet, path, nil, binding)
		old := handoffUICookie(t, signedIn, "unihub_session")
		checkSession(old, departing.Binding.UserID)

		// Keep the current account recognizable, but require a fresh session
		// for a same-account handoff after its native auth version changes.
		local := departing.Binding.LocalUserID
		if _, err := s.SyncLifecycle(ctx, "agentwiki", "default", Lifecycle{
			LocalUserID: local, OperationID: "advance-departing-version", PreviousAuthVersion: 1, NextAuthVersion: 2,
		}); err != nil {
			t.Fatal(err)
		}
		departing, err := s.Authenticate(ctx, "agentwiki", "default", Authentication{
			LocalUserID: local, AuthenticationID: "reauthenticate-departing", Method: "password",
			AuthenticatedAt: time.Now().Unix(), AuthVersion: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		samePath, sameBinding := begin(clients["wiki"], departing)
		switchPath, switchBinding := begin(clients["wiki"], native)
		prompt := handoffUIRequest(t, h, http.StatusOK, http.MethodGet, switchPath, nil, old, switchBinding)
		form := handoffUIForm(t, prompt)
		form.Set("action", "confirm")

		bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		blocker, err := s.pool.Begin(bounded)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Rollback(blocker)
		sameURL, err := url.Parse(samePath)
		if err != nil {
			t.Fatal(err)
		}
		var held string
		if err = blocker.QueryRow(bounded, `SELECT id_hash FROM product_browser_handoffs WHERE id_hash=$1 FOR UPDATE`,
			digest(sameURL.Query().Get("handoff"))).Scan(&held); err != nil {
			t.Fatal(err)
		}

		request := httptest.NewRequestWithContext(bounded, http.MethodGet, "https://hub.test"+samePath, nil)
		request.AddCookie(old)
		request.AddCookie(sameBinding)
		stale := httptest.NewRecorder()
		finished := make(chan struct{})
		go func() {
			h.ServeHTTP(stale, request)
			close(finished)
		}()
		// The GET has inspected the old cookie and is waiting to redeem its
		// own handoff. Confirm the other handoff before letting it continue.
		waitForBlockedTransaction(t, bounded, blocker, finished)
		switched := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodPost, browserHandoffFinishPath, form, old, switchBinding)
		winner := handoffUICookie(t, switched, "unihub_session")
		if err = blocker.Commit(bounded); err != nil {
			t.Fatal(err)
		}
		<-finished
		if stale.Code != http.StatusConflict || len(stale.Result().Cookies()) != 0 {
			t.Fatalf("stale same-account GET returned HTTP %d with %d cookies, want HTTP 409 and no cookies", stale.Code, len(stale.Result().Cookies()))
		}
		checkSession(winner, native.Binding.UserID)
		checkSession(old, "")

		path, binding = begin(clients["wiki"], departing)
		stale = handoffUIRequest(t, h, http.StatusConflict, http.MethodGet, path, nil, old, binding)
		if len(stale.Result().Cookies()) != 0 {
			t.Fatal("GET with an already-replaced cookie wrote a cookie")
		}
	})

	t.Run("signed out and legacy product handoffs proceed directly", func(t *testing.T) {
		path, binding := begin(clients["wiki"], native)
		done := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodGet, path, nil, binding)
		wikiSession := handoffUICookie(t, done, "unihub_session")
		checkSession(wikiSession, native.Binding.UserID)

		path, binding = begin(clients["router"], router)
		done = handoffUIRequest(t, h, http.StatusSeeOther, http.MethodGet, path, nil, wikiSession, binding)
		checkSession(handoffUICookie(t, done, "unihub_session"), router.Binding.UserID)
		checkSession(wikiSession, "")
	})
}

func TestConsoleProductBrowserSessionRequiresEffectiveProductUse(t *testing.T) {
	s, h, clients, _ := setupBrowserHandoffUI(t)
	s.ConfigureBrowserClients(clients)
	ctx := context.Background()
	actor := nativeBrowserTestActor(t, s, "console-permission-source")
	path, binding := approveNativeBrowserTestHandoff(t, s, h, actor, "wiki", "")
	finish := handoffUIRequest(t, h, http.StatusSeeOther, http.MethodGet, path.RequestURI(), nil, binding)
	source := handoffUICookie(t, finish, "unihub_session")

	independent := &http.Cookie{Name: "unihub_session", Value: browserSecret()}
	if _, err := s.pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at)
		SELECT $1,issuer,subject,name,email,'independent-csrf',expires_at FROM browser_sessions WHERE id_hash=$2`,
		browserSessionDigest(independent.Value), browserSessionDigest(source.Value)); err != nil {
		t.Fatal(err)
	}
	check := func(cookie *http.Cookie, want bool) {
		t.Helper()
		w := handoffUIRequest(t, h, http.StatusOK, http.MethodGet, "/auth/session", nil, cookie)
		var body struct{ Authenticated bool }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Authenticated != want {
			t.Fatalf("console authenticated=%v, want %v (%v)", body.Authenticated, want, err)
		}
	}
	check(source, true)
	if _, err := s.pool.Exec(ctx, `UPDATE product_user_access SET permission_overrides='{"product":{"use":false}}',version=version+1
		WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, actor.Binding.UserID, actor.Binding.ProductID, actor.Binding.InstanceID); err != nil {
		t.Fatal(err)
	}
	check(source, false)
	check(independent, true)
}
