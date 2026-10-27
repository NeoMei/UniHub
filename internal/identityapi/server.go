// Package identityapi exposes product-owned JSON identity flows while Casdoor
// remains the only password authority and OAuth token issuer.
package identityapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/database"
	"unihub/internal/products"
)

const registrationPath = "/v1/identity/registrations"
const authorizePath = "/v1/identity/authorize/password"
const tokenPath = "/v1/identity/token"
const scopes = "openid profile email"

// Options is private server configuration. AuditRedactionVerified may be enabled
// only after deployment readback verifies Casdoor/proxy/tracing redaction; it is
// never accepted from a product or an HTTP request. Default capabilities are off.
type Options struct {
	Issuer, ManagementClientID, ManagementClientSecret string
	SigningKey                                         []byte
	Clients                                            map[string]products.Client
	Production, AuditRedactionVerified                 bool
	// TrustLoopbackProxy accepts a single X-Real-IP only from a loopback peer.
	// The reverse proxy must replace the header with its observed remote address.
	TrustLoopbackProxy bool
	// A client with a configured credential is a trusted server client. These are
	// independent UniHub credentials, never Casdoor application/admin secrets.
	ClientCredentials map[string]string
}
type Server struct {
	opts      Options
	pool      *pgxpool.Pool
	client    *http.Client
	verifiers map[string]*oidc.IDTokenVerifier
}
type cappedTransport struct{ base http.RoundTripper }
type cappedBody struct {
	io.Reader
	io.Closer
}

func (t cappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(r)
	if err == nil {
		res.Body = cappedBody{io.LimitReader(res.Body, (1<<20)+1), res.Body}
	}
	return res, err
}

func New(ctx context.Context, pool *pgxpool.Pool, opts Options) (*Server, error) {
	s := &Server{opts: opts, pool: pool, verifiers: map[string]*oidc.IDTokenVerifier{}}
	s.opts.Clients = map[string]products.Client{}
	enabled := false
	for _, c := range opts.Clients {
		if c.ClientID == "" {
			return nil, errors.New("headless client ID is required")
		}
		if _, ok := s.opts.Clients[c.ClientID]; ok {
			return nil, errors.New("duplicate headless client")
		}
		c.RedirectURIs = slices.Clone(c.RedirectURIs)
		c.Origins = slices.Clone(c.Origins)
		c.Scopes = slices.Clone(c.Scopes)
		s.opts.Clients[c.ClientID] = c
		if c.PasswordLogin || c.Registration {
			enabled = true
			if c.Organization != "unihub" || !identifier.MatchString(c.Application) || strings.Contains(c.ClientID, "-org-") || c.ClientID == opts.ManagementClientID || len(c.RedirectURIs) == 0 {
				return nil, errors.New("invalid headless client policy")
			}
		}
	}
	if !enabled {
		return s, nil
	}
	if pool == nil || len(opts.SigningKey) < 32 || !opts.AuditRedactionVerified || opts.ManagementClientID == "" || opts.ManagementClientSecret == "" || !safeURL(opts.Issuer, opts.Production) {
		return nil, errors.New("headless identity requires database, independent key, reviewed audit redaction and private provider configuration")
	}
	s.opts.SigningKey = slices.Clone(opts.SigningKey)
	s.client = &http.Client{Timeout: 5 * time.Second, Transport: cappedTransport{http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("identity redirects forbidden") }}
	var meta struct {
		Issuer string `json:"issuer"`
		Token  string `json:"token_endpoint"`
		JWKS   string `json:"jwks_uri"`
	}
	if err := s.providerJSON(ctx, http.MethodGet, "/.well-known/openid-configuration", nil, false, &meta); err != nil || meta.Issuer != opts.Issuer || meta.Token != strings.TrimSuffix(opts.Issuer, "/")+"/api/login/oauth/access_token" || !sameOrigin(opts.Issuer, meta.JWKS) || !safeURL(meta.JWKS, opts.Production) {
		return nil, errors.New("invalid headless OIDC discovery")
	}
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(context.WithoutCancel(ctx), s.client), meta.JWKS)
	for id, c := range s.opts.Clients {
		if c.PasswordLogin || c.Registration {
			s.verifiers[id] = oidc.NewVerifier(opts.Issuer, keys, &oidc.Config{ClientID: id, SupportedSigningAlgs: []string{oidc.RS256}})
		}
	}
	if _, err := s.organization(ctx); err != nil {
		return nil, errors.New("unsupported headless organization policy")
	}
	if err := s.managementPolicy(ctx); err != nil {
		return nil, errors.New("management application must belong only to unihub")
	}
	return s, nil
}
func safeURL(raw string, production bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || strings.TrimSpace(raw) != raw {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return !production && u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}
func sameOrigin(a, b string) bool {
	x, e := url.Parse(a)
	y, f := url.Parse(b)
	return e == nil && f == nil && x.Scheme == y.Scheme && x.Host == y.Host
}
func (s *Server) mac(parts ...string) string {
	h := hmac.New(sha256.New, s.opts.SigningKey)
	for _, v := range parts {
		h.Write([]byte{0})
		h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("secure random source unavailable")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
func randomID() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("secure random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func equal(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code string) {
	jsonResponse(w, status, map[string]string{"error": code})
}
func interaction(w http.ResponseWriter, kind string) {
	jsonResponse(w, 409, map[string]any{"error": "interaction_required", "interaction": map[string]any{"kind": kind, "supported": false}})
}
func (s *Server) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p != registrationPath && p != authorizePath && p != tokenPath {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			fail(w, 405, "method_not_allowed")
			return
		}
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || media != "application/json" {
			fail(w, 415, "unsupported_media_type")
			return
		}
		if r.URL.RawQuery != "" {
			fail(w, 400, "invalid_request")
			return
		}
		switch p {
		case registrationPath:
			s.register(w, r)
		case authorizePath:
			s.authorize(w, r)
		case tokenPath:
			s.token(w, r)
		}
	})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "invalid_request")
		return false
	}
	return true
}
func (s *Server) clientFor(w http.ResponseWriter, r *http.Request, id string, registration bool) (products.Client, bool) {
	c, ok := s.opts.Clients[id]
	if !ok || registration && !c.Registration || !registration && !c.PasswordLogin {
		fail(w, 403, "client_not_enabled")
		return c, false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !c.AllowsOrigin(origin) {
		fail(w, 403, "origin_not_allowed")
		return c, false
	}
	if secret := s.opts.ClientCredentials[id]; secret != "" {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == r.Header.Get("Authorization") || !equal(s.mac("client", raw), s.mac("client", secret)) {
			fail(w, 401, "invalid_client")
			return c, false
		}
	}
	return c, true
}

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,63}$`)
var pkce = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var authorizationCode = regexp.MustCompile(`^[0-9a-f]{64}$`)
var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func validUsername(v string) bool { return identifier.MatchString(v) && v == strings.ToLower(v) }
func validPassword(v, username, email string, options []string) bool {
	n := utf8.RuneCountInString(v)
	if n < 12 || n > 128 || len(v) > 72 {
		return false
	}
	low, up, digit, special := false, false, false, false
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
		low = low || r >= 'a' && r <= 'z'
		up = up || r >= 'A' && r <= 'Z'
		digit = digit || r >= '0' && r <= '9'
		special = special || r >= 33 && r <= 47 || r >= 58 && r <= 64 || r >= 91 && r <= 96 || r >= 123 && r <= 126
	}
	if !low || !up || !digit {
		return false
	}
	lower := strings.ToLower(v)
	if strings.Contains(lower, username) {
		return false
	}
	name, _, _ := strings.Cut(strings.ToLower(email), "@")
	if len(name) >= 3 && strings.Contains(lower, name) {
		return false
	}
	for _, option := range options {
		switch option {
		case "AtLeast6", "AtLeast8", "Aa123":
		case "SpecialChar":
			if !special {
				return false
			}
		case "NoRepeat":
			for i := 1; i < len(v); i++ {
				if v[i] == v[i-1] {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}
func validEmail(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > 254 || v != strings.TrimSpace(v) || strings.ContainsAny(v, "\r\n\t ") {
		return false
	}
	parts := strings.Split(v, "@")
	return len(parts) == 2 && parts[0] != "" && strings.Contains(parts[1], ".") && !strings.ContainsAny(v, "<>\"\\")
}

// limit uses durable HMAC identifiers; raw addresses and usernames are not stored.
func (s *Server) limit(ctx context.Context, r *http.Request, c products.Client, user, kind string) bool {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false
	}
	defer database.Rollback(tx)
	retryAt, err := s.limitInTx(ctx, tx, r, c, user, kind)
	if err != nil || tx.Commit(ctx) != nil {
		return false
	}
	return retryAt.IsZero()
}

// limitInTx returns the denied fixed window's end, or zero when allowed. The
// caller commits counters even when denied, and distinguishes storage errors.
func (s *Server) limitInTx(ctx context.Context, tx pgx.Tx, r *http.Request, c products.Client, user, kind string) (time.Time, error) {
	host := s.rateAddress(r)
	type dimension struct {
		name, value string
		duration    time.Duration
		max         int
	}
	dims := []dimension{{"ip", host, time.Minute, 10}, {"global-ip", host, time.Minute, 40}}
	if kind == "register" {
		dims = []dimension{{"register-ip", host, time.Hour, 3}, {"register-global-ip", host, time.Hour, 12}}
	} else if kind == "token" {
		dims = []dimension{{"token-ip", host, time.Minute, 30}}
	} else {
		dims = append(dims, dimension{"username", strings.ToLower(user), 15 * time.Minute, 5})
	}
	now := time.Now().UTC()
	for _, d := range dims {
		window := now.Truncate(d.duration)
		var hits int
		client := c.ClientID
		if strings.HasPrefix(d.name, "global-") || strings.Contains(d.name, "global-ip") {
			client = ""
		}
		key := s.mac("limit", kind, d.name, client, d.value)
		if err := tx.QueryRow(ctx, `INSERT INTO identity_rate_limits(key_hash,window_start,hits) VALUES($1,$2,1) ON CONFLICT(key_hash,window_start) DO UPDATE SET hits=identity_rate_limits.hits+1 RETURNING hits`, key, window).Scan(&hits); err != nil {
			return time.Time{}, err
		}
		if hits > d.max {
			// Denied IPs cannot create fresh attacker-chosen username buckets.
			return window.Add(d.duration), nil
		}
	}
	return time.Time{}, nil
}

func (s *Server) rateAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if s.opts.TrustLoopbackProxy && peer != nil && peer.IsLoopback() {
		values := r.Header.Values("X-Real-IP")
		if len(values) == 1 {
			if ip := net.ParseIP(values[0]); ip != nil {
				return ip.String()
			}
		}
	}
	if peer != nil {
		return peer.String()
	}
	return "unknown"
}

type registrationRequest struct {
	ClientID    string `json:"client_id"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}
type registrationRow struct{ id, client, username, subject, keyHash, metadata, status string }

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req registrationRequest
	if !decode(w, r, &req) {
		return
	}
	c, ok := s.clientFor(w, r, req.ClientID, true)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 1 || len(key) > 128 || strings.TrimSpace(key) != key || !validUsername(req.Username) || !validEmail(req.Email) || utf8.RuneCountInString(req.DisplayName) > 80 {
		fail(w, 400, "invalid_request")
		return
	}
	req.Email = strings.ToLower(req.Email)
	metadata := s.mac("registration", c.ClientID, req.Username, req.Email, req.DisplayName)
	keyHash := s.mac("idempotency", key)
	// Serialize decisions for this client/key across server instances. In
	// particular a denied concurrent request must observe an already saved UNKNOWN
	// intent rather than falsely telling the caller it may start a new operation.
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "service_unavailable")
		return
	}
	defer database.Rollback(tx)
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, s.mac("registration-lock", c.ClientID, keyHash)); err != nil {
		fail(w, 503, "service_unavailable")
		return
	}
	commit := func() bool {
		if err := tx.Commit(r.Context()); err != nil {
			fail(w, 503, "service_unavailable")
			return false
		}
		return true
	}
	row, exists, err := existingRegistration(r.Context(), tx, c.ClientID, keyHash)
	if err != nil {
		fail(w, 503, "service_unavailable")
		return
	}
	if exists {
		// Reconciliation can use the pool only after this transaction releases it.
		if !commit() {
			return
		}
		if !equal(row.metadata, metadata) {
			fail(w, 409, "idempotency_conflict")
			return
		}
		s.registrationResult(w, r, row)
		return
	}
	retryAt, err := s.limitInTx(r.Context(), tx, r, c, req.Username, "register")
	if err != nil {
		fail(w, 503, "service_unavailable")
		return
	}
	if !retryAt.IsZero() {
		if !commit() {
			return
		}
		seconds := max(1, int((time.Until(retryAt)+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		jsonResponse(w, 429, map[string]any{"error": "rate_limited", "retry_after": seconds, "registration_not_started": true})
		return
	}
	org, err := s.organization(r.Context())
	if err != nil {
		if !commit() {
			return
		}
		fail(w, 503, "registration_policy_unavailable")
		return
	}
	if !validPassword(req.Password, req.Username, req.Email, org.PasswordOptions) {
		if !commit() {
			return
		}
		fail(w, 400, "invalid_password")
		return
	}
	if kind, err := s.applicationPolicy(r.Context(), c, true); err != nil {
		if !commit() {
			return
		}
		fail(w, 503, "registration_policy_unavailable")
		return
	} else if kind != "" {
		if !commit() {
			return
		}
		interaction(w, kind)
		return
	}
	row = registrationRow{id: randomUUID(), client: c.ClientID, username: req.Username, subject: randomUUID(), keyHash: keyHash, metadata: metadata, status: "UNKNOWN"}
	tag, err := tx.Exec(r.Context(), `INSERT INTO identity_registrations(id,client_id,username,subject_id,idempotency_hash,metadata_hash,status) VALUES($1,$2,$3,$4,$5,$6,'UNKNOWN') ON CONFLICT DO NOTHING`, row.id, row.client, row.username, row.subject, row.keyHash, row.metadata)
	if err != nil {
		fail(w, 503, "service_unavailable")
		return
	}
	if tag.RowsAffected() != 1 {
		prior, yes, e := existingRegistration(r.Context(), tx, c.ClientID, keyHash)
		if !commit() {
			return
		}
		if e == nil && yes && equal(prior.metadata, metadata) {
			s.registrationResult(w, r, prior)
			return
		}
		fail(w, 409, "registration_conflict")
		return
	}
	if !commit() {
		return
	}
	body := map[string]any{"owner": "unihub", "name": req.Username, "id": row.subject, "type": "normal-user", "password": req.Password, "passwordType": "plain", "displayName": req.DisplayName, "email": req.Email, "emailVerified": false, "isAdmin": false, "isForbidden": false, "isDeleted": false, "signupApplication": c.Application, "registerType": "UniHub API", "registerSource": "unihub/" + c.ProductID, "properties": map[string]string{"registration_id": row.id}}
	var env envelope
	err = s.providerJSON(r.Context(), "POST", "/api/add-user", body, true, &env)
	// A rejected or lost creation response may still follow a committed insert.
	// Never infer absence or issue a second create. Reconciliation only reads.
	if err == nil && env.Status == "ok" && string(env.Data) == `"Affected"` {
		row.status = "CREATED"
		updateCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if _, e := s.pool.Exec(updateCtx, `UPDATE identity_registrations SET status='CREATED',updated_at=now() WHERE id=$1`, row.id); e != nil {
			fail(w, 503, "service_unavailable")
			return
		}
		jsonResponse(w, 201, map[string]any{"registration_id": row.id, "status": "CREATED", "login_required": true})
		return
	}
	jsonResponse(w, 202, map[string]any{"registration_id": row.id, "status": "UNKNOWN", "retry_after": 2})
}
func existingRegistration(ctx context.Context, tx pgx.Tx, client, key string) (registrationRow, bool, error) {
	var row registrationRow
	e := tx.QueryRow(ctx, `SELECT id,client_id,username,subject_id,idempotency_hash,metadata_hash,status FROM identity_registrations WHERE client_id=$1 AND idempotency_hash=$2`, client, key).Scan(&row.id, &row.client, &row.username, &row.subject, &row.keyHash, &row.metadata, &row.status)
	if e == pgx.ErrNoRows {
		return row, false, nil
	}
	return row, e == nil, e
}
func (s *Server) registrationResult(w http.ResponseWriter, r *http.Request, row registrationRow) {
	if row.status == "UNKNOWN" {
		var env envelope
		if s.providerJSON(r.Context(), "GET", "/api/get-user?id="+url.QueryEscape("unihub/"+row.username), nil, true, &env) == nil && env.Status == "ok" && string(env.Data) != "null" {
			var user struct {
				Owner, Name, ID, Type           string
				IsAdmin, IsForbidden, IsDeleted bool
				Properties                      map[string]string
			}
			if json.Unmarshal(env.Data, &user) == nil {
				if user.Owner == "unihub" && user.Name == row.username && user.ID == row.subject && user.Type == "normal-user" && !user.IsAdmin && user.Properties["registration_id"] == row.id {
					row.status = "CREATED"
				} else {
					row.status = "CONFLICT"
				}
				_, _ = s.pool.Exec(r.Context(), `UPDATE identity_registrations SET status=$2,updated_at=now() WHERE id=$1`, row.id, row.status)
			}
		}
	}
	if row.status == "CONFLICT" {
		fail(w, 409, "registration_conflict")
		return
	}
	status := 200
	res := map[string]any{"registration_id": row.id, "status": row.status, "login_required": row.status == "CREATED"}
	if row.status == "UNKNOWN" {
		status = 202
		res["retry_after"] = 2
	}
	jsonResponse(w, status, res)
}

type authorizeRequest struct {
	ClientID        string `json:"client_id"`
	RedirectURI     string `json:"redirect_uri"`
	Scope           string `json:"scope"`
	State           string `json:"state"`
	Nonce           string `json:"nonce"`
	ChallengeMethod string `json:"code_challenge_method"`
	Challenge       string `json:"code_challenge"`
	Username        string `json:"username"`
	Password        string `json:"password"`
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	var req authorizeRequest
	if !decode(w, r, &req) {
		return
	}
	c, ok := s.clientFor(w, r, req.ClientID, false)
	if !ok {
		return
	}
	if !slices.Contains(c.RedirectURIs, req.RedirectURI) || req.Scope != scopes || req.ChallengeMethod != "S256" || !pkce.MatchString(req.Challenge) || len(req.State) < 32 || len(req.State) > 256 || len(req.Nonce) < 32 || len(req.Nonce) > 256 || !validUsername(req.Username) || len(req.Password) == 0 || utf8.RuneCountInString(req.Password) > 128 {
		fail(w, 400, "invalid_request")
		return
	}
	if !s.limit(r.Context(), r, c, req.Username, "authorize") {
		fail(w, 429, "rate_limited")
		return
	}
	if _, e := s.organization(r.Context()); e != nil {
		fail(w, 503, "identity_policy_unavailable")
		return
	}
	if kind, e := s.applicationPolicy(r.Context(), c, false); e != nil {
		fail(w, 503, "identity_policy_unavailable")
		return
	} else if kind != "" {
		interaction(w, kind)
		return
	}
	q := url.Values{"clientId": {c.ClientID}, "responseType": {"code"}, "redirectUri": {req.RedirectURI}, "type": {"code"}, "scope": {scopes}, "state": {req.State}, "nonce": {req.Nonce}, "code_challenge_method": {"S256"}, "code_challenge": {req.Challenge}}
	body := map[string]any{"type": "code", "signinMethod": "Password", "organization": "unihub", "application": c.Application, "username": req.Username, "password": req.Password, "autoSignin": false}
	var env envelope
	if e := s.providerJSON(r.Context(), "POST", "/api/login?"+q.Encode(), body, false, &env); e != nil {
		fail(w, 503, "identity_unavailable")
		return
	}
	if env.Status != "ok" {
		fail(w, 401, "invalid_credentials")
		return
	}
	var code string
	if json.Unmarshal(env.Data, &code) != nil {
		interaction(w, "consent")
		return
	}
	kinds := map[string]string{"NextMfa": "mfa", "RequiredMfa": "mfa_enrollment", "RequiredUpdatePassword": "password_change", "SelectPlan": "provider_policy", "BuyPlanResult": "provider_policy"}
	if kind := kinds[code]; kind != "" {
		interaction(w, kind)
		return
	}
	if !emptyProviderField(env.Data2) || !emptyProviderField(env.Data3) || !authorizationCode.MatchString(code) {
		interaction(w, "provider_policy")
		return
	}
	id := randomID()
	_, e := s.pool.Exec(r.Context(), `INSERT INTO identity_authorizations(id,client_id,redirect_uri,code_hash,pkce_challenge,nonce_hash,state_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,now()+interval '5 minutes')`, id, c.ClientID, req.RedirectURI, s.mac("code", code), req.Challenge, s.mac("nonce", req.Nonce), s.mac("state", req.State))
	if e != nil {
		fail(w, 400, "authorization_conflict")
		return
	}
	jsonResponse(w, 200, map[string]any{"authorization_id": id, "code": code, "state": req.State, "expires_in": 300})
}

type tokenRequest struct {
	Grant           string `json:"grant_type"`
	AuthorizationID string `json:"authorization_id"`
	ClientID        string `json:"client_id"`
	RedirectURI     string `json:"redirect_uri"`
	Code            string `json:"code"`
	Verifier        string `json:"code_verifier"`
}
type tokenSet struct {
	Access  string `json:"access_token"`
	ID      string `json:"id_token"`
	Type    string `json:"token_type"`
	Scope   string `json:"scope"`
	Expires int64  `json:"expires_in"`
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decode(w, r, &req) {
		return
	}
	c, ok := s.clientFor(w, r, req.ClientID, false)
	if !ok {
		return
	}
	if req.Grant != "authorization_code" || len(req.AuthorizationID) != 43 || req.Code == "" || len(req.Code) > 512 || !slices.Contains(c.RedirectURIs, req.RedirectURI) || !verifierPattern.MatchString(req.Verifier) {
		fail(w, 400, "invalid_request")
		return
	}
	if !s.limit(r.Context(), r, c, "", "token") {
		fail(w, 429, "rate_limited")
		return
	}
	challenge := sha256.Sum256([]byte(req.Verifier))
	var nonceHash string
	// Binding and single use are enforced atomically before any provider request.
	e := s.pool.QueryRow(r.Context(), `UPDATE identity_authorizations SET consumed_at=now() WHERE id=$1 AND client_id=$2 AND redirect_uri=$3 AND code_hash=$4 AND pkce_challenge=$5 AND expires_at>now() AND consumed_at IS NULL RETURNING nonce_hash`, req.AuthorizationID, c.ClientID, req.RedirectURI, s.mac("code", req.Code), base64.RawURLEncoding.EncodeToString(challenge[:])).Scan(&nonceHash)
	if e == pgx.ErrNoRows {
		fail(w, 400, "invalid_grant")
		return
	}
	if e != nil {
		fail(w, 503, "service_unavailable")
		return
	}
	var tokens tokenSet
	if s.providerJSON(r.Context(), "POST", "/api/login/oauth/access_token", map[string]string{"grant_type": "authorization_code", "client_id": c.ClientID, "code": req.Code, "code_verifier": req.Verifier}, false, &tokens) != nil {
		fail(w, 503, "authorization_result_unknown")
		return
	}
	if tokens.Access == "" || tokens.ID == "" || tokens.Type != "Bearer" || tokens.Expires <= 0 || tokens.Scope != scopes || s.verifyTokens(r.Context(), c.ClientID, tokens, nonceHash) != nil {
		fail(w, 502, "invalid_identity_response")
		return
	}
	jsonResponse(w, 200, tokens)
}
func (s *Server) verifyTokens(ctx context.Context, client string, tokens tokenSet, nonceHash string) error {
	v := s.verifiers[client]
	access, e := v.Verify(ctx, tokens.Access)
	if e != nil {
		return errors.New("invalid identity")
	}
	id, e := v.Verify(ctx, tokens.ID)
	if e != nil {
		return errors.New("invalid identity")
	}
	var a struct {
		Type   string   `json:"tokenType"`
		Scope  string   `json:"scope"`
		Azp    string   `json:"azp"`
		Expiry *float64 `json:"exp"`
	}
	var i struct {
		Type   string   `json:"tokenType"`
		Azp    string   `json:"azp"`
		Expiry *float64 `json:"exp"`
	}
	if access.Claims(&a) != nil || id.Claims(&i) != nil || a.Type != "access-token" || i.Type != "id-token" || a.Scope != scopes || a.Expiry == nil || *a.Expiry <= 0 || i.Expiry == nil || *i.Expiry <= 0 || len(access.Audience) != 1 || access.Audience[0] != client || len(id.Audience) != 1 || id.Audience[0] != client || a.Azp != "" && a.Azp != client || i.Azp != "" && i.Azp != client || access.Subject == "" || access.Subject != id.Subject || access.Issuer != id.Issuer || !equal(s.mac("nonce", id.Nonce), nonceHash) {
		return errors.New("invalid identity")
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(tokens.Access) != nil {
		return errors.New("invalid identity")
	}
	return nil
}

type envelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
	Data2  json.RawMessage `json:"data2"`
	Data3  json.RawMessage `json:"data3"`
}

func (s *Server) providerJSON(ctx context.Context, method, path string, body any, management bool, out any) error {
	var raw []byte
	var e error
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil {
			return errors.New("identity request invalid")
		}
		defer clear(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(s.opts.Issuer, "/")+path, bytes.NewReader(raw))
	if e != nil {
		return errors.New("identity request invalid")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if management {
		req.SetBasicAuth(s.opts.ManagementClientID, s.opts.ManagementClientSecret)
	}
	res, e := s.client.Do(req)
	if e != nil {
		return errors.New("identity provider unavailable")
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	defer clear(b)
	if e != nil || len(b) > 1<<20 || res.StatusCode != 200 {
		return errors.New("identity provider unavailable")
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("identity provider invalid response")
	}
	return nil
}

func emptyProviderField(raw json.RawMessage) bool { return len(raw) == 0 || string(raw) == "null" }
