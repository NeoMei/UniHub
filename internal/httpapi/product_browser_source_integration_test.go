package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

const (
	sourceIssuer        = "https://controlled-idp.test"
	sourceSubject       = "controlled-source-subject"
	sourceUser          = "Source Proof Test"
	sourceProduct       = "llmrouter"
	sourceClient        = "source-proof-client"
	sourceInstance      = "default"
	nativeSourceProduct = "native-source"
)

func TestBrowserSourceProofCentralSessionRevocationAndScope(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	user, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: sourceIssuer, Subject: sourceSubject, Name: sourceUser})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := productusers.Parse([]byte(`[{"product_id":"llmrouter","name":"Router","entry_url":"https://router.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]} , {"product_id":"native-source","name":"Native source","entry_url":"https://native-source.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	users := productusers.New(p, registry, "https://hub.test", nil)
	pa := productauth.New(p, a, users, "https://hub.test")
	client := products.Client{ClientID: sourceClient, ProductID: sourceProduct, InstanceID: sourceInstance, BrowserSSO: true, RedirectURIs: []string{"https://router.test/api/unihub/callback"}}
	pa.ConfigureBrowserClients(map[string]products.Client{sourceClient: client})
	services := map[string]RuntimeService{
		"source":         {Token: strings.Repeat("s", 40), ProductID: sourceProduct, InstanceID: sourceInstance, FinanceClientID: sourceClient, AllowedOps: []string{"identity:browser"}},
		"wrong-client":   {Token: strings.Repeat("c", 40), ProductID: sourceProduct, InstanceID: sourceInstance, FinanceClientID: "not-registered", AllowedOps: []string{"identity:browser"}},
		"wrong-product":  {Token: strings.Repeat("p", 40), ProductID: "other-product", InstanceID: sourceInstance, FinanceClientID: sourceClient, AllowedOps: []string{"identity:browser"}},
		"wrong-instance": {Token: strings.Repeat("i", 40), ProductID: sourceProduct, InstanceID: "other-instance", FinanceClientID: sourceClient, AllowedOps: []string{"identity:browser"}},
	}
	api := New(runtimeVerifier{}, a, wallet.New(p, a, nil), Options{ProductUsers: users, ProductAuth: pa, ProductAccess: map[string]products.Client{sourceClient: client}, ProductRuntime: services})
	h := pa.BrowserHandler(api)
	request := func(method, path, service string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "https://hub.test"+path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if service != "" {
			req.Header.Set("Authorization", "Bearer "+services[service].Token)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		return out
	}
	newSession := func(n int) *http.Cookie {
		t.Helper()
		raw := fmt.Sprintf("%042dX", n)
		sum := sha256.Sum256([]byte(raw))
		_, err := p.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,$2,$3,$4,'','csrf',now()+interval '1 hour')`, hex.EncodeToString(sum[:]), sourceIssuer, sourceSubject, sourceUser)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "unihub_session", Value: raw}
	}
	codeForCookie := func(n int, cookie *http.Cookie) productauth.BrowserExchange {
		t.Helper()
		verifier := strings.Repeat("v", 43)
		hash := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(hash[:])
		nonce := fmt.Sprintf("nonce-%038d", n)
		state := fmt.Sprintf("state-%038d", n)
		path := "/auth/product/authorize?" + url.Values{
			"client_id": {sourceClient}, "redirect_uri": {client.RedirectURIs[0]}, "state": {state}, "nonce": {nonce},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		}.Encode()
		authorized := request(http.MethodGet, path, "", nil, cookie)
		if authorized.Code != http.StatusSeeOther {
			t.Fatalf("authorization status=%d body=%s", authorized.Code, authorized.Body.String())
		}
		location, err := url.Parse(authorized.Header().Get("Location"))
		if err != nil || location.Query().Get("code") == "" {
			t.Fatalf("authorization returned no code: %s (%v)", authorized.Header().Get("Location"), err)
		}
		return productauth.BrowserExchange{ClientID: sourceClient, Code: location.Query().Get("code"), Verifier: verifier, Nonce: nonce, RedirectURI: client.RedirectURIs[0]}
	}
	newCode := func(n int) (*http.Cookie, productauth.BrowserExchange) {
		t.Helper()
		cookie := newSession(n)
		return cookie, codeForCookie(n, cookie)
	}
	exchange := func(in productauth.BrowserExchange) productauth.BrowserGrant {
		t.Helper()
		response := request(http.MethodPost, "/internal/v1/product-runtime/identity/browser/exchange", "source", in, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("exchange status=%d body=%s", response.Code, response.Body.String())
		}
		var grant productauth.BrowserGrant
		if err := json.Unmarshal(response.Body.Bytes(), &grant); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(grant.SourceProofToken, "ubsp_") || grant.SourceProofExpiresAt <= time.Now().Add(2*time.Minute).Unix() {
			t.Fatalf("source proof should outlive the one-use bind proof: %+v", grant)
		}
		if len(grant.SourceProofToken) != len("ubsp_")+64 {
			t.Fatalf("source proof has unexpected length: %d", len(grant.SourceProofToken))
		}
		if _, err := hex.DecodeString(grant.SourceProofToken[len("ubsp_"):]); err != nil || strings.ToLower(grant.SourceProofToken) != grant.SourceProofToken {
			t.Fatalf("source proof is not lowercase hex: %q", grant.SourceProofToken)
		}
		return grant
	}
	verify := func(grant productauth.BrowserGrant, service string) int {
		t.Helper()
		body := map[string]string{"source_proof_token": grant.SourceProofToken, "central_user_id": user.ID}
		return request(http.MethodPost, "/internal/v1/product-runtime/identity/browser/source/verify", service, body, nil).Code
	}
	assertFreshProof := func(n int) (cookie *http.Cookie, exchangeRequest productauth.BrowserExchange, grant productauth.BrowserGrant) {
		t.Helper()
		cookie, exchangeRequest = newCode(n)
		grant = exchange(exchangeRequest)
		if got := verify(grant, "source"); got != http.StatusNoContent {
			t.Fatalf("valid source proof status=%d want=%d", got, http.StatusNoContent)
		}
		return
	}

	// This is a controlled central browser-session row, not an IdP or public-browser acceptance test.
	cookie, pending := newCode(1)
	if _, err := p.Exec(ctx, `DELETE FROM browser_sessions WHERE id_hash=$1`, hashSessionCookie(cookie.Value)); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodPost, "/internal/v1/product-runtime/identity/browser/exchange", "source", pending, nil).Code; got != http.StatusForbidden {
		t.Fatalf("pending code exchanged after central logout: status=%d", got)
	}

	cases := []struct {
		name    string
		mutate  func(*testing.T)
		restore func(*testing.T)
	}{
		{name: "browser session deletion cascades proof", mutate: func(t *testing.T) {
			c, _, g := assertFreshProof(2)
			if _, err := p.Exec(ctx, `DELETE FROM browser_sessions WHERE id_hash=$1`, hashSessionCookie(c.Value)); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := p.QueryRow(ctx, `SELECT count(*) FROM product_browser_source_proofs WHERE token_hash=$1`, hashSourceProof(g.SourceProofToken)).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("proof row survived source-session deletion: count=%d", count)
			}
			if got := verify(g, "source"); got != http.StatusForbidden {
				t.Fatalf("verification after logout status=%d", got)
			}
		}},
		{name: "session not before cutoff", mutate: func(t *testing.T) {
			_, _, g := assertFreshProof(3)
			if _, err := p.Exec(ctx, `UPDATE users SET session_not_before=now()+interval '1 hour' WHERE id=$1`, user.ID); err != nil {
				t.Fatal(err)
			}
			if got := verify(g, "source"); got != http.StatusForbidden {
				t.Fatalf("verification after session cutoff status=%d", got)
			}
		}, restore: func(t *testing.T) {
			if _, err := p.Exec(ctx, `UPDATE users SET session_not_before='epoch' WHERE id=$1`, user.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "pending identity revocation", mutate: func(t *testing.T) {
			_, _, g := assertFreshProof(4)
			if _, err := p.Exec(ctx, `INSERT INTO identity_revocations(id,issuer,subject,revoked_after,pending) VALUES('source-pending-revoke',$1,$2,now(),true)`, sourceIssuer, sourceSubject); err != nil {
				t.Fatal(err)
			}
			if got := verify(g, "source"); got != http.StatusForbidden {
				t.Fatalf("verification during pending revocation status=%d", got)
			}
		}, restore: func(t *testing.T) {
			if _, err := p.Exec(ctx, `DELETE FROM identity_revocations WHERE id='source-pending-revoke'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "global user inactive", mutate: func(t *testing.T) {
			_, _, g := assertFreshProof(5)
			if _, err := p.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, user.ID); err != nil {
				t.Fatal(err)
			}
			if got := verify(g, "source"); got != http.StatusForbidden {
				t.Fatalf("verification with inactive central user status=%d", got)
			}
		}, restore: func(t *testing.T) {
			if _, err := p.Exec(ctx, `UPDATE users SET active=true WHERE id=$1`, user.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "destination product access inactive", mutate: func(t *testing.T) {
			_, _, g := assertFreshProof(6)
			if _, err := p.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,$2,$3,'["member"]',false,1) ON CONFLICT(user_id,product_id,instance_id) DO UPDATE SET active=false,version=product_user_access.version+1`, user.ID, sourceProduct, sourceInstance); err != nil {
				t.Fatal(err)
			}
			if got := verify(g, "source"); got != http.StatusForbidden {
				t.Fatalf("verification with inactive destination product access status=%d", got)
			}
		}, restore: func(t *testing.T) {
			if _, err := p.Exec(ctx, `DELETE FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, user.ID, sourceProduct, sourceInstance); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong product instance and client", mutate: func(t *testing.T) {
			_, _, g := assertFreshProof(7)
			for _, service := range []string{"wrong-product", "wrong-instance", "wrong-client"} {
				if got := verify(g, service); got != http.StatusForbidden {
					t.Errorf("verification with %s service status=%d", service, got)
				}
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.mutate(t)
			if tc.restore != nil {
				tc.restore(t)
			}
		})
	}

	// Ensure issue/exchange did not persist the raw proof or create any extra central identity.
	var rawProofRows, hashedProofRows, userCount, identityCount int
	lastCookie, _, lastGrant := assertFreshProof(8)
	_ = lastCookie
	if err := p.QueryRow(ctx, `SELECT count(*) FROM product_browser_source_proofs WHERE token_hash=$1`, lastGrant.SourceProofToken).Scan(&rawProofRows); err != nil {
		t.Fatal(err)
	}
	if rawProofRows != 0 {
		t.Fatal("raw source proof was stored")
	}
	if err := p.QueryRow(ctx, `SELECT count(*) FROM product_browser_source_proofs WHERE token_hash=$1`, hashSourceProof(lastGrant.SourceProofToken)).Scan(&hashedProofRows); err != nil {
		t.Fatal(err)
	}
	if hashedProofRows != 1 {
		t.Fatal("hashed source proof row missing")
	}
	if err := p.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT count(*) FROM identities`).Scan(&identityCount); err != nil {
		t.Fatal(err)
	}
	if userCount != 1 || identityCount != 1 {
		t.Fatalf("browser source verification created a merge or extra identity: users=%d identities=%d", userCount, identityCount)
	}
	if got := verify(lastGrant, "source"); got != http.StatusNoContent {
		t.Fatalf("final fresh proof status=%d", got)
	}

	t.Run("native origin auth version invalidates independently", func(t *testing.T) {
		binding, err := users.EnsureRuntimeBinding(ctx, nativeSourceProduct, sourceInstance, "native-source-user", "native-source-binding-key", accounts.Identity{Issuer: sourceIssuer, Subject: sourceSubject}, user)
		if err != nil {
			t.Fatal(err)
		}
		if err := pa.AdoptBinding(ctx, nativeSourceProduct, sourceInstance, "native-source-user", 1); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,$2,$3,'["member"]',true,1)`, user.ID, nativeSourceProduct, sourceInstance); err != nil {
			t.Fatal(err)
		}
		brokerIssuer := "https://hub.test/product-broker"
		if _, err := p.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3)`, brokerIssuer, user.ID, user.ID); err != nil {
			t.Fatal(err)
		}
		rawCookie := fmt.Sprintf("%042dX", 9)
		sessionHash := hashSessionCookie(rawCookie)
		if _, err := p.Exec(ctx, `INSERT INTO browser_sessions(id_hash,issuer,subject,name,email,csrf_token,expires_at) VALUES($1,$2,$3,$4,'','csrf',now()+interval '1 hour')`, sessionHash, brokerIssuer, user.ID, sourceUser); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO product_browser_session_origins(session_id_hash,binding_id,auth_version) VALUES($1,$2,1)`, sessionHash, binding.ID); err != nil {
			t.Fatal(err)
		}
		cookie := &http.Cookie{Name: "unihub_session", Value: rawCookie}
		grant := exchange(codeForCookie(9, cookie))
		if got := verify(grant, "source"); got != http.StatusNoContent {
			t.Fatalf("valid native-origin proof status=%d", got)
		}
		if _, err := p.Exec(ctx, `UPDATE product_auth_subjects SET auth_version=2 WHERE binding_id=$1`, binding.ID); err != nil {
			t.Fatal(err)
		}
		var sessionCount int
		if err := p.QueryRow(ctx, `SELECT count(*) FROM browser_sessions WHERE id_hash=$1`, sessionHash).Scan(&sessionCount); err != nil {
			t.Fatal(err)
		}
		if sessionCount != 1 {
			t.Fatal("native auth-version mutation removed the central browser session")
		}
		if got := verify(grant, "source"); got != http.StatusForbidden {
			t.Fatalf("native-origin auth-version change status=%d", got)
		}
	})
}

func hashSessionCookie(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func hashSourceProof(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
