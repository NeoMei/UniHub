package console

import (
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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

type issuerFixture struct {
	server                 *httptest.Server
	key                    *rsa.PrivateKey
	nonce, challenge, mode string
	exchanges              int
}

func newIssuer(t *testing.T) *issuerFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &issuerFixture{key: key}
	i.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			if i.mode == "discovery redirect" {
				http.Redirect(w, r, i.server.URL+"/secret", 302)
				return
			}
			endpoint := i.server.URL + "/token"
			if i.mode == "remote http" {
				endpoint = "http://example.com/token"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": i.server.URL, "authorization_endpoint": i.server.URL + "/authorize", "token_endpoint": endpoint, "jwks_uri": i.server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/token":
			i.exchanges++
			_ = r.ParseForm()
			digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != "http://localhost:8088/auth/callback" || r.Form.Get("code") != "single-code" || base64.RawURLEncoding.EncodeToString(digest[:]) != i.challenge {
				http.Error(w, "bad exchange", 400)
				return
			}
			if i.mode == "token redirect" {
				http.Redirect(w, r, i.server.URL+"/secret", 302)
				return
			}
			nonce := i.nonce
			if i.mode == "bad nonce" {
				nonce = "wrong"
			}
			id := i.jwt(t, map[string]any{"nonce": nonce})
			access := i.jwt(t, map[string]any{"tokenType": "access-token", "scope": "openid profile email"})
			if i.mode == "wrong identity" {
				access = i.jwt(t, map[string]any{"sub": "other", "tokenType": "access-token", "scope": "openid"})
			}
			if i.mode == "invalid access" {
				access = i.jwt(t, map[string]any{"tokenType": "id-token", "scope": "openid"})
			}
			if i.mode == "missing ID" {
				id = ""
			}
			if i.mode == "wrong audience" {
				id = i.jwt(t, map[string]any{"nonce": nonce, "aud": "other"})
			}
			if i.mode == "short expiry" {
				access = i.jwt(t, map[string]any{"exp": time.Now().Add(30 * time.Second).Unix(), "tokenType": "access-token", "scope": "openid"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "id_token": id, "token_type": "Bearer", "expires_in": 7200, "refresh_token": "never-store-refresh"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(i.server.Close)
	return i
}
func (i *issuerFixture) jwt(t *testing.T, extra map[string]any) string {
	t.Helper()
	c := map[string]any{"iss": i.server.URL, "sub": "alice", "aud": "client", "exp": time.Now().Add(2 * time.Hour).Unix(), "iat": time.Now().Unix(), "name": "Alice", "email": "alice@example.com"}
	for k, v := range extra {
		c[k] = v
	}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
	p, _ := json.Marshal(c)
	raw := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	hash := sha256.Sum256([]byte(raw))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return raw + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func fixture(t *testing.T) (*Server, *issuerFixture, *pgxpool.Pool, http.Handler) {
	t.Helper()
	pool := testutil.Database(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	i := newIssuer(t)
	v, err := auth.NewOIDC(context.Background(), i.server.URL, "client")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), pool, v, Options{Issuer: i.server.URL, ClientID: "client", ClientSecret: "test-secret", PublicURL: "http://localhost:8088", Sandbox: true})
	if err != nil {
		t.Fatal(err)
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := auth.BrowserClaims(r.Context())
		if r.Header.Get("Authorization") != "" {
			if r.Header.Get("Authorization") != "Bearer valid" {
				w.WriteHeader(401)
				return
			}
			w.WriteHeader(202)
			return
		}
		if !ok || c.Subject != "alice" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(204)
	})
	return s, i, pool, s.Handler(api)
}
func request(h http.Handler, method, path string, cookie *http.Cookie, origin, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, i *issuerFixture, h http.Handler) (*http.Cookie, string, *httptest.ResponseRecorder) {
	t.Helper()
	w := request(h, "GET", "/auth/login", nil, "", "")
	if w.Code != 302 {
		t.Fatalf("login status=%d %s", w.Code, w.Body)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	i.nonce = q.Get("nonce")
	i.challenge = q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || len(i.nonce) < 32 || len(i.challenge) < 32 || q.Get("redirect_uri") != "http://localhost:8088/auth/callback" {
		t.Fatalf("unsafe authorization request: %s", u)
	}
	var pending *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "unihub_login" {
			pending = c
		}
	}
	if pending == nil {
		t.Fatal("missing login binding")
	}
	path := "/auth/callback?code=single-code&state=" + url.QueryEscape(q.Get("state"))
	return pending, path, request(h, "GET", path, pending, "", "")
}
func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "unihub_session" && c.MaxAge > 0 {
			if !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("unsafe cookie")
			}
			return c
		}
	}
	t.Fatalf("missing session cookie status=%d body=%s", w.Code, w.Body)
	return nil
}
func TestBrowserLoginSessionCSRFAndLogout(t *testing.T) {
	_, i, pool, h := fixture(t)
	pending, path, w := login(t, i, h)
	if w.Code != 302 {
		t.Fatalf("callback=%d %s", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)
	if w := request(h, "GET", path, pending, "", ""); w.Code != 400 {
		t.Fatalf("replay=%d", w.Code)
	}
	w = request(h, "GET", "/auth/session", cookie, "", "")
	var status struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf_token"`
		User          struct{ ID, Name, Email string }
		Sandbox       bool `json:"sandbox"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.User.ID == "" || status.User.Name != "Alice" || !status.Sandbox || len(status.CSRF) < 32 {
		t.Fatalf("session=%s", w.Body)
	}
	for _, bad := range []struct{ origin, csrf string }{{"", ""}, {"http://localhost:8088", ""}, {"https://evil.example", status.CSRF}, {"http://localhost:8088", "wrong"}} {
		if w := request(h, "POST", "/v1/mutate", cookie, bad.origin, bad.csrf); w.Code != 403 {
			t.Fatalf("csrf bypass=%d", w.Code)
		}
	}
	if w := request(h, "POST", "/v1/mutate", cookie, "http://localhost:8088", status.CSRF); w.Code != 204 {
		t.Fatalf("valid cookie=%d", w.Code)
	}
	for _, token := range []string{"valid", "invalid"} {
		r := httptest.NewRequest("POST", "/v1/mutate", nil)
		r.AddCookie(cookie)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 202
		if token == "invalid" {
			want = 401
		}
		if w.Code != want {
			t.Fatalf("bearer %s status=%d", token, w.Code)
		}
	}
	var stored string
	if err := pool.QueryRow(context.Background(), `SELECT row_to_json(s)::text FROM browser_sessions s`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{cookie.Value, "never-store-refresh", "access_token", "id_token"} {
		if strings.Contains(stored, secret) {
			t.Fatalf("stored credential %s", secret)
		}
	}
	if w := request(h, "POST", "/auth/logout", cookie, "https://evil.example", status.CSRF); w.Code != 403 {
		t.Fatal("logout CSRF")
	}
	if w := request(h, "POST", "/auth/logout", cookie, "http://localhost:8088", status.CSRF); w.Code != 204 {
		t.Fatalf("logout=%d", w.Code)
	}
	if w := request(h, "GET", "/v1/test", cookie, "", ""); w.Code != 401 {
		t.Fatalf("revoked session=%d", w.Code)
	}
}
func TestLoginRejectsBadNonceIdentityAndRedirect(t *testing.T) {
	for _, mode := range []string{"bad nonce", "wrong identity", "token redirect", "invalid access", "missing ID", "wrong audience"} {
		t.Run(mode, func(t *testing.T) {
			_, i, _, h := fixture(t)
			i.mode = mode
			pending, path, w := login(t, i, h)
			if w.Code != 400 {
				t.Fatalf("invalid callback=%d %s", w.Code, w.Body)
			}
			if w := request(h, "GET", path, pending, "", ""); w.Code != 400 {
				t.Fatal("failed transaction reusable")
			}
			if i.exchanges != 1 {
				t.Fatalf("exchange count=%d", i.exchanges)
			}
		})
	}
}
func TestLoginStateAndBrowserBinding(t *testing.T) {
	_, i, _, h := fixture(t)
	w := request(h, "GET", "/auth/login", nil, "", "")
	u, _ := url.Parse(w.Header().Get("Location"))
	state := u.Query().Get("state")
	for _, p := range []string{"/auth/callback?code=single-code&state=" + state, "/auth/callback?code=single-code&state=wrong"} {
		if w := request(h, "GET", p, nil, "", ""); w.Code != 400 {
			t.Fatal("unbound callback accepted")
		}
	}
	if i.exchanges != 0 {
		t.Fatal("unbound request contacted token endpoint")
	}
}
func TestSessionExpiresAndIsCapped(t *testing.T) {
	_, i, pool, h := fixture(t)
	i.mode = "short expiry"
	_, _, w := login(t, i, h)
	c := sessionCookie(t, w)
	if c.MaxAge > 30 {
		t.Fatalf("lifetime=%d", c.MaxAge)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE browser_sessions SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/v1/test", c, "", ""); w.Code != 401 {
		t.Fatalf("expired=%d", w.Code)
	}
	w = request(h, "GET", "/auth/session", c, "", "")
	if strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatal("expired session shown authenticated")
	}
}
func TestConfigurationAndDiscoveryTransport(t *testing.T) {
	for _, u := range []string{"http://localhost:8088", "https://example.com/path", "https://example.com?x=1", "https://user:pass@example.com"} {
		if s, err := New(context.Background(), nil, nil, Options{Issuer: "https://id.example.com", ClientID: "client", PublicURL: u, Production: true}); err == nil || s != nil {
			t.Fatalf("unsafe production config accepted %s", u)
		}
	}
	for _, mode := range []string{"discovery redirect", "remote http"} {
		i := newIssuer(t)
		i.mode = mode
		if s, err := New(context.Background(), nil, nil, Options{Issuer: i.server.URL, ClientID: "client", PublicURL: "http://localhost:8088"}); err == nil || s != nil {
			t.Fatalf("unsafe metadata accepted %s", mode)
		}
	}
}
func TestStaticSPABoundaryAndSecurityHeaders(t *testing.T) {
	s, _, _, _ := fixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("console-index"), 0600); err != nil {
		t.Fatal(err)
	}
	s.opts.StaticDir = dir
	h := s.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, p := range []string{"/", "/members"} {
		w := request(h, "GET", p, nil, "", "")
		if w.Code != 200 || w.Body.String() != "console-index" {
			t.Fatalf("SPA %s: %d %s", p, w.Code, w.Body)
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing security headers")
		}
	}
	for _, p := range []string{"/auth/missing", "/api/missing", "/assets/missing.js"} {
		if w := request(h, "GET", p, nil, "", ""); w.Code != 404 {
			t.Fatalf("SPA swallowed %s", p)
		}
	}
	if w := request(h, "POST", "/internal/settle", nil, "", ""); w.Code != 204 {
		t.Fatal("service route swallowed")
	}
}

// These tests catch consumption of an unbound callback, session fixation,
// CSRF omission on logout, and accidentally trusting bearer-plus-cookie context.
func TestBoundStateTamperDoesNotAuthenticateAndSessionRotates(t *testing.T) {
	_, i, pool, h := fixture(t)
	w := request(h, "GET", "/auth/login", nil, "", "")
	var pending *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "unihub_login" {
			pending = c
		}
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	q := u.Query()
	i.nonce = q.Get("nonce")
	i.challenge = q.Get("code_challenge")
	if w := request(h, "GET", "/auth/callback?code=single-code&state=tampered", pending, "", ""); w.Code != 400 {
		t.Fatal("tampered state authenticated")
	}
	if i.exchanges != 0 {
		t.Fatal("tampered state reached issuer")
	}
	cb := "/auth/callback?code=single-code&state=" + url.QueryEscape(q.Get("state"))
	w = request(h, "GET", cb, pending, "", "")
	first := sessionCookie(t, w)
	if first.MaxAge > 3600 {
		t.Fatalf("session exceeded one hour: %d", first.MaxAge)
	}
	w = request(h, "GET", "/auth/login", nil, "", "")
	for _, c := range w.Result().Cookies() {
		if c.Name == "unihub_login" {
			pending = c
		}
	}
	u, _ = url.Parse(w.Header().Get("Location"))
	q = u.Query()
	i.nonce = q.Get("nonce")
	i.challenge = q.Get("code_challenge")
	r := httptest.NewRequest("GET", "/auth/callback?code=single-code&state="+url.QueryEscape(q.Get("state")), nil)
	r.AddCookie(pending)
	r.AddCookie(first)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	second := sessionCookie(t, w)
	if first.Value == second.Value {
		t.Fatal("session did not rotate")
	}
	if w := request(h, "GET", "/v1/test", first, "", ""); w.Code != 401 {
		t.Fatal("old session survives rotation")
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM browser_sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("sessions after rotation=%d", count)
	}
	if w := request(h, "POST", "/auth/logout", second, "http://localhost:8088", ""); w.Code != 403 {
		t.Fatal("logout without CSRF accepted")
	}
}

func TestExpiredLoginAndMissingCookiesNeverExchange(t *testing.T) {
	_, i, pool, h := fixture(t)
	w := request(h, "GET", "/auth/login", nil, "", "")
	u, _ := url.Parse(w.Header().Get("Location"))
	var pending *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "unihub_login" {
			pending = c
		}
	}
	if _, err := pool.Exec(context.Background(), `UPDATE browser_login_transactions SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/auth/callback?code=single-code&state="+url.QueryEscape(u.Query().Get("state")), pending, "", ""); w.Code != 400 {
		t.Fatal("expired login accepted")
	}
	if i.exchanges != 0 {
		t.Fatal("expired login reached issuer")
	}
	if w := request(h, "GET", "/v1/test", nil, "", ""); w.Code != 401 {
		t.Fatal("anonymous API authenticated")
	}
}

func TestBearerNeverCarriesCookieIdentity(t *testing.T) {
	s, i, _, h := fixture(t)
	_, _, w := login(t, i, h)
	cookie := sessionCookie(t, w)
	h = s.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.BrowserClaims(r.Context()); ok {
			t.Error("bearer request inherited browser identity")
		}
		w.WriteHeader(418)
	}))
	for _, header := range []string{"Bearer invalid", "Basic invalid"} {
		r := httptest.NewRequest("POST", "/v1/test", nil)
		r.AddCookie(cookie)
		r.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 418 {
			t.Fatalf("explicit auth not delegated: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/v1/test", nil)
	r.AddCookie(cookie)
	r.Header.Set("Authorization", "")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("empty explicit auth fell back to cookie")
	}
}

func TestServiceRoutesAndOperatorBrowserBoundary(t *testing.T) {
	s, i, _, h := fixture(t)
	_, _, w := login(t, i, h)
	cookie := sessionCookie(t, w)
	h = s.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ops/v1") {
			if _, ok := auth.BrowserClaims(r.Context()); !ok {
				t.Error("operator request lost trusted identity")
			}
		}
		w.WriteHeader(204)
	}))
	for _, p := range []string{"/healthz", "/webhooks/sandbox", "/internal/service"} {
		if w := request(h, "POST", p, nil, "", ""); w.Code != 204 {
			t.Fatalf("service route %s status=%d", p, w.Code)
		}
	}
	if w := request(h, "GET", "/ops/v1/accounts", nil, "", ""); w.Code != 401 {
		t.Fatalf("operator route unprotected=%d", w.Code)
	}
	if w := request(h, "GET", "/ops/v1/accounts", cookie, "", ""); w.Code != 204 {
		t.Fatalf("operator route lost cookie=%d", w.Code)
	}
	if w := request(h, "POST", "/ops/v1/mutate", cookie, "", ""); w.Code != 403 {
		t.Fatalf("operator CSRF bypass=%d", w.Code)
	}
}

func TestStaticCannotEscapeRootThroughSymlink(t *testing.T) {
	s, _, _, _ := fixture(t)
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret-outside-root"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "public.txt")); err != nil {
		t.Fatal(err)
	}
	s.opts.StaticDir = dir
	h := s.Handler(http.NotFoundHandler())
	w := request(h, "GET", "/public.txt", nil, "", "")
	if w.Code != 404 || strings.Contains(w.Body.String(), "secret-outside-root") {
		t.Fatalf("static symlink escaped root: %d %s", w.Code, w.Body)
	}
}

func TestPlatformAPIBoundaryRequiresSessionAndCSRF(t *testing.T) {
	_, i, _, h := fixture(t)
	if w := request(h, "POST", "/admin/v1/mutate", nil, "", ""); w.Code != 401 {
		t.Fatalf("unauthed management=%d", w.Code)
	}
	_, _, loginResponse := login(t, i, h)
	cookie := sessionCookie(t, loginResponse)
	response := request(h, "GET", "/auth/session", cookie, "", "")
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if e := json.Unmarshal(response.Body.Bytes(), &session); e != nil {
		t.Fatal(e)
	}
	if w := request(h, "POST", "/admin/v1/mutate", cookie, "", ""); w.Code != 403 {
		t.Fatalf("missing csrf=%d", w.Code)
	}
	w := request(h, "POST", "/admin/v1/mutate", cookie, "http://localhost:8088", session.CSRF)
	if w.Code != 204 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("admin session=%d %s", w.Code, w.Body)
	}
}

func TestDisabledAndRevokedBrowserCannotUseSessionEndpoint(t *testing.T) {
	_, i, p, h := fixture(t)
	_, _, response := login(t, i, h)
	cookie := sessionCookie(t, response)
	if w := request(h, "GET", "/auth/session", cookie, "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	_, e := p.Exec(context.Background(), `UPDATE users SET session_not_before=clock_timestamp()+interval '1 second'`)
	if e != nil {
		t.Fatal(e)
	}
	w := request(h, "GET", "/auth/session", cookie, "", "")
	if strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatalf("revoked browser exposed session %s", w.Body)
	}
}
