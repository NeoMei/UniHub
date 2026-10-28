package identityapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"unihub/internal/platformadmin"
)

func TestAdminProviderGuardAndExactSubjectReadback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get-organization" {
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"name": "unihub", "passwordType": "bcrypt", "passwordObfuscatorType": "", "passwordOptions": []string{}, "masterPassword": "", "masterVerificationCode": "", "defaultPassword": ""}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]any{"id": "different-subject", "owner": "unihub", "name": "alice", "type": "normal-user", "email": "a@example.test", "emailVerified": true}})
	}))
	defer srv.Close()
	s := &Server{opts: Options{Issuer: srv.URL, AuditRedactionVerified: true}, client: srv.Client()}
	p, e := s.AdministrationProvider(context.Background(), AdminProviderOptions{PinnedVersion: AdminPinnedVersion, Organization: "unihub", DeploymentVerified: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.ReadIdentity(context.Background(), srv.URL, "exact-subject"); e == nil {
		t.Fatal("provider subject mismatch trusted")
	}
	if _, e = s.AdministrationProvider(context.Background(), AdminProviderOptions{PinnedVersion: "other", Organization: "unihub", DeploymentVerified: true}); e == nil {
		t.Fatal("unvalidated provider version accepted")
	}
	var _ platformadmin.IdentityProvider = p
}

type adminLocalDelivery struct{}

func (adminLocalDelivery) DeliverIdentityRecovery(context.Context, string, string, string) error {
	return nil
}
func TestAdminProviderRevokesExactSSOAndRefreshWithoutOtherUser(t *testing.T) {
	sessions, tokens := true, true
	passwordWrites, sessionDeletes, tokenDeletes := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := func(data any) { json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": data}) }
		switch r.URL.Path {
		case "/api/get-organization":
			answer(map[string]any{"name": "unihub", "passwordType": "bcrypt", "passwordObfuscatorType": "", "passwordOptions": []string{}, "masterPassword": "", "masterVerificationCode": "", "defaultPassword": ""})
		case "/api/get-user":
			answer(map[string]any{"id": "exact-subject", "owner": "unihub", "name": "alice", "type": "normal-user", "email": "a@example.test", "emailVerified": true})
		case "/api/set-password":
			r.ParseForm()
			if r.Form.Get("userName") != "alice" || r.Form.Get("userOwner") != "unihub" || r.Form.Get("newPassword") != "CorrectHorse9Battery!" {
				t.Error("wrong password target")
			}
			passwordWrites++
			answer(true)
		case "/api/get-sessions":
			if sessions {
				answer([]map[string]string{{"owner": "unihub", "name": "alice", "application": "console"}})
			} else {
				answer([]any{})
			}
		case "/api/delete-session":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["owner"] != "unihub" || body["name"] != "alice" || body["application"] != "console" {
				t.Error("wrong session target")
			}
			sessions = false
			sessionDeletes++
			answer(true)
		case "/api/get-tokens":
			if r.URL.Query().Get("organization") != "unihub" || r.URL.Query().Get("value") != "alice" {
				t.Error("token filter not scoped")
			}
			if tokens {
				answer([]map[string]string{{"owner": "admin", "name": "opaque-token-ref", "organization": "unihub", "user": "alice"}})
			} else {
				answer([]any{})
			}
		case "/api/delete-token":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["organization"] != "unihub" || body["user"] != "alice" || body["name"] != "opaque-token-ref" {
				t.Error("wrong refresh target")
			}
			tokens = false
			tokenDeletes++
			answer(true)
		default:
			t.Error("unexpected provider endpoint", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer srv.Close()
	s := &Server{opts: Options{Issuer: srv.URL, AuditRedactionVerified: true}, client: srv.Client()}
	p, e := s.AdministrationProvider(context.Background(), AdminProviderOptions{PinnedVersion: AdminPinnedVersion, Organization: "unihub", DeploymentVerified: true, SessionRevocationVerified: true, PasswordResetVerified: true, Delivery: adminLocalDelivery{}, PublicOrigin: srv.URL})
	if e != nil {
		t.Fatal(e)
	}
	id, e := p.ReadIdentity(context.Background(), srv.URL, "exact-subject")
	if e != nil {
		t.Fatal(e)
	}
	if e = p.SetRecoveryPassword(context.Background(), id, "CorrectHorse9Battery!", "op"); e != nil {
		t.Fatal(e)
	}
	if e = p.RevokeIdentitySessions(context.Background(), id, "op"); e != nil {
		t.Fatal(e)
	}
	if passwordWrites != 1 || sessionDeletes != 1 || tokenDeletes != 1 || sessions || tokens {
		t.Fatal(passwordWrites, sessionDeletes, tokenDeletes)
	}
}
