package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The issuer signs actual JWTs and serves discovery/JWKS over HTTP so the tests
// exercise discovery, key retrieval, signature verification and claim policy.
type testIssuer struct {
	server      *httptest.Server
	key         *rsa.PrivateKey
	keyRequests atomic.Int32
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &testIssuer{key: key}
	i.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                i.server.URL,
				"authorization_endpoint":                i.server.URL + "/authorize",
				"token_endpoint":                        i.server.URL + "/token",
				"jwks_uri":                              i.server.URL + "/jwks",
				"response_types_supported":              []string{"code"},
				"subject_types_supported":               []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256", "RS512"},
			})
		case "/jwks":
			i.keyRequests.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
				"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(i.server.Close)
	return i
}

func (i *testIssuer) claims() map[string]any {
	return map[string]any{
		"iss": i.server.URL, "sub": "casdoor-user-123", "aud": []string{"unihub"},
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"tokenType": "access-token", "scope": "openid profile email",
		"name": "Alice", "email": "alice@example.com",
	}
}

func signJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any, alg string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	content := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	if alg == "none" {
		return content + "."
	}
	digest := sha256.Sum256([]byte(content))
	hash, sum := crypto.SHA256, digest[:]
	if alg == "RS512" {
		digest512 := sha512.Sum512([]byte(content))
		hash, sum = crypto.SHA512, digest512[:]
	}
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, hash, sum)
	if err != nil {
		t.Fatal(err)
	}
	return content + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestOIDCVerifiesCasdoorAccessToken(t *testing.T) {
	i := newTestIssuer(t)
	verifier, err := NewOIDC(context.Background(), i.server.URL, "unihub")
	if err != nil {
		t.Fatal(err)
	}
	var contract Verifier = verifier
	got, err := contract.Verify(context.Background(), signJWT(t, i.key, i.claims(), "RS256"))
	if err != nil {
		t.Fatalf("valid access token rejected: %v", err)
	}
	want := Claims{Issuer: i.server.URL, Subject: "casdoor-user-123", Name: "Alice", Email: "alice@example.com"}
	if got != want {
		t.Fatalf("identity = %#v, want %#v", got, want)
	}
	if i.keyRequests.Load() == 0 {
		t.Fatal("token accepted without fetching signing keys")
	}
}

func TestOIDCRejectsInvalidTokens(t *testing.T) {
	i := newTestIssuer(t)
	verifier, err := NewOIDC(context.Background(), i.server.URL, "unihub")
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(map[string]any)
		key    *rsa.PrivateKey
		alg    string
	}{
		{name: "wrong issuer", change: func(c map[string]any) { c["iss"] = "https://other.example.com" }},
		{name: "wrong audience", change: func(c map[string]any) { c["aud"] = []string{"other-app"} }},
		{name: "missing audience", change: func(c map[string]any) { delete(c, "aud") }},
		{name: "wrong signature", key: otherKey},
		{name: "expired", change: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }},
		{name: "missing expiry", change: func(c map[string]any) { delete(c, "exp") }},
		{name: "zero expiry", change: func(c map[string]any) { c["exp"] = 0 }},
		{name: "null expiry", change: func(c map[string]any) { c["exp"] = nil }},
		{name: "string expiry", change: func(c map[string]any) { c["exp"] = "4102444800" }},
		{name: "ID token", change: func(c map[string]any) { c["tokenType"] = "id-token" }},
		{name: "refresh token", change: func(c map[string]any) { c["tokenType"] = "refresh-token" }},
		{name: "missing purpose", change: func(c map[string]any) { delete(c, "tokenType") }},
		{name: "wrong purpose field", change: func(c map[string]any) { delete(c, "tokenType"); c["type"] = "access-token" }},
		{name: "missing subject", change: func(c map[string]any) { delete(c, "sub") }},
		{name: "empty subject", change: func(c map[string]any) { c["sub"] = "" }},
		{name: "blank subject", change: func(c map[string]any) { c["sub"] = " \t" }},
		{name: "missing scope", change: func(c map[string]any) { delete(c, "scope") }},
		{name: "wrong scope", change: func(c map[string]any) { c["scope"] = "profile email" }},
		{name: "scope substring", change: func(c map[string]any) { c["scope"] = "not-openid openid-other" }},
		{name: "scope wrong type", change: func(c map[string]any) { c["scope"] = []string{"openid"} }},
		{name: "future not before", change: func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() }},
		{name: "unsigned", alg: "none"},
		{name: "unsupported algorithm", alg: "RS512"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := i.claims()
			if tt.change != nil {
				tt.change(claims)
			}
			key := tt.key
			if key == nil {
				key = i.key
			}
			alg := tt.alg
			if alg == "" {
				alg = "RS256"
			}
			got, err := verifier.Verify(context.Background(), signJWT(t, key, claims, alg))
			if err == nil {
				t.Fatalf("invalid token accepted: %#v", got)
			}
			if got != (Claims{}) {
				t.Fatalf("rejected token exposed identity: %#v", got)
			}
		})
	}
	for _, raw := range []string{"", "not-a-token", "a.b.c"} {
		if got, err := verifier.Verify(context.Background(), raw); err == nil || got != (Claims{}) {
			t.Errorf("malformed token %q returned (%#v, %v)", raw, got, err)
		}
	}
}

func TestNewOIDCRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct{ name, issuer, audience string }{
		{"empty issuer", "", "unihub"},
		{"relative issuer", "/casdoor", "unihub"},
		{"remote plain HTTP", "http://example.com", "unihub"},
		{"private plain HTTP", "http://192.168.1.5:8000", "unihub"},
		{"fake localhost", "http://localhost.example.com", "unihub"},
		{"unsupported scheme", "ftp://localhost", "unihub"},
		{"missing host", "https:///casdoor", "unihub"},
		{"userinfo", "https://user:secret@example.com", "unihub"},
		{"query", "https://example.com?tenant=one", "unihub"},
		{"fragment", "https://example.com#tenant", "unihub"},
		{"empty audience", "http://127.0.0.1:1", ""},
		{"blank audience", "http://127.0.0.1:1", " \t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewOIDC(context.Background(), tt.issuer, tt.audience)
			if err == nil || got != nil {
				t.Fatalf("invalid config accepted: (%v, %v)", got, err)
			}
		})
	}
}

func TestEndpointTransportPolicy(t *testing.T) {
	tests := []struct {
		endpoint string
		valid    bool
	}{
		{"https://id.example.com", true},
		{"https://id.example.com/tenant", true},
		{"http://localhost:8000", true},
		{"http://127.0.0.1:8000", true},
		{"http://[::1]:8000", true},
		{"http://example.com", false},
		{"http://192.168.1.5:8000", false},
		{"http://localhost.example.com", false},
		{"https://id.example.com?", false},
		{"https://id.example.com#", false},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			err := validateEndpoint(tt.endpoint)
			if (err == nil) != tt.valid {
				t.Fatalf("endpoint validation = %v, want accepted=%v", err, tt.valid)
			}
		})
	}
}

func TestNewOIDCRejectsInvalidDiscovery(t *testing.T) {
	for _, mode := range []string{"wrong issuer", "invalid JSON", "server failure", "missing JWKS", "insecure remote JWKS"} {
		t.Run(mode, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "server failure" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				if mode == "invalid JSON" {
					_, _ = w.Write([]byte("invalid"))
					return
				}
				document := map[string]any{"issuer": server.URL, "jwks_uri": server.URL + "/jwks"}
				if mode == "wrong issuer" {
					document["issuer"] = "https://other.example.com"
				}
				if mode == "missing JWKS" {
					delete(document, "jwks_uri")
				}
				if mode == "insecure remote JWKS" {
					document["jwks_uri"] = "http://example.com/jwks"
				}
				_ = json.NewEncoder(w).Encode(document)
			}))
			defer server.Close()
			if got, err := NewOIDC(context.Background(), server.URL, "unihub"); err == nil || got != nil {
				t.Fatalf("invalid discovery accepted: (%v, %v)", got, err)
			}
		})
	}
}

func TestNewOIDCRespectsCanceledContext(t *testing.T) {
	i := newTestIssuer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := NewOIDC(ctx, i.server.URL, "unihub"); err == nil || got != nil {
		t.Fatalf("canceled discovery returned (%v, %v)", got, err)
	}
}

func TestOIDCRejectsUnavailableJWKS(t *testing.T) {
	i := newTestIssuer(t)
	verifier, err := NewOIDC(context.Background(), i.server.URL, "unihub")
	if err != nil {
		t.Fatal(err)
	}
	i.server.Close()
	if got, err := verifier.Verify(context.Background(), signJWT(t, i.key, i.claims(), "RS256")); err == nil || got != (Claims{}) {
		t.Fatalf("token accepted without available signing keys: (%#v, %v)", got, err)
	}
}

func TestOIDCRespectsCanceledVerificationContext(t *testing.T) {
	i := newTestIssuer(t)
	verifier, err := NewOIDC(context.Background(), i.server.URL, "unihub")
	if err != nil {
		t.Fatal(err)
	}
	raw := signJWT(t, i.key, i.claims(), "RS256")
	// Prime the key cache: cancellation must still fail without a key fetch.
	if _, err := verifier.Verify(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := verifier.Verify(ctx, raw); err == nil || got != (Claims{}) {
		t.Fatalf("canceled verification returned (%#v, %v)", got, err)
	}
}
