package identityapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestSelfProfileKeepsLoginNameAndChangesDisplayNameAndAvatar(t *testing.T) {
	p := testutil.Database(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	display := "Original name"
	passwordChanged := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/get-user":
			if r.URL.Query().Get("userId") != "immutable-sub" || r.URL.Query().Get("owner") != "unihub" {
				t.Error("target is not the immutable authenticated subject")
			}
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]string{"owner": "unihub", "id": "immutable-sub", "name": "login-name", "displayName": display, "avatar": "https://external.invalid/tracking.png", "email": "a@example.test", "updatedTime": "known"}})
		case "/api/update-user":
			if r.URL.Query().Get("id") != "unihub/login-name" || r.URL.Query().Get("userId") != "" {
				t.Error("profile update must use the name read from immutable subject lookup")
			}
			if r.URL.Query().Get("columns") != "displayName" {
				t.Error("update is not field restricted")
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 1 {
				t.Error("extra provider fields")
			}
			if body["displayName"] == "" && r.URL.Query().Get("allowEmpty") != "true" {
				t.Error("empty nickname must use provider's explicit allowEmpty contract")
			}
			display = body["displayName"]
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "data": "Affected"})
		case "/api/get-organization":
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"name": "unihub", "passwordType": "bcrypt", "passwordSalt": "", "passwordObfuscatorType": "None", "masterPassword": "", "masterVerificationCode": "", "defaultPassword": "", "useEmailAsUsername": false, "passwordOptions": []string{"Aa123"}}})
		case "/api/set-password":
			r.ParseForm()
			if r.Form.Get("userName") != "login-name" || r.Form.Get("userOwner") != "unihub" {
				t.Error("password target mismatch")
			}
			if r.Form.Get("oldPassword") != "old-password" {
				json.NewEncoder(w).Encode(map[string]string{"status": "error", "msg": "password or code is incorrect, you have 4 remaining chances"})
				return
			}
			passwordChanged = true
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/api/get-account":
			if passwordChanged && r.Header.Get("Authorization") != "Bearer new-token" {
				json.NewEncoder(w).Encode(map[string]string{"status": "error"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]string{"id": "immutable-sub"}})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	u, err := accounts.New(p).EnsureIdentity(ctx, accounts.Identity{Issuer: upstream.URL, Subject: "immutable-sub", Name: ""})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{pool: p, opts: Options{Issuer: upstream.URL, ManagementClientID: "test-management", ManagementClientSecret: "test-only-private", SigningKey: bytes.Repeat([]byte{1}, 32)}, client: upstream.Client()}
	profile, err := s.Profile(ctx, u.ID)
	if err != nil || profile.Username != "login-name" || profile.AvatarURL != "" {
		t.Fatalf("initial profile %#v %v", profile, err)
	}
	updated, err := s.UpdateDisplayName(ctx, u.ID, "New nickname")
	if err != nil || updated.Username != "login-name" || updated.DisplayName != "New nickname" || updated.Version == profile.Version {
		t.Fatalf("update %#v %v", updated, err)
	}
	updated, err = s.UpdateDisplayName(ctx, u.ID, "")
	if err != nil || updated.Username != "login-name" || updated.DisplayName != "" {
		t.Fatalf("empty nickname must retain login name: %#v %v", updated, err)
	}
	updated, err = s.Profile(ctx, u.ID)
	if err != nil || updated.Username != "login-name" || updated.DisplayName != "" {
		t.Fatalf("empty nickname readback: %#v %v", updated, err)
	}
	var cachedName string
	if err = p.QueryRow(ctx, `SELECT name FROM users WHERE id=$1`, u.ID).Scan(&cachedName); err != nil || cachedName != "" {
		t.Fatalf("display-name cache: %q %v", cachedName, err)
	}
	if _, err = s.UpdateDisplayName(ctx, u.ID, strings.Repeat("a", 81)); err != ErrInvalidInput {
		t.Fatalf("long nickname: %v", err)
	}
	var imageData bytes.Buffer
	png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	updated, err = s.PutAvatar(ctx, u.ID, "image/png", imageData.Bytes())
	if err != nil || updated.AvatarURL != "/v1/me/avatar" {
		t.Fatalf("avatar %#v %v", updated, err)
	}
	kind, data, err := s.Avatar(ctx, u.ID)
	if err != nil || kind != "image/png" || len(data) == 0 {
		t.Fatalf("avatar read %s %v", kind, err)
	}
	if _, err = s.PutAvatar(ctx, u.ID, "image/png", []byte("not an image")); err != ErrInvalidInput {
		t.Fatalf("invalid image accepted: %v", err)
	}
	if _, err = s.PutAvatar(ctx, u.ID, "image/svg+xml", []byte("<svg/>")); err != ErrInvalidInput {
		t.Fatalf("SVG accepted: %v", err)
	}
	if !s.SessionAllowed(ctx, "old-token") {
		t.Fatal("existing live token denied")
	}
	if _, err = p.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES('profile-cookie',$1,'immutable-sub','Old','','csrf',$2)`, upstream.URL, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, u.ID, "incorrect", "NewSecretPassword9!"); err != ErrInvalidPassword {
		t.Fatalf("wrong password: %v", err)
	}
	if passwordChanged {
		t.Fatal("wrong old password changed provider")
	}
	if !s.SessionAllowed(ctx, "old-token") {
		t.Fatal("definitive wrong password revoked the live bearer")
	}
	var preservedSessions int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM browser_sessions WHERE subject='immutable-sub'`).Scan(&preservedSessions); err != nil || preservedSessions != 1 {
		t.Fatalf("definitive wrong password removed browser session: %d %v", preservedSessions, err)
	}
	if err = s.ChangePassword(ctx, u.ID, "old-password", "NewSecretPassword9!"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if s.SessionAllowed(ctx, "old-token") || !s.SessionAllowed(ctx, "new-token") {
		t.Fatal("token revocation/fresh-login contract failed")
	}
	var sessions int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM browser_sessions WHERE subject='immutable-sub'`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("browser sessions retained %d %v", sessions, err)
	}
}

func TestChangePasswordUnknownProviderResultRevokesSessions(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider_revoke_failure", true: "caller_cancelled_after_mutation"}[cancelRequest], func(t *testing.T) {
			p := testutil.Database(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := database.Migrate(ctx, p); err != nil {
				t.Fatal(err)
			}
			var called atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/get-user":
					json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]string{"id": "subject", "name": "login", "email": ""}})
				case "/api/get-organization":
					json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"name": "unihub", "passwordType": "bcrypt", "passwordObfuscatorType": "None", "passwordOptions": []string{}, "masterPassword": "", "masterVerificationCode": "", "defaultPassword": ""}})
				case "/api/set-password":
					called.Store(true)
					if cancelRequest {
						cancel()
					}
					// Model the pinned provider's mutation already persisted, followed by
					// token-revocation failure. Its old tokens remain live at get-account.
					json.NewEncoder(w).Encode(map[string]string{"status": "error", "msg": "token store unavailable"})
				case "/api/get-account":
					json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]string{"id": "subject"}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			u, err := accounts.New(p).EnsureIdentity(ctx, accounts.Identity{Issuer: upstream.URL, Subject: "subject"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES('ambiguous',$1,'subject','','','csrf',now()+interval '1 hour')`, upstream.URL); err != nil {
				t.Fatal(err)
			}
			s := &Server{pool: p, opts: Options{Issuer: upstream.URL, ManagementClientID: "m", ManagementClientSecret: "s", SigningKey: bytes.Repeat([]byte{1}, 32)}, client: upstream.Client()}
			oldPayload, _ := json.Marshal(map[string]any{"sub": "subject", "iat": time.Now().Add(-time.Hour).Unix()})
			oldToken := "header." + base64.RawURLEncoding.EncodeToString(oldPayload) + ".signature"
			if !s.SessionAllowed(ctx, oldToken) {
				t.Fatal("old bearer not live before mutation")
			}
			if err = s.ChangePassword(ctx, u.ID, "old", "NewSecretPassword9!"); err != ErrUnavailable {
				t.Fatalf("ambiguous result: %v", err)
			}
			if !called.Load() {
				t.Fatal("set-password was not reached")
			}
			var sessions int
			if err = p.QueryRow(context.Background(), `SELECT count(*) FROM browser_sessions WHERE subject='subject'`).Scan(&sessions); err != nil || sessions != 0 {
				t.Fatalf("sessions retained: %d %v", sessions, err)
			}
			if s.SessionAllowed(context.Background(), oldToken) {
				t.Fatal("still-active provider old bearer bypassed local cutoff")
			}
			freshPayload, _ := json.Marshal(map[string]any{"sub": "subject", "iat": time.Now().Add(time.Second).Unix()})
			freshToken := "header." + base64.RawURLEncoding.EncodeToString(freshPayload) + ".signature"
			if s.SessionAllowed(context.Background(), freshToken) == cancelRequest {
				t.Fatal("later bearer did not respect pending versus completed attempt")
			}
			if s.BrowserSessionAllowed(context.Background(), u.ID) == cancelRequest {
				t.Fatal("browser claims did not respect pending attempt")
			}
		})
	}
}
