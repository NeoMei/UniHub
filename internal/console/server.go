// Package console owns the browser authentication boundary and console hosting.
package console

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/auth"
)

type Options struct {
	Issuer, ClientID, ClientSecret, PublicURL string
	Production, Sandbox                       bool
	StaticDir                                 string
}

type Server struct {
	opts                                           Options
	pool                                           *pgxpool.Pool
	verifier                                       auth.Verifier
	idVerifier                                     *oidc.IDTokenVerifier
	client                                         *http.Client
	authorizationEndpoint, tokenEndpoint, callback string
}

// boundedTransport caps all discovery, key and token responses, including those
// consumed by the OIDC library. No redirects are followed and requests time out.
type boundedTransport struct{ base http.RoundTripper }
type boundedBody struct {
	io.Reader
	io.Closer
}

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(r)
	if err == nil {
		res.Body = boundedBody{io.LimitReader(res.Body, 1<<20), res.Body}
	}
	return res, err
}

func New(ctx context.Context, pool *pgxpool.Pool, verifier auth.Verifier, opts Options) (*Server, error) {
	if !validURL(opts.Issuer, opts.Production, false) || !validURL(opts.PublicURL, opts.Production, true) || opts.ClientID == "" || strings.TrimSpace(opts.ClientID) != opts.ClientID {
		return nil, errors.New("invalid console OIDC configuration")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: boundedTransport{http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OIDC redirects forbidden") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(opts.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, errors.New("invalid discovery request")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("OIDC discovery unavailable")
	}
	defer res.Body.Close()
	var meta struct {
		Issuer        string `json:"issuer"`
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		JWKS          string `json:"jwks_uri"`
	}
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&meta) != nil || meta.Issuer != opts.Issuer {
		return nil, errors.New("invalid OIDC discovery")
	}
	issuerURL, _ := url.Parse(opts.Issuer)
	for _, endpoint := range []string{meta.Authorization, meta.Token, meta.JWKS} {
		if !validURL(endpoint, opts.Production, false) {
			return nil, errors.New("invalid OIDC endpoint")
		}
		u, _ := url.Parse(endpoint)
		if issuerURL.Scheme == "https" && u.Scheme != "https" {
			return nil, errors.New("insecure OIDC endpoint")
		}
	}
	clientCtx := oidc.ClientContext(ctx, client)
	return &Server{opts: opts, pool: pool, verifier: verifier, client: client, authorizationEndpoint: meta.Authorization, tokenEndpoint: meta.Token, callback: strings.TrimSuffix(opts.PublicURL, "/") + "/auth/callback", idVerifier: oidc.NewVerifier(opts.Issuer, oidc.NewRemoteKeySet(clientCtx, meta.JWKS), &oidc.Config{ClientID: opts.ClientID, SupportedSigningAlgs: []string{oidc.RS256}})}, nil
}
func validURL(raw string, production, origin bool) bool {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return false
	}
	if origin && u.Path != "" && u.Path != "/" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return !production && u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback()))
}
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func digest(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func constantEqual(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func (s *Server) cookie(w http.ResponseWriter, name, value string, expiry time.Time) {
	maxAge := int(time.Until(expiry).Seconds())
	if value == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, HttpOnly: true, Secure: s.opts.Production || strings.HasPrefix(s.opts.PublicURL, "https://"), SameSite: http.SameSiteLaxMode, Path: "/", MaxAge: maxAge, Expires: expiry})
}
func failure(w http.ResponseWriter, status int) { http.Error(w, http.StatusText(status), status) }
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) Handler(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if s.opts.Production {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		p := r.URL.Path
		if strings.HasPrefix(p, "/auth/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		switch p {
		case "/auth/login":
			if r.Method != http.MethodGet {
				failure(w, 405)
				return
			}
			s.login(w, r)
		case "/auth/callback":
			if r.Method != http.MethodGet {
				failure(w, 405)
				return
			}
			s.callbackRequest(w, r)
		case "/auth/session":
			if r.Method != http.MethodGet {
				failure(w, 405)
				return
			}
			s.session(w, r)
		case "/auth/logout":
			if r.Method != http.MethodPost {
				failure(w, 405)
				return
			}
			s.logout(w, r)
		default:
			if p == "/v1" || strings.HasPrefix(p, "/v1/") || p == "/ops/v1" || strings.HasPrefix(p, "/ops/v1/") || p == "/admin/v1" || strings.HasPrefix(p, "/admin/v1/") {
				w.Header().Set("Cache-Control", "no-store")
				s.api(api, w, r)
				return
			}
			if p == "/healthz" || p == "/webhooks/sandbox" || p == "/internal" || strings.HasPrefix(p, "/internal/") {
				api.ServeHTTP(w, r)
				return
			}
			if p == "/auth" || strings.HasPrefix(p, "/auth/") || p == "/api" || strings.HasPrefix(p, "/api/") {
				http.NotFound(w, r)
				return
			}
			s.static(w, r)
		}
	})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	state, err := randomSecret()
	if err != nil {
		failure(w, 500)
		return
	}
	binding, err := randomSecret()
	if err != nil {
		failure(w, 500)
		return
	}
	nonce, err := randomSecret()
	if err != nil {
		failure(w, 500)
		return
	}
	verifier, err := randomSecret()
	if err != nil {
		failure(w, 500)
		return
	}
	expiry := time.Now().Add(5 * time.Minute)
	// Expired credentials need no retention; cleanup is bounded by indexed expiry.
	if _, err = s.pool.Exec(r.Context(), `DELETE FROM browser_login_transactions WHERE expires_at<=now()`); err != nil {
		failure(w, 503)
		return
	}
	if _, err = s.pool.Exec(r.Context(), `INSERT INTO browser_login_transactions(state_hash,binding_hash,nonce,pkce_verifier,expires_at) VALUES($1,$2,$3,$4,$5)`, digest(state), digest(binding), nonce, verifier, expiry); err != nil {
		failure(w, 503)
		return
	}
	s.cookie(w, "unihub_login", binding, expiry)
	u, _ := url.Parse(s.authorizationEndpoint)
	q := u.Query()
	q.Set("client_id", s.opts.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", s.callback)
	q.Set("scope", "openid profile email")
	q.Set("state", state)
	q.Set("nonce", nonce)
	hash := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(hash[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), 302)
}
func (s *Server) callbackRequest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	binding, err := r.Cookie("unihub_login")
	if err != nil || state == "" || code == "" || len(q["state"]) != 1 || len(q["code"]) != 1 || q.Get("error") != "" {
		failure(w, 400)
		return
	}
	var nonce, pkce string
	err = s.pool.QueryRow(r.Context(), `DELETE FROM browser_login_transactions WHERE state_hash=$1 AND binding_hash=$2 AND expires_at>now() RETURNING nonce,pkce_verifier`, digest(state), digest(binding.Value)).Scan(&nonce, &pkce)
	if err != nil {
		failure(w, 400)
		return
	}
	s.cookie(w, "unihub_login", "", time.Unix(0, 0))
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {s.opts.ClientID}, "code": {code}, "redirect_uri": {s.callback}, "code_verifier": {pkce}}
	if s.opts.ClientSecret != "" {
		form.Set("client_secret", s.opts.ClientSecret)
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		failure(w, 400)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.client.Do(req)
	if err != nil {
		failure(w, 400)
		return
	}
	defer res.Body.Close()
	var tokens struct {
		Access string `json:"access_token"`
		ID     string `json:"id_token"`
		Type   string `json:"token_type"`
	}
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&tokens) != nil || tokens.Access == "" || tokens.ID == "" || !strings.EqualFold(tokens.Type, "Bearer") {
		failure(w, 400)
		return
	}
	token, err := s.idVerifier.Verify(r.Context(), tokens.ID)
	if err != nil || !constantEqual(token.Nonce, nonce) {
		failure(w, 400)
		return
	}
	if token.AccessTokenHash != "" && token.VerifyAccessToken(tokens.Access) != nil {
		failure(w, 400)
		return
	}
	if s.verifier == nil {
		failure(w, 503)
		return
	}
	claims, err := s.verifier.Verify(r.Context(), tokens.Access)
	if err != nil || claims.Issuer != s.opts.Issuer || claims.Subject != token.Subject {
		failure(w, 400)
		return
	}
	// The access token was verified above; decode its signed exp to enforce the
	// stricter of both token lifetimes and the one-hour browser session ceiling.
	parts := strings.Split(tokens.Access, ".")
	var access struct {
		Expiry int64 `json:"exp"`
	}
	if len(parts) != 3 {
		failure(w, 400)
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &access) != nil || access.Expiry <= time.Now().Unix() {
		failure(w, 400)
		return
	}
	expiry := time.Now().Add(time.Hour)
	if token.Expiry.Before(expiry) {
		expiry = token.Expiry
	}
	if exp := time.Unix(access.Expiry, 0); exp.Before(expiry) {
		expiry = exp
	}
	if _, err := accounts.New(s.pool).EnsureIdentity(r.Context(), accounts.Identity{Issuer: claims.Issuer, Subject: claims.Subject, Name: claims.Name, Email: claims.Email}); err != nil {
		failure(w, 503)
		return
	}
	id, err := randomSecret()
	if err != nil {
		failure(w, 500)
		return
	}
	csrf, err := randomSecret()
	if err != nil {
		failure(w, 500)
		return
	}
	// Rotate an existing session at successful authentication.
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		failure(w, 503)
		return
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}()
	var active bool
	var cutoff time.Time
	var centralID string
	if err = tx.QueryRow(r.Context(), `SELECT u.id,u.active,u.session_not_before FROM users u JOIN identities i ON i.user_id=u.id WHERE i.issuer=$1 AND i.subject=$2 FOR SHARE OF u`, claims.Issuer, claims.Subject).Scan(&centralID, &active, &cutoff); err != nil {
		failure(w, 503)
		return
	}
	if !active || (cutoff.After(time.Unix(0, 0)) && (claims.IssuedAt <= 0 || time.Unix(claims.IssuedAt, 0).Before(cutoff))) {
		failure(w, 401)
		return
	}
	if old, err := r.Cookie("unihub_session"); err == nil {
		if _, err = tx.Exec(r.Context(), `DELETE FROM browser_sessions WHERE id_hash=$1`, digest(old.Value)); err != nil {
			failure(w, 503)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM browser_sessions WHERE expires_at<=now()`); err != nil {
		failure(w, 503)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, digest(id), claims.Issuer, claims.Subject, claims.Name, claims.Email, csrf, expiry); err != nil {
		failure(w, 503)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE users SET last_authenticated_at=clock_timestamp() WHERE id=$1`, centralID); err != nil {
		failure(w, 503)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO platform_admin_audit(actor_id,action,target_id,reason,detail) VALUES($1,'identity.login',$1,'verified OIDC authorization-code authentication','{}')`, centralID); err != nil {
		failure(w, 503)
		return
	}
	if tx.Commit(r.Context()) != nil {
		failure(w, 503)
		return
	}
	s.cookie(w, "unihub_session", id, expiry)
	http.Redirect(w, r, "/", 302)
}
func (s *Server) identity(r *http.Request) (auth.Claims, string, bool) {
	c, err := r.Cookie("unihub_session")
	if err != nil || len(c.Value) != 43 {
		return auth.Claims{}, "", false
	}
	var claims auth.Claims
	var csrf string
	err = s.pool.QueryRow(r.Context(), `SELECT b.issuer,b.subject,b.name,b.email,b.csrf_token,extract(epoch from b.expires_at)::bigint,extract(epoch from b.created_at)::bigint FROM browser_sessions b JOIN identities i ON i.issuer=b.issuer AND i.subject=b.subject JOIN users u ON u.id=i.user_id WHERE b.id_hash=$1 AND b.expires_at>now() AND u.active AND b.created_at>=u.session_not_before AND NOT EXISTS(SELECT 1 FROM product_browser_session_origins o LEFT JOIN product_auth_subjects p ON p.binding_id=o.binding_id LEFT JOIN product_runtime_bindings pb ON pb.id=p.binding_id LEFT JOIN users pu ON pu.id=pb.user_id LEFT JOIN product_user_access a ON a.user_id=pb.user_id AND a.product_id=pb.product_id AND a.instance_id=pb.instance_id WHERE o.session_id_hash=b.id_hash AND (p.binding_id IS NULL OR p.deleted OR p.auth_version<>o.auth_version OR NOT pu.active OR NOT a.active))`, digest(c.Value)).Scan(&claims.Issuer, &claims.Subject, &claims.Name, &claims.Email, &csrf, &claims.ExpiresAt, &claims.IssuedAt)
	return claims, csrf, err == nil
}
func (s *Server) csrf(r *http.Request, expected string) bool {
	return r.Header.Get("Origin") == strings.TrimSuffix(s.opts.PublicURL, "/") && len(r.Header.Values("Origin")) == 1 && len(r.Header.Values("X-CSRF-Token")) == 1 && constantEqual(r.Header.Get("X-CSRF-Token"), expected)
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	claims, csrf, ok := s.identity(r)
	if !ok {
		jsonResponse(w, 200, map[string]any{"authenticated": false, "sandbox": s.opts.Sandbox})
		return
	}
	user, err := accounts.New(s.pool).EnsureIdentity(r.Context(), accounts.Identity{Issuer: claims.Issuer, Subject: claims.Subject, Name: claims.Name, Email: claims.Email})
	if err != nil {
		failure(w, 503)
		return
	}
	jsonResponse(w, 200, map[string]any{"authenticated": true, "user": user, "csrf_token": csrf, "sandbox": s.opts.Sandbox})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_, csrf, ok := s.identity(r)
	if !ok {
		failure(w, 401)
		return
	}
	if !s.csrf(r, csrf) {
		failure(w, 403)
		return
	}
	cookie, _ := r.Cookie("unihub_session")
	if _, err := s.pool.Exec(r.Context(), `DELETE FROM browser_sessions WHERE id_hash=$1`, digest(cookie.Value)); err != nil {
		failure(w, 503)
		return
	}
	s.cookie(w, "unihub_session", "", time.Unix(0, 0))
	w.WriteHeader(204)
}
func (s *Server) api(api http.Handler, w http.ResponseWriter, r *http.Request) {
	if _, present := r.Header["Authorization"]; present {
		if len(r.Header.Values("Authorization")) != 1 || strings.TrimSpace(r.Header.Get("Authorization")) == "" {
			failure(w, 401)
			return
		}
		api.ServeHTTP(w, r)
		return
	}
	claims, csrf, ok := s.identity(r)
	if !ok {
		failure(w, 401)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && !s.csrf(r, csrf) {
		failure(w, 403)
		return
	}
	api.ServeHTTP(w, r.WithContext(auth.WithBrowserClaims(r.Context(), claims)))
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		failure(w, 405)
		return
	}
	if s.opts.StaticDir == "" {
		http.NotFound(w, r)
		return
	}
	clean := path.Clean("/" + r.URL.Path)
	for _, segment := range strings.Split(clean, "/") {
		if strings.HasPrefix(segment, ".") {
			http.NotFound(w, r)
			return
		}
	}
	root, err := os.OpenRoot(s.opts.StaticDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	target := strings.TrimPrefix(clean, "/")
	if target == "" {
		target = "."
	}
	file, err := root.Open(target)
	if err == nil {
		defer file.Close()
		info, statErr := file.Stat()
		if statErr == nil && !info.IsDir() {
			http.ServeContent(w, r, path.Base(clean), info.ModTime(), file)
			return
		}
	}
	if path.Ext(clean) != "" || strings.HasPrefix(clean, "/assets/") {
		http.NotFound(w, r)
		return
	}
	index, err := root.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer index.Close()
	info, err := index.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", info.ModTime(), index)
}

