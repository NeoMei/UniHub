package identityapi

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/testutil"
)

type testIssuer struct {
	server                                    *httptest.Server
	key                                       *rsa.PrivateKey
	mu                                        sync.Mutex
	mode, nonce, challenge                    string
	creates, logins, exchanges                int
	user                                      map[string]any
	orgPolicy                                 []string
	captcha, requiredEmail                    bool
	organizationEntered, organizationContinue chan struct{}
	requests                                  int
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &testIssuer{key: key, orgPolicy: []string{"AtLeast8", "Aa123"}}
	i.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i.mu.Lock()
		defer i.mu.Unlock()
		i.requests++
		w.Header().Set("Content-Type", "application/json")
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "msg": "", "data": data, "data2": nil, "data3": nil})
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": i.server.URL, "token_endpoint": i.server.URL + "/api/login/oauth/access_token", "jwks_uri": i.server.URL + "/.well-known/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/.well-known/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/api/get-organization":
			if i.organizationEntered != nil {
				close(i.organizationEntered)
				<-i.organizationContinue
				i.organizationEntered = nil
			}
			u, p, yes := r.BasicAuth()
			if !yes || u != "management" || p != "private-management-secret" || r.URL.Query().Get("id") != "admin/unihub" {
				http.Error(w, "bad management auth", 401)
				return
			}
			if i.mode == "missing org policy" {
				ok(map[string]any{"name": "unihub", "passwordType": "bcrypt"})
				return
			}
			ok(map[string]any{"name": "unihub", "passwordType": "bcrypt", "passwordObfuscatorType": "", "passwordOptions": i.orgPolicy, "masterPassword": "", "masterVerificationCode": "", "defaultPassword": ""})
		case "/api/get-app-login":
			if r.URL.Query().Get("clientId") == "management" {
				org := "unihub"
				if i.mode == "global management" {
					org = "built-in"
				}
				ok(map[string]any{"name": "management-app", "organization": org, "clientId": "management", "isShared": false})
				return
			}
			if r.Header.Get("Authorization") != "" {
				t.Error("management secret leaked to public policy request")
			}
			items := []any{map[string]any{"name": "Username", "visible": true, "required": true}, map[string]any{"name": "Password", "visible": true, "required": true}, map[string]any{"name": "Display name", "visible": true}, map[string]any{"name": "Email", "visible": true, "rule": "No verification", "required": false}}
			if i.requiredEmail {
				items[3] = map[string]any{"name": "Email", "visible": true, "required": true, "rule": "Normal"}
			}
			providers := []any{}
			if i.captcha {
				providers = append(providers, map[string]any{"provider": map[string]any{"category": "Captcha"}, "rule": "Always"})
			}
			ok(map[string]any{"name": "native-app", "organization": "unihub", "clientId": "native", "isShared": false, "enableSignUp": true, "disableSelfSignup": false, "defaultGroup": "", "defaultTag": "", "signupItems": items, "signinMethods": []any{map[string]any{"name": "Password", "rule": "All"}}, "providers": providers})
		case "/api/add-user":
			u, p, yes := r.BasicAuth()
			if !yes || u != "management" || p != "private-management-secret" || r.URL.RawQuery != "" {
				http.Error(w, "bad management auth", 401)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["owner"] != "unihub" || body["type"] != "normal-user" || body["isAdmin"] != false || body["emailVerified"] != false || body["passwordType"] != "plain" || body["password"] != "CorrectHorse9Battery!" {
				t.Error("unsafe creation DTO")
			}
			i.creates++
			if i.mode == "create unaffected" {
				ok("Unaffected")
				return
			}
			i.user = body
			if i.mode == "create unknown" {
				w.WriteHeader(503)
				return
			}
			ok("Affected")
		case "/api/get-user":
			u, p, yes := r.BasicAuth()
			if !yes || u != "management" || p != "private-management-secret" || r.URL.Query().Get("id") != "unihub/alice" {
				http.Error(w, "bad lookup", 401)
				return
			}
			ok(i.user)
		case "/api/login":
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("ambient credentials leaked to login")
			}
			q := r.URL.Query()
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if q.Get("clientId") != "native" || q.Get("responseType") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid profile email" || body["type"] != "code" || body["signinMethod"] != "Password" || body["organization"] != "unihub" || body["application"] != "native-app" {
				t.Error("incorrect password-to-code protocol")
			}
			i.logins++
			i.nonce = q.Get("nonce")
			i.challenge = q.Get("code_challenge")
			switch i.mode {
			case "unexpected hybrid":
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "data2": "unexpected-token", "data3": nil})
				return
			case "session login":
				ok("unihub/alice")
				return
			case "mfa":
				ok("NextMfa")
				return
			case "enrollment":
				ok("RequiredMfa")
				return
			case "password change":
				ok("RequiredUpdatePassword")
				return
			case "consent":
				ok(map[string]bool{"required": true})
				return
			case "bad credentials":
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "msg": "Alice does not exist secret details"})
				return
			}
			ok("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		case "/api/login/oauth/access_token":
			if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
				t.Error("secret or query used in token exchange")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			h := sha256.Sum256([]byte(body["code_verifier"]))
			if body["grant_type"] != "authorization_code" || body["client_id"] != "native" || body["code"] != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" || base64.RawURLEncoding.EncodeToString(h[:]) != i.challenge || body["client_secret"] != "" || body["password"] != "" {
				t.Error("unsafe token exchange")
			}
			i.exchanges++
			nonce := i.nonce
			if i.mode == "bad nonce" {
				nonce = "wrong"
			}
			access := i.jwt(t, map[string]any{"tokenType": "access-token", "scope": "openid profile email"})
			id := i.jwt(t, map[string]any{"tokenType": "id-token", "nonce": nonce})
			if i.mode == "wrong access purpose" {
				access = i.jwt(t, map[string]any{"tokenType": "id-token", "scope": "openid profile email"})
			}
			if i.mode == "wrong subject" {
				id = i.jwt(t, map[string]any{"tokenType": "id-token", "nonce": nonce, "sub": "other"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "id_token": id, "refresh_token": "private-refresh", "token_type": "Bearer", "expires_in": 600, "scope": "openid profile email"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(i.server.Close)
	return i
}
func (i *testIssuer) jwt(t *testing.T, extra map[string]any) string {
	t.Helper()
	claims := map[string]any{"iss": i.server.URL, "sub": "stable-subject", "aud": []string{"native"}, "azp": "native", "exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix()}
	for k, v := range extra {
		claims[k] = v
	}
	a, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
	b, _ := json.Marshal(claims)
	raw := base64.RawURLEncoding.EncodeToString(a) + "." + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	sig, e := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, h[:])
	if e != nil {
		t.Fatal(e)
	}
	return raw + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func options(i *testIssuer) Options {
	return Options{Issuer: i.server.URL, ManagementClientID: "management", ManagementClientSecret: "private-management-secret", SigningKey: bytes.Repeat([]byte{42}, 32), AuditRedactionVerified: true, Clients: map[string]products.Client{"native": {ProductID: "demo", ClientID: "native", Application: "native-app", Organization: "unihub", RedirectURIs: []string{"http://localhost:9000/callback"}, Origins: []string{"http://localhost:9000"}, PasswordLogin: true, Registration: true}}}
}
func setup(t *testing.T) (*Server, *testIssuer, *pgxpool.Pool) {
	t.Helper()
	p := testutil.Database(t)
	if e := database.Migrate(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	i := newTestIssuer(t)
	s, e := New(context.Background(), p, options(i))
	if e != nil {
		t.Fatal(e)
	}
	return s, i, p
}
func request(t *testing.T, s *Server, path string, body any, key string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("Cookie", "browser=must-not-pass")
	w := httptest.NewRecorder()
	s.Handler(http.NotFoundHandler()).ServeHTTP(w, r)
	return w
}
func registration() map[string]any {
	return map[string]any{"client_id": "native", "username": "alice", "password": "CorrectHorse9Battery!", "display_name": "Alice", "email": "alice@example.com"}
}

const verifier = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

func authorize() map[string]any {
	h := sha256.Sum256([]byte(verifier))
	return map[string]any{"client_id": "native", "redirect_uri": "http://localhost:9000/callback", "scope": "openid profile email", "state": strings.Repeat("s", 32), "nonce": strings.Repeat("n", 32), "code_challenge_method": "S256", "code_challenge": base64.RawURLEncoding.EncodeToString(h[:]), "username": "alice", "password": "CorrectHorse9Battery!"}
}
func tokenBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var b map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &b); e != nil {
		t.Fatal(e)
	}
	return map[string]any{"client_id": "native", "redirect_uri": "http://localhost:9000/callback", "grant_type": "authorization_code", "authorization_id": b["authorization_id"], "code": b["code"], "code_verifier": verifier}
}
func TestDisabledCapabilitiesNeedNoProviderOrSigningKey(t *testing.T) {
	s, e := New(context.Background(), nil, Options{Clients: map[string]products.Client{"native": {ClientID: "native"}}})
	if e != nil {
		t.Fatal(e)
	}
	w := request(t, s, "/v1/identity/registrations", registration(), "one")
	if w.Code != 403 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}
func TestEnabledRequiresAuditGate(t *testing.T) {
	i := newTestIssuer(t)
	o := options(i)
	o.AuditRedactionVerified = false
	if _, e := New(context.Background(), nil, o); e == nil {
		t.Fatal("enabled without audited deployment")
	}
}
func TestRegistrationCreateOnlyAndPasswordNeverPersisted(t *testing.T) {
	s, i, p := setup(t)
	w := request(t, s, "/v1/identity/registrations", registration(), "one")
	if w.Code != 201 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	b := registration()
	b["password"] = "DifferentCorrectPassword9!"
	again := request(t, s, "/v1/identity/registrations", b, "one")
	if again.Code != 200 || i.creates != 1 {
		t.Fatalf("replay=%d creates=%d", again.Code, i.creates)
	}
	other := request(t, s, "/v1/identity/registrations", registration(), "two")
	if other.Code != 409 || i.creates != 1 {
		t.Fatalf("different key=%d creates=%d", other.Code, i.creates)
	}
	var raw string
	if e := p.QueryRow(context.Background(), `SELECT row_to_json(r)::text FROM identity_registrations r`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(raw, "CorrectHorse") || strings.Contains(raw, "DifferentCorrect") || strings.Contains(raw, "password") {
		t.Fatal("password material persisted")
	}
}
func TestRegistrationUnknownReconcilesWithoutSecondCreate(t *testing.T) {
	s, i, _ := setup(t)
	i.mode = "create unknown"
	w := request(t, s, "/v1/identity/registrations", registration(), "one")
	if w.Code != 202 {
		t.Fatalf("first=%d body=%s", w.Code, w.Body)
	}
	w = request(t, s, "/v1/identity/registrations", registration(), "one")
	if w.Code != 200 || i.creates != 1 {
		t.Fatalf("reconcile=%d creates=%d body=%s", w.Code, i.creates, w.Body)
	}
}
func TestRegistrationRejectsPrivilegeInputAndUnknownPolicy(t *testing.T) {
	s, i, _ := setup(t)
	b := registration()
	b["isAdmin"] = true
	if w := request(t, s, "/v1/identity/registrations", b, "one"); w.Code != 400 || i.creates != 0 {
		t.Fatalf("privilege status=%d", w.Code)
	}
	i.requiredEmail = true
	if w := request(t, s, "/v1/identity/registrations", registration(), "two"); w.Code != 409 || i.creates != 0 {
		t.Fatalf("verification status=%d", w.Code)
	}
}
func TestPublicPKCEUsesCasdoorTokensAndRejectsReplay(t *testing.T) {
	s, i, p := setup(t)
	w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
	if w.Code != 200 {
		t.Fatalf("authorize=%d body=%s", w.Code, w.Body)
	}
	b := tokenBody(t, w)
	w = request(t, s, "/v1/identity/token", b, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "access_token") || strings.Contains(w.Body.String(), "private-refresh") {
		t.Fatalf("token=%d body=%s", w.Code, w.Body)
	}
	w = request(t, s, "/v1/identity/token", b, "")
	if w.Code != 400 || i.exchanges != 1 {
		t.Fatalf("replay=%d exchanges=%d", w.Code, i.exchanges)
	}
	var raw string
	if e := p.QueryRow(context.Background(), `SELECT row_to_json(t)::text FROM identity_authorizations t`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", strings.Repeat("n", 32), verifier, "access_token", "private-refresh"} {
		if strings.Contains(raw, v) {
			t.Fatalf("sensitive transaction persisted: %s", v)
		}
	}
}
func TestTokenBindingRejectsWrongURIAndVerifierBeforeExchange(t *testing.T) {
	for _, field := range []string{"redirect_uri", "code_verifier", "client_id", "code"} {
		t.Run(field, func(t *testing.T) {
			s, i, _ := setup(t)
			w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
			b := tokenBody(t, w)
			b[field] = "wrong"
			w = request(t, s, "/v1/identity/token", b, "")
			if w.Code != 400 && w.Code != 403 {
				t.Fatalf("status=%d", w.Code)
			}
			if i.exchanges != 0 {
				t.Fatal("bad binding reached provider")
			}
		})
	}
}
func TestInteractionsNeverProduceCodeOrExchange(t *testing.T) {
	for _, mode := range []string{"mfa", "enrollment", "password change", "consent"} {
		t.Run(mode, func(t *testing.T) {
			s, i, _ := setup(t)
			i.mode = mode
			w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
			if w.Code != 409 || !strings.Contains(w.Body.String(), "interaction_required") || strings.Contains(w.Body.String(), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") || i.exchanges != 0 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}
func TestTokenVerificationRejectsInvalidNoncePurposeAndSubject(t *testing.T) {
	for _, mode := range []string{"bad nonce", "wrong access purpose", "wrong subject"} {
		t.Run(mode, func(t *testing.T) {
			s, i, _ := setup(t)
			w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
			i.mode = mode
			w = request(t, s, "/v1/identity/token", tokenBody(t, w), "")
			if w.Code != 502 || strings.Contains(w.Body.String(), "access_token") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}
func TestRateLimitSurvivesServerRecreation(t *testing.T) {
	s, i, p := setup(t)
	i.mode = "bad credentials"
	for n := 0; n < 5; n++ {
		w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
		if w.Code != 401 {
			t.Fatalf("attempt %d: %d %s", n, w.Code, w.Body)
		}
	}
	again, e := New(context.Background(), p, options(i))
	if e != nil {
		t.Fatal(e)
	}
	w := request(t, again, "/v1/identity/authorize/password", authorize(), "")
	if w.Code != 429 || i.logins != 5 {
		t.Fatalf("limit=%d logins=%d", w.Code, i.logins)
	}
}
func TestOriginAndPolicyFailClosed(t *testing.T) {
	s, i, _ := setup(t)
	b, _ := json.Marshal(authorize())
	r := httptest.NewRequest("POST", "/v1/identity/authorize/password", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.Handler(http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 403 || i.logins != 0 {
		t.Fatalf("origin status=%d", w.Code)
	}
	i.captcha = true
	w = request(t, s, "/v1/identity/authorize/password", authorize(), "")
	if w.Code != 409 || i.logins != 0 {
		t.Fatalf("captcha=%d logins=%d body=%s", w.Code, i.logins, w.Body)
	}
}

// A management application in built-in would grant global administrator power;
// construction must reject it even though it can read the target organization.
func TestManagementCredentialCannotBelongToGlobalOrganization(t *testing.T) {
	p := testutil.Database(t)
	if e := database.Migrate(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	i := newTestIssuer(t)
	i.mode = "global management"
	if _, e := New(context.Background(), p, options(i)); e == nil {
		t.Fatal("accepted a global management credential")
	}
}

func TestUnaffectedCreateDoesNotClaimUserCreated(t *testing.T) {
	s, i, _ := setup(t)
	i.mode = "create unaffected"
	w := request(t, s, "/v1/identity/registrations", registration(), "one")
	if w.Code != 202 || strings.Contains(w.Body.String(), `"status":"CREATED"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}
func TestUnexpectedLoginSuccessIsNotAnAuthorizationCode(t *testing.T) {
	s, i, _ := setup(t)
	i.mode = "session login"
	w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
	if w.Code != 409 || strings.Contains(w.Body.String(), `"code"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestBcryptRegistrationRejectsOverlongUTF8PasswordBeforeCreation(t *testing.T) {
	s, i, _ := setup(t)
	b := registration()
	b["password"] = "LongPassword9!" + strings.Repeat("你", 25)
	w := request(t, s, "/v1/identity/registrations", b, "one")
	if w.Code != 400 || i.creates != 0 {
		t.Fatalf("status=%d creates=%d", w.Code, i.creates)
	}
}
func TestCodeOnlyFlowRejectsUnexpectedHybridTokenFields(t *testing.T) {
	s, i, _ := setup(t)
	i.mode = "unexpected hybrid"
	w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
	if w.Code != 409 || strings.Contains(w.Body.String(), `"code"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestConcurrentRegistrationAndTokenExchangeStaySingleUse(t *testing.T) {
	s, i, _ := setup(t)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- request(t, s, "/v1/identity/registrations", registration(), "same-concurrent-key").Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 201 && status != 200 && status != 202 {
			t.Fatalf("register concurrency status=%d", status)
		}
	}
	i.mu.Lock()
	creates := i.creates
	i.mu.Unlock()
	if creates != 1 {
		t.Fatalf("create count=%d", creates)
	}
	w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
	if w.Code != 200 {
		t.Fatalf("authorize status=%d", w.Code)
	}
	body := tokenBody(t, w)
	statuses = make(chan int, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); statuses <- request(t, s, "/v1/identity/token", body, "").Code }()
	}
	wg.Wait()
	close(statuses)
	good, bad := 0, 0
	for status := range statuses {
		if status == 200 {
			good++
		} else if status == 400 {
			bad++
		}
	}
	if good != 1 || bad != 1 {
		t.Fatalf("single-use successes=%d rejections=%d", good, bad)
	}
	i.mu.Lock()
	exchanges := i.exchanges
	i.mu.Unlock()
	if exchanges != 1 {
		t.Fatalf("exchanges=%d", exchanges)
	}
}

func TestMissingPolicyFieldsCannotBeAssumedSafe(t *testing.T) {
	p := testutil.Database(t)
	if e := database.Migrate(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	i := newTestIssuer(t)
	i.mode = "missing org policy"
	if _, e := New(context.Background(), p, options(i)); e == nil {
		t.Fatal("missing organization policy fields accepted")
	}
}

// Seed adjacent windows to keep integration tests stable at the hour boundary.
func exhaustRegistrationLimit(t *testing.T, s *Server, p *pgxpool.Pool, global bool, hits int) {
	t.Helper()
	dimension, client := "register-ip", "native"
	if global {
		dimension, client = "register-global-ip", ""
	}
	key := s.mac("limit", "register", dimension, client, "192.0.2.1")
	window := time.Now().UTC().Truncate(time.Hour)
	for _, at := range []time.Time{window, window.Add(time.Hour)} {
		if _, err := p.Exec(context.Background(), `INSERT INTO identity_rate_limits(key_hash,window_start,hits) VALUES($1,$2,$3) ON CONFLICT(key_hash,window_start) DO UPDATE SET hits=$3`, key, at, hits); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegistrationRateLimitReportsWindowWithoutStartingRegistration(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(strconv.FormatBool(global), func(t *testing.T) {
			s, i, p := setup(t)
			hits := 3
			if global {
				hits = 12
			}
			exhaustRegistrationLimit(t, s, p, global, hits)
			providerRequests := i.requests
			before := time.Now()
			w := request(t, s, registrationPath, registration(), "limited-key")
			after := time.Now()
			if w.Code != 429 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
			if err != nil || seconds < 1 || seconds > 3600 {
				t.Fatalf("invalid Retry-After=%q", w.Header().Get("Retry-After"))
			}
			// Retry-After must point at the actual hourly boundary, including ceiling.
			earliest := before.UTC().Truncate(time.Hour).Add(time.Hour)
			latest := after.UTC().Truncate(time.Hour).Add(time.Hour)
			deadline := after.Add(time.Duration(seconds) * time.Second)
			if deadline.Before(earliest) || deadline.After(latest.Add(2*time.Second)) {
				t.Fatalf("retry deadline=%s want hourly boundary %s..%s", deadline, earliest, latest)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != "rate_limited" || body["retry_after"] != float64(seconds) || body["registration_not_started"] != true || body["registration_id"] != nil {
				t.Fatalf("invalid denial body=%s", w.Body)
			}
			if i.requests != providerRequests {
				t.Fatalf("rate-limited registration contacted provider: requests before=%d after=%d", providerRequests, i.requests)
			}
			var rows int
			if err := p.QueryRow(context.Background(), `SELECT count(*) FROM identity_registrations`).Scan(&rows); err != nil || rows != 0 || i.creates != 0 {
				t.Fatalf("denial wrote registrations: rows=%d creates=%d err=%v", rows, i.creates, err)
			}
			var raw string
			if err := p.QueryRow(context.Background(), `SELECT coalesce(json_agg(r)::text,'[]') FROM identity_rate_limits r`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"192.0.2.1", "limited-key", "alice", "CorrectHorse9Battery!"} {
				if strings.Contains(raw, secret) {
					t.Fatal("denial persisted request material")
				}
			}
		})
	}
}

func TestRegistrationUnknownReplaysDespiteExhaustedRateLimit(t *testing.T) {
	s, i, p := setup(t)
	// Use one connection: the transaction must release it before reconciliation.
	cfg := p.Config()
	cfg.MaxConns = 1
	single, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(single.Close)
	s.pool = single
	i.mode = "create unknown"
	first := request(t, s, registrationPath, registration(), "unknown-key")
	if first.Code != 202 {
		t.Fatalf("first=%d %s", first.Code, first.Body)
	}
	exhaustRegistrationLimit(t, s, p, false, 3)
	w := request(t, s, registrationPath, registration(), "unknown-key")
	if w.Code != 200 || i.creates != 1 || strings.Contains(w.Body.String(), "registration_not_started") || w.Header().Get("Retry-After") != "" {
		t.Fatalf("replay=%d creates=%d body=%s", w.Code, i.creates, w.Body)
	}
}

func TestRegistrationRateStoreFailureIsUnavailable(t *testing.T) {
	s, i, p := setup(t)
	if _, err := p.Exec(context.Background(), `ALTER TABLE identity_rate_limits RENAME TO unavailable_rate_limits`); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, registrationPath, registration(), "unavailable-key")
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"error":"service_unavailable"`) || strings.Contains(w.Body.String(), "registration_not_started") || w.Header().Get("Retry-After") != "" || i.creates != 0 {
		t.Fatalf("status=%d creates=%d body=%s", w.Code, i.creates, w.Body)
	}
}

func TestConcurrentSameKeyNeverClaimsNotStartedAfterAcceptedRequest(t *testing.T) {
	s, i, p := setup(t)
	exhaustRegistrationLimit(t, s, p, false, 2)
	entered, proceed := make(chan struct{}), make(chan struct{})
	i.mu.Lock()
	i.organizationEntered, i.organizationContinue = entered, proceed
	i.mu.Unlock()
	first := make(chan *httptest.ResponseRecorder, 1)
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- request(t, s, registrationPath, registration(), "concurrent-limit-key") }()
	<-entered
	go func() { second <- request(t, s, registrationPath, registration(), "concurrent-limit-key") }()
	var early *httptest.ResponseRecorder
	select {
	case early = <-second:
	case <-time.After(100 * time.Millisecond):
	}
	close(proceed)
	firstResult := <-first
	if early == nil {
		early = <-second
	}
	if firstResult.Code != 201 {
		t.Fatalf("first=%d body=%s", firstResult.Code, firstResult.Body)
	}
	if early.Code != 200 && early.Code != 202 {
		t.Fatalf("same-key concurrent replay=%d body=%s", early.Code, early.Body)
	}
	if strings.Contains(early.Body.String(), "registration_not_started") || i.creates != 1 {
		t.Fatalf("incorrect not-started assertion or duplicate create: creates=%d body=%s", i.creates, early.Body)
	}
}
