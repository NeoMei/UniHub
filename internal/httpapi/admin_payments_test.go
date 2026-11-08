package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/console"
	"unihub/internal/database"
	"unihub/internal/paymentconfig"
	"unihub/internal/platformadmin"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestMerchantAdministrationAuthorityAndFailureTruth(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(pool)
	ids := map[string]string{}
	for _, role := range []string{"bob", "alice", "finance", "auditor", "support"} {
		u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: role})
		if e != nil {
			t.Fatal(e)
		}
		ids[role] = u.ID
	}
	admin := platformadmin.New(pool, nil)
	if e := admin.Bootstrap(ctx, "fixture", ids["bob"], "bootstrap fixture"); e != nil {
		t.Fatal(e)
	}
	for name, role := range map[string]string{"finance": "finance_admin", "auditor": "auditor", "support": "user_admin"} {
		if _, e := admin.SetRoles(ctx, ids["bob"], ids[name], []string{role}, 0, "fixture authority"); e != nil {
			t.Fatal(e)
		}
	}
	path := filepath.Join(merchantTestDir(t), "key")
	key := make([]byte, 32)
	rand.Read(key)
	os.WriteFile(path, key, 0600)
	manager, e := paymentconfig.New(pool, admin, path, "https://hub.example")
	if e != nil {
		t.Fatal(e)
	}
	b := billing.New(pool, a, noLedger{})
	b.ConfigurePaymentSource(manager.Providers)
	h := New(merchantVerifier{}, a, wallet.New(pool, a, noLedger{}), Options{PlatformAdmin: admin, PaymentConfiguration: manager, Billing: b, ProductVerifiers: map[string]auth.Verifier{"notes-client": productAccessVerifier{}}})
	const route = "/admin/v1/payment-providers"
	for _, actor := range []string{"bob", "finance", "auditor"} {
		w := call(h, "GET", route, actor, "", "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(actor, w.Code, w.Body)
		}
	}
	for _, actor := range []string{"alice", "support", "product-alice"} {
		w := call(h, "GET", route, actor, "", "")
		if w.Code != 403 {
			t.Fatal(actor, w.Code, w.Body)
		}
	}
	body := `{"expected_version":0,"reason":"disable empty fixture","enabled":false}`
	for _, actor := range []string{"alice", "auditor", "support", "product-alice"} {
		w := call(h, "PUT", route+"/alipay", actor, body, "")
		if w.Code != 403 {
			t.Fatal(actor, w.Code, w.Body)
		}
	}
	for _, field := range []string{`"private_key_file":"/secret"`, `"notify_url":"https://evil.example"`, `"private_key_pem":""`, `"private_key_pem":null`, `"enabled":false`, `"merchant_id":""`, `"platform_key_ids":[]`} {
		invalid := strings.TrimSuffix(body, "}") + "," + field + "}"
		w := call(h, "PUT", route+"/alipay", "finance", invalid, "")
		if w.Code != 400 {
			t.Fatal("invalid field", field, w.Code, w.Body)
		}
	}
	w := call(h, "PUT", route+"/alipay", "finance", body, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var status paymentconfig.Status
	if e = json.Unmarshal(w.Body.Bytes(), &status); e != nil || status.Version != 1 || status.Configured || !status.Managed {
		t.Fatal(status, e)
	}
	w = call(h, "PUT", route+"/alipay", "finance", body, "")
	if w.Code != 409 {
		t.Fatal("CAS", w.Code, w.Body)
	}
	if _, e = admin.SetRoles(ctx, ids["bob"], ids["finance"], []string{}, 1, "revoke finance"); e != nil {
		t.Fatal(e)
	}
	v := int64(1)
	enabled := false
	if _, e = manager.Update(ctx, ids["finance"], "alipay", paymentconfig.UpdateRequest{ExpectedVersion: &v, Enabled: &enabled, Reason: "revoked update"}); e != accounts.ErrForbidden {
		t.Fatal("direct service stale authority", e)
	}
	os.Remove(path)
	for _, route := range []string{route, "/admin/v1/system-status", "/v1/capabilities"} {
		w = call(h, "GET", route, "bob", "", "")
		if w.Code != 503 || !strings.Contains(w.Body.String(), "payment_configuration_unavailable") {
			t.Fatal("read failure hidden", route, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), path) {
			t.Fatal("path disclosed")
		}
	}
}

type merchantVerifier struct{}

func (merchantVerifier) Verify(ctx context.Context, token string) (auth.Claims, error) {
	switch token {
	case "finance", "auditor", "support":
		return auth.Claims{Issuer: "https://test", Subject: token}, nil
	}
	return (verifier{}).Verify(ctx, token)
}

func TestMerchantAdministrationComposedConsoleCSRF(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	if e := database.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(pool)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if e != nil {
		t.Fatal(e)
	}
	admin := platformadmin.New(pool, nil)
	if e = admin.Bootstrap(ctx, "fixture", u.ID, "fixture bootstrap"); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(merchantTestDir(t), "key")
	os.WriteFile(path, make([]byte, 32), 0600)
	manager, e := paymentconfig.New(pool, admin, path, "https://hub.example")
	if e != nil {
		t.Fatal(e)
	}
	api := New(verifier{}, a, wallet.New(pool, a, noLedger{}), Options{PlatformAdmin: admin, PaymentConfiguration: manager})
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks"})
	}))
	defer issuer.Close()
	browser, e := console.New(ctx, pool, verifier{}, console.Options{Issuer: issuer.URL, ClientID: "fixture", ClientSecret: "fixture", PublicURL: "https://hub.example"})
	if e != nil {
		t.Fatal(e)
	}
	h := browser.Handler(api)
	cookie := strings.Repeat("s", 43)
	csrf := strings.Repeat("c", 43)
	hash := sha256.Sum256([]byte(cookie))
	if _, e = pool.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,'https://test','bob','','',$2,now()+interval '1 hour')`, hex.EncodeToString(hash[:]), csrf); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		origin, csrf string
		want         int
	}{{"", "", 403}, {"https://hub.example", "", 403}, {"https://evil.example", csrf, 403}, {"https://hub.example", "wrong", 403}, {"https://hub.example", csrf, 200}} {
		req := httptest.NewRequest("PUT", "/admin/v1/payment-providers/alipay", strings.NewReader(`{"expected_version":0,"reason":"composed fixture","enabled":false}`))
		req.AddCookie(&http.Cookie{Name: "unihub_session", Value: cookie})
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("X-CSRF-Token", tc.csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatal(tc.want, w.Code, w.Body)
		}
	}
	status, e := manager.Status(ctx, u.ID)
	if e != nil || status.Version != 1 {
		t.Fatal("CSRF rejection changed durable revision", status, e)
	}
}

func merchantTestDir(t *testing.T) string {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return dir
}
