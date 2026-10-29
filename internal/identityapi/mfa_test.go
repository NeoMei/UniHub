package identityapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func bytesKey() []byte { return bytes.Repeat([]byte{7}, 32) }

// currentMfaSecret models the provider's stored TOTP secret across requests.
var currentMfaSecret atomic.Value

func TestMfaLifecycleEnablesConfirmsAndDisablesWithPassword(t *testing.T) {
	p := testutil.Database(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	currentMfaSecret.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "msg": "", "data": data, "data2": nil, "data3": nil})
		}
		fail := func(msg string) { _ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "msg": msg}) }
		switch r.URL.Path {
		case "/api/get-user":
			secret, _ := currentMfaSecret.Load().(string)
			ok(map[string]any{"id": "sub", "name": "login", "totpSecret": secret, "displayName": "Login", "email": "a@example.test", "updatedTime": "known"})
		case "/api/mfa/setup/initiate":
			ok(map[string]any{"mfaType": "totp", "secret": "PENDING-SECRET", "url": "otpauth://totp/x?secret=PENDING", "recoveryCodes": []string{"one-time-recovery"}})
		case "/api/mfa/setup/verify":
			r.ParseForm()
			want := ""
			if r.Form.Get("secret") == "PENDING-SECRET" {
				want = "123456"
			} else if r.Form.Get("secret") == "ENABLED-SECRET" {
				want = "987654"
			} else {
				fail("totp secret missing")
				return
			}
			if r.Form.Get("passcode") != want {
				fail("totp passcode error")
				return
			}
			ok("OK")
		case "/api/mfa/setup/enable":
			r.ParseForm()
			if r.Form.Get("recoveryCodes") != "one-time-recovery" {
				fail("recovery codes is missing")
				return
			}
			currentMfaSecret.Store("ENABLED-SECRET")
			ok("OK")
		case "/api/delete-mfa":
			currentMfaSecret.Store("")
			ok([]any{})
		case "/api/check-user-password":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != "Current-Passphrase!" {
				fail("password or code is incorrect")
				return
			}
			ok(nil)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	u, err := accounts.New(p).EnsureIdentity(ctx, accounts.Identity{Issuer: upstream.URL, Subject: "sub", Name: ""})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{pool: p, opts: Options{Issuer: upstream.URL, ManagementClientID: "m", ManagementClientSecret: "s", SigningKey: bytesKey()}, client: upstream.Client()}
	status, err := s.MfaStatus(ctx, u.ID)
	if err != nil || status.Enabled {
		t.Fatalf("initial status %v %v", status, err)
	}
	setup, _, err := s.MfaEnroll(ctx, u.ID)
	if err != nil || setup.Secret != "PENDING-SECRET" || setup.RecoveryCodes[0] != "one-time-recovery" || !strings.HasPrefix(setup.URL, "otpauth://") {
		t.Fatalf("enroll %#v %v", setup, err)
	}
	if _, err = s.MfaConfirm(ctx, u.ID, setup.Secret, "000000", "one-time-recovery"); err != ErrMfaInvalidCode {
		t.Fatalf("wrong passcode must reject: %v", err)
	}
	if _, err = s.MfaConfirm(ctx, u.ID, setup.Secret, "123456", "one-time-recovery"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	var raw string
	if err = p.QueryRow(ctx, `SELECT row_to_json(r)::text FROM audit_events r`).Scan(&raw); err != nil || !strings.Contains(raw, "identity.mfa.enabled") || strings.Contains(raw, "PENDING-SECRET") {
		t.Fatalf("audit %s %v", raw, err)
	}
	if _, _, err = s.MfaEnroll(ctx, u.ID); err != ErrMfaState {
		t.Fatalf("second enroll must conflict: %v", err)
	}
	if _, err = s.MfaDisable(ctx, u.ID, "wrong", "987654"); err != ErrMfaInvalidPass {
		t.Fatalf("wrong password must reject: %v", err)
	}
	if _, err = s.MfaDisable(ctx, u.ID, "Current-Passphrase!", "000000"); err != ErrMfaInvalidCode {
		t.Fatalf("wrong disable code must reject: %v", err)
	}
	if _, err = s.MfaDisable(ctx, u.ID, "Current-Passphrase!", "987654"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if status, err = s.MfaStatus(ctx, u.ID); err != nil || status.Enabled {
		t.Fatalf("disabled status %v %v", status, err)
	}
}

func TestMfaDisableRequiresBothFactors(t *testing.T) {
	p := testutil.Database(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	var checks, deletes int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ok := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "msg": "", "data": data, "data2": nil, "data3": nil})
		}
		switch r.URL.Path {
		case "/api/get-user":
			ok(map[string]any{"id": "sub", "name": "login", "totpSecret": "ENABLED-SECRET", "displayName": "Login", "email": "a@example.test", "updatedTime": "known"})
		case "/api/check-user-password":
			atomic.AddInt32(&checks, 1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["owner"] != "unihub" || body["name"] != "login" || body["password"] != "Current-Passphrase!" {
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "msg": "password or code is incorrect"})
				return
			}
			ok(nil)
		case "/api/delete-mfa":
			atomic.AddInt32(&deletes, 1)
			ok([]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	u, err := accounts.New(p).EnsureIdentity(ctx, accounts.Identity{Issuer: upstream.URL, Subject: "sub", Name: ""})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{pool: p, opts: Options{Issuer: upstream.URL, ManagementClientID: "m", ManagementClientSecret: "s", SigningKey: bytesKey()}, client: upstream.Client()}
	if _, err = s.MfaDisable(ctx, u.ID, "wrong", "123456"); err != ErrMfaInvalidPass {
		t.Fatalf("wrong password: %v", err)
	}
	if atomic.LoadInt32(&deletes) != 0 {
		t.Fatal("provider delete reached without both factors")
	}
	if atomic.LoadInt32(&checks) != 1 {
		t.Fatal("password check must run exactly once per disable attempt")
	}
}

func TestAuthorizeMfaContinuesPendedLogin(t *testing.T) {
	s, i, p := setup(t)
	if s.providerSession == "" {
		t.Fatal("provider session cookie name undiscovered")
	}
	i.mode = "mfa"
	w := request(t, s, "/v1/identity/authorize/password", authorize(), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "mfa_pending") {
		t.Fatalf("pending %d %s", w.Code, w.Body)
	}
	var pending struct {
		Token string `json:"mfa_token"`
	}
	if json.Unmarshal(w.Body.Bytes(), &pending) != nil || len(pending.Token) != 43 {
		t.Fatalf("pending token %q", pending.Token)
	}
	var raw string
	if err := p.QueryRow(context.Background(), `SELECT row_to_json(r)::text FROM identity_mfa_pending r`).Scan(&raw); err != nil || strings.Contains(raw, "casdoor-session") {
		t.Fatalf("pending row leaks provider session: %s %v", raw, err)
	}
	var storedHash string
	if err := p.QueryRow(context.Background(), `SELECT id_hash FROM identity_mfa_pending`).Scan(&storedHash); err != nil {
		t.Fatalf("pending row missing: %v", err)
	}
	if storedHash != s.providerMfaToken(pending.Token) {
		t.Fatal("stored hash does not bind the issued token")
	}
	i.mode = "mfa verify"
	t.Logf("token=%q hash-prefix=%s", pending.Token, s.providerMfaToken(pending.Token)[:12])
	if len(pending.Token) != 43 {
		t.Fatalf("bad pending token before continue: %q", pending.Token)
	}
	w = httptest.NewRecorder()
	s.Handler(http.NotFoundHandler()).ServeHTTP(w, mfaRequest(pending.Token, "012345"))
	t.Logf("first mfa attempt status=%d body=%s", w.Code, w.Body.String())
	if w.Code != 401 {
		t.Fatalf("wrong code must stay pending: %d %s token=%q", w.Code, w.Body, pending.Token)
	}
	w = httptest.NewRecorder()
	s.Handler(http.NotFoundHandler()).ServeHTTP(w, mfaRequest(pending.Token, "654321"))
	t.Logf("second mfa attempt status=%d body=%s", w.Code, w.Body.String())
	if w.Code != 200 || !strings.Contains(w.Body.String(), "authorization_id") {
		t.Fatalf("mfa authorize %d %s token=%q", w.Code, w.Body, pending.Token)
	}
	w = request(t, s, "/v1/identity/token", tokenBody(t, w), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "access_token") {
		t.Fatalf("token %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	s.Handler(http.NotFoundHandler()).ServeHTTP(w, mfaRequest(pending.Token, "654321"))
	if w.Code != 400 {
		t.Fatalf("replay must fail: %d", w.Code)
	}
}

func mfaRequest(token, passcode string) *http.Request {
	b, _ := json.Marshal(map[string]any{"client_id": "native", "mfa_token": token, "passcode": passcode})
	r := httptest.NewRequest("POST", "/v1/identity/authorize/mfa", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return r
}
