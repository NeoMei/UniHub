package httpapi

import (
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
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/modelaccess"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestAccountModelConsistencyUsesAuthorizedFundingAndProduct(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	makeToken := func(subject, audience, kind string, exp time.Time) string {
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key"})
		claims, _ := json.Marshal(map[string]any{"iss": issuer.URL, "sub": subject, "aud": audience, "iat": time.Now().Add(-time.Minute).Unix(), "exp": exp.Unix(), "tokenType": kind, "scope": "openid"})
		input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
		sum := sha256.Sum256([]byte(input))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		return input + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	consoleOIDC, e := auth.NewOIDC(ctx, issuer.URL, "unihub")
	if e != nil {
		t.Fatal(e)
	}
	productOIDC, e := auth.NewOIDC(ctx, issuer.URL, "sw")
	if e != nil {
		t.Fatal(e)
	}
	aliceToken := makeToken("alice", "unihub", "access-token", time.Now().Add(time.Hour))
	aliceProductToken := makeToken("alice", "sw", "access-token", time.Now().Add(time.Hour))
	a := accounts.New(p)
	alice, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: issuer.URL, Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	bob, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: issuer.URL, Subject: "bob"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, alice.ID)
	if e != nil {
		t.Fatal(e)
	}
	bs, e := a.ListAccounts(ctx, bob.ID)
	if e != nil {
		t.Fatal(e)
	}
	profile := `{"product_id":"superwagie","version":1,"usage":{"enabled":true,"funding_modes":["prepaid_cash","credits"],"default_funding_mode":"credits","models":["shared","missing"]}}`
	if _, e = p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) VALUES('superwagie',1,'deployment',$1)`, profile); e != nil {
		t.Fatal(e)
	}
	for _, r := range []billing.Rate{{ID: "cash", Model: "shared", Status: "published", Asset: billing.CashAsset}, {ID: "credit", Model: "shared", Status: "published", Asset: billing.CreditAsset}, {ID: "secret", Model: "foreign-model", Status: "published", Asset: billing.CashAsset}} {
		raw, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.Exec(ctx, `INSERT INTO billing_rates(id,status,snapshot) VALUES($1,'published',$2)`, r.ID, raw); e != nil {
			t.Fatal(e)
		}
	}
	registry, e := productusers.Parse([]byte(`[{"product_id":"superwagie","instance_id":"default","name":"SuperWagie","entry_url":"https://product.test","version":1,"default_roles":["member"],"roles":[{"id":"member","name":"Member","permissions":["product:use"]},{"id":"viewer","name":"Viewer","permissions":["catalog:read"]}]}]`))
	if e != nil {
		t.Fatal(e)
	}
	users := productusers.New(p, registry, "https://hub.test", nil)
	b := billing.New(p, a, noLedger{})
	client := products.Client{ProductID: "superwagie", ClientID: "sw", Scopes: []string{"catalog:read"}}
	h := New(consoleOIDC, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ModelAccess: modelaccess.New(p, a, b), ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"sw": productOIDC}, ProductAccess: map[string]products.Client{"sw": client}})
	base := "/v1/products/superwagie/model-consistency"
	path := func(account, mode string) string {
		return base + "?account_id=" + url.QueryEscape(account) + "&funding_mode=" + url.QueryEscape(mode)
	}
	for _, token := range []string{
		makeToken("alice", "unihub", "id-token", time.Now().Add(time.Hour)),
		makeToken("alice", "unihub", "refresh-token", time.Now().Add(time.Hour)),
		makeToken("alice", "unihub", "access-token", time.Now().Add(-time.Minute)),
		makeToken("alice", "other", "access-token", time.Now().Add(time.Hour)),
	} {
		w := call(h, "GET", path(as[0].ID, "prepaid_cash"), token, "", "")
		if w.Code != 401 {
			t.Fatalf("invalid bearer accepted: %d %s", w.Code, w.Body)
		}
	}
	for _, tc := range []struct{ mode, asset, rate string }{{"prepaid_cash", billing.CashAsset, "cash"}, {"credits", billing.CreditAsset, "credit"}} {
		w := call(h, "GET", path(as[0].ID, tc.mode), aliceToken, "", "")
		var out modelaccess.Consistency
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%s: %d %s", tc.mode, w.Code, w.Body)
		}
		if out.Asset != tc.asset || len(out.PublishedRates) != 1 || out.PublishedRates[0].ID != tc.rate || len(out.MissingModels) != 1 || out.MissingModels[0] != "missing" || len(out.ExtraModels) != 0 || strings.Contains(w.Body.String(), "foreign-model") {
			t.Fatalf("%s leaked or wrong projection: %s", tc.mode, w.Body)
		}
	}
	if w := call(h, "GET", path(as[0].ID, "prepaid_cash"), aliceProductToken, "", ""); w.Code != 200 || strings.Contains(w.Body.String(), "foreign-model") {
		t.Fatalf("product token: %d %s", w.Code, w.Body)
	}
	if _, e = p.Exec(ctx, `DELETE FROM account_members WHERE account_id=$1 AND user_id=$2`, as[0].ID, alice.ID); e != nil {
		t.Fatal(e)
	}
	if w := call(h, "GET", path(as[0].ID, "prepaid_cash"), aliceToken, "", ""); w.Code != 403 {
		t.Fatalf("removed member: %d %s", w.Code, w.Body)
	}
	if _, e = p.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'owner')`, as[0].ID, alice.ID); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{path(bs[0].ID, "prepaid_cash"), path(as[0].ID, "invalid"), base + "?funding_mode=credits", base + "?account_id=" + as[0].ID, base + "?account_id=" + as[0].ID + "&funding_mode=credits&funding_mode=prepaid_cash"} {
		w := call(h, "GET", raw, aliceToken, "", "")
		if w.Code == 200 {
			t.Fatalf("unauthorized or malformed query accepted: %s %s", raw, w.Body)
		}
	}
	if w := call(h, "GET", path(bs[0].ID, "prepaid_cash"), aliceProductToken, "", ""); w.Code != 403 {
		t.Fatalf("foreign payer: %d %s", w.Code, w.Body)
	}
	if w := call(h, "GET", "/v1/products/llmrouter/model-consistency?account_id="+as[0].ID+"&funding_mode=credits", aliceProductToken, "", ""); w.Code != 403 {
		t.Fatalf("foreign product: %d %s", w.Code, w.Body)
	}
	badClient := client
	badClient.InstanceID = "unregistered"
	badHandler := New(consoleOIDC, a, wallet.New(p, a, noLedger{}), Options{Billing: b, ModelAccess: modelaccess.New(p, a, b), ProductUsers: users, ProductVerifiers: map[string]auth.Verifier{"sw": productOIDC}, ProductAccess: map[string]products.Client{"sw": badClient}})
	if w := call(badHandler, "GET", path(as[0].ID, "prepaid_cash"), aliceProductToken, "", ""); w.Code != 403 {
		t.Fatalf("unregistered instance: %d %s", w.Code, w.Body)
	}
	if _, e = p.Exec(ctx, `INSERT INTO product_user_access(user_id,product_id,instance_id,roles,active,version) VALUES($1,'superwagie','default','["viewer"]',true,1)`, alice.ID); e != nil {
		t.Fatal(e)
	}
	if w := call(h, "GET", path(as[0].ID, "prepaid_cash"), aliceToken, "", ""); w.Code != 403 {
		t.Fatalf("permissionless product role: %d %s", w.Code, w.Body)
	}
	if _, e = p.Exec(ctx, `UPDATE product_user_access SET roles='["member"]' WHERE user_id=$1`, alice.ID); e != nil {
		t.Fatal(e)
	}
	// An omitted/null allowlist is an unrestricted product policy. The UI
	// selects a model by membership in allowlist, so the scoped DTO must derive
	// that list from this asset's actual published rates, not send an empty list.
	if _, e = p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) SELECT product_id,2,actor_id,jsonb_set(jsonb_set(snapshot,'{usage,models}','null'),'{version}','2') FROM product_billing_profiles WHERE product_id='superwagie' AND version=1`); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		mode string
		want []string
	}{{"prepaid_cash", []string{"foreign-model", "shared"}}, {"credits", []string{"shared"}}} {
		w := call(h, "GET", path(as[0].ID, tc.mode), aliceToken, "", "")
		var out modelaccess.Consistency
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("unrestricted %s: %d %s", tc.mode, w.Code, w.Body)
		}
		if !slices.Equal(out.Allowlist, tc.want) || len(out.ExtraModels) != 0 || len(out.MissingModels) != 0 {
			t.Fatalf("unrestricted %s: %s", tc.mode, w.Body)
		}
		for _, r := range out.PublishedRates {
			if !slices.Contains(out.Allowlist, r.Model) {
				t.Fatalf("UI would reject published model %q", r.Model)
			}
		}
	}
	legacy := call(h, "GET", base, aliceToken, "", "")
	var old modelaccess.Consistency
	if legacy.Code != 200 || json.Unmarshal(legacy.Body.Bytes(), &old) != nil || len(old.Allowlist) != 0 {
		t.Fatalf("legacy no-parameter changed: %d %s", legacy.Code, legacy.Body)
	}
	// Explicit empty is deny-all, despite published rates.
	if _, e = p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) SELECT product_id,3,actor_id,jsonb_set(jsonb_set(snapshot,'{usage,models}','[]'),'{version}','3') FROM product_billing_profiles WHERE product_id='superwagie' AND version=2`); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"prepaid_cash", "credits"} {
		w := call(h, "GET", path(as[0].ID, mode), aliceToken, "", "")
		var out modelaccess.Consistency
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Allowlist) != 0 || len(out.PublishedRates) != 0 || len(out.ExtraModels) != 0 {
			t.Fatalf("deny-all %s: %d %s", mode, w.Code, w.Body)
		}
	}
	if _, e = p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) SELECT product_id,4,actor_id,jsonb_set(jsonb_set(snapshot,'{usage,funding_modes}','["credits"]'),'{version}','4') FROM product_billing_profiles WHERE product_id='superwagie' AND version=1`); e != nil {
		t.Fatal(e)
	}
	if w := call(h, "GET", path(as[0].ID, "prepaid_cash"), aliceToken, "", ""); w.Code != 403 {
		t.Fatalf("disabled funding mode: %d %s", w.Code, w.Body)
	}
	if _, e = p.Exec(ctx, `INSERT INTO product_billing_profiles(product_id,version,actor_id,snapshot) SELECT product_id,5,actor_id,jsonb_set(jsonb_set(jsonb_set(snapshot,'{usage,require_subscription}','true'),'{subscription}', '{"enabled":true}'),'{version}','5') FROM product_billing_profiles WHERE product_id='superwagie' AND version=4`); e != nil {
		t.Fatal(e)
	}
	if w := call(h, "GET", path(as[0].ID, "credits"), aliceToken, "", ""); w.Code != 403 || strings.Contains(w.Body.String(), "shared") {
		t.Fatalf("unsubscribed catalog disclosure: %d %s", w.Code, w.Body)
	}
	var credentials, calls int
	if e = p.QueryRow(ctx, `SELECT (SELECT count(*) FROM model_credentials), (SELECT count(*) FROM billing_calls)`).Scan(&credentials, &calls); e != nil || credentials != 0 || calls != 0 {
		t.Fatalf("read mutated credentials or calls: %d %d %v", credentials, calls, e)
	}
}
