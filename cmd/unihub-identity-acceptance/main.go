package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unihub/internal/database"
	"unihub/internal/identityapi"
	"unihub/internal/products"
)

func opaque() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("random unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func check(e error) {
	if e != nil {
		panic("headless probe dependency failure")
	}
}

func privateOutput(path string, body []byte) error {
	if path == "" {
		return fmt.Errorf("output path required")
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe output directory")
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(body)
	return errors.Join(err, f.Close())
}

// This admin-only acceptance command creates a disposable database schema and
// an ordinary test user. Its temporary HTTP server binds loopback only. It never
// changes production's audit gate or starts a public service. A later verifier
// must examine the real provider/proxy sinks before activation.
func main() {
	started := time.Now().UTC().Format(time.RFC3339Nano)
	if os.Getenv("IDENTITY_ACCEPTANCE_CONTROLLED_PROBE") != "true" {
		panic("explicit controlled probe required")
	}
	markers := []string{}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	check(e)
	defer admin.Close()
	schema := "headless_probe_" + hex.EncodeToString([]byte(time.Now().UTC().Format("150405.000")))
	_, e = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	check(e)
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	}()
	cfg, e := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	check(e)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	check(e)
	defer p.Close()
	check(database.Migrate(ctx, p))
	issuer := os.Getenv("OIDC_ISSUER")
	registryRaw, e := os.ReadFile(os.Getenv("IDENTITY_ACCEPTANCE_CLIENT_FILE"))
	check(e)
	registry, e := products.Parse(registryRaw)
	check(e)
	var c products.Client
	for _, value := range registry {
		if value.ClientID == os.Getenv("IDENTITY_ACCEPTANCE_CLIENT_ID") {
			c = value
		}
	}
	if c.ClientID == "" || !c.Registration || !c.PasswordLogin || len(c.Origins) == 0 {
		panic("public acceptance client unavailable")
	}

	s, e := identityapi.New(ctx, p, identityapi.Options{Issuer: issuer, ManagementClientID: os.Getenv("HEADLESS_MANAGEMENT_CLIENT_ID"), ManagementClientSecret: os.Getenv("HEADLESS_MANAGEMENT_CLIENT_SECRET"), Production: strings.HasPrefix(issuer, "https://"), SigningKey: []byte(opaque()), AuditRedactionVerified: true, Clients: map[string]products.Client{c.ClientID: c}})
	check(e)
	api := httptest.NewServer(s.Handler(http.NotFoundHandler()))
	defer api.Close()
	call := func(path, origin string, body any, key string) (int, map[string]any) {
		b, _ := json.Marshal(body)
		r, _ := http.NewRequestWithContext(ctx, "POST", api.URL+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		res, e := api.Client().Do(r)
		check(e)
		defer res.Body.Close()
		var out map[string]any
		check(json.NewDecoder(res.Body).Decode(&out))
		return res.StatusCode, out
	}
	marker := "hlprobe" + strings.ToLower(hex.EncodeToString([]byte(time.Now().UTC().Format("150405.000"))))
	password := "ProbeSecret9!" + opaque()
	body := map[string]any{"client_id": c.ClientID, "username": marker, "password": password, "email": "", "display_name": "Headless isolated protocol probe"}
	registrationStatus, reg := call("/v1/identity/registrations", c.Origins[0], body, opaque())
	if registrationStatus != 201 {
		panic("real JSON registration did not create isolated user")
	}
	verifier := opaque()
	digest := sha256.Sum256([]byte(verifier))
	auth := map[string]any{"client_id": c.ClientID, "redirect_uri": c.RedirectURIs[0], "scope": "openid profile email", "state": opaque(), "nonce": opaque(), "code_challenge_method": "S256", "code_challenge": base64.RawURLEncoding.EncodeToString(digest[:]), "username": marker, "password": "wrong-password"}
	foreign, _ := call("/v1/identity/authorize/password", "https://foreign.example", auth, "")
	bad, badResult := call("/v1/identity/authorize/password", c.Origins[0], auth, "")
	auth["password"] = password
	status, out := call("/v1/identity/authorize/password", c.Origins[0], auth, "")
	result := map[string]any{"controlled_loopback_only": true, "audit_privacy_verified": false, "started_at": started, "client_id": c.ClientID, "registration_status": registrationStatus, "registration_error": reg["error"], "foreign_origin_status": foreign, "invalid_password_status": bad, "invalid_password_error": badResult["error"], "authorization_status": status, "authorization_error": out["error"], "authorization_interaction": out["interaction"], "fixture_username": marker}
	markers = append(markers, password, auth["state"].(string), auth["nonce"].(string), verifier, auth["code_challenge"].(string))
	if status == 200 {
		markers = append(markers, out["code"].(string))
		token := map[string]any{"client_id": c.ClientID, "redirect_uri": c.RedirectURIs[0], "authorization_id": out["authorization_id"], "code": out["code"], "code_verifier": verifier, "grant_type": "authorization_code"}
		ts, tok := call("/v1/identity/token", c.Origins[0], token, "")
		replay, _ := call("/v1/identity/token", c.Origins[0], token, "")
		for _, key := range []string{"access_token", "id_token"} {
			if value, ok := tok[key].(string); ok {
				markers = append(markers, value)
			}
		}
		result["token_status"] = ts
		result["token_error"] = tok["error"]
		result["token_type"] = tok["token_type"]
		result["scope"] = tok["scope"]
		result["has_access_token"] = tok["access_token"] != "" && tok["access_token"] != nil
		result["has_id_token"] = tok["id_token"] != "" && tok["id_token"] != nil
		_, result["has_refresh_token"] = tok["refresh_token"]
		result["replay_status"] = replay
	}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
	check(privateOutput(os.Getenv("IDENTITY_ACCEPTANCE_RESULT"), append(encoded, '\n')))
	private, _ := json.Marshal(map[string]any{"audit_markers": markers})
	check(privateOutput(os.Getenv("IDENTITY_ACCEPTANCE_PRIVATE_MARKERS"), private))
	if registrationStatus != 201 || status != 200 || result["token_status"] != 200 || result["replay_status"] != 400 || foreign != 403 || bad != 401 {
		panic("controlled identity acceptance failed")
	}
}
