package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/identityapi"
	"unihub/internal/ledger"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

type profileFixture struct {
	profile identityProfile
	revoked bool
	avatar  []byte
}

type identityProfile = identityapi.Profile

func (f *profileFixture) Profile(context.Context, string) (identityProfile, error) {
	return f.profile, nil
}
func (f *profileFixture) UpdateDisplayName(_ context.Context, _ string, name string) (identityProfile, error) {
	f.profile.DisplayName = name
	f.profile.Version = "2"
	return f.profile, nil
}
func (f *profileFixture) ChangePassword(_ context.Context, user, current, next string) error {
	if user == "" || current != "old" || next != "new" {
		return accounts.ErrInvalid
	}
	f.revoked = true
	return nil
}
func (f *profileFixture) SessionAllowed(_ context.Context, _ string) bool        { return !f.revoked }
func (f *profileFixture) BrowserSessionAllowed(_ context.Context, _ string) bool { return !f.revoked }
func (f *profileFixture) PutAvatar(_ context.Context, _ string, _ string, data []byte) (identityProfile, error) {
	f.avatar = bytes.Clone(data)
	f.profile.AvatarURL = "/v1/me/avatar"
	return f.profile, nil
}
func (f *profileFixture) Avatar(context.Context, string) (string, []byte, error) {
	return "image/png", f.avatar, nil
}

type verifier struct{}

func (verifier) Verify(ctx context.Context, s string) (auth.Claims, error) {
	if s != "alice" && s != "bob" {
		return auth.Claims{}, errors.New("invalid token")
	}
	return auth.Claims{Issuer: "https://test", Subject: s, Name: s}, nil
}

type noLedger struct{}

func (noLedger) Post(context.Context, ledger.Command) (ledger.Transaction, error) {
	return ledger.Transaction{}, ledger.ErrUnknown
}
func (noLedger) Find(context.Context, string) (*ledger.Transaction, error) {
	return nil, ledger.ErrUnknown
}
func (noLedger) Balance(context.Context, string, string) (int64, error) { return 0, nil }
func setup(t *testing.T) http.Handler {
	p := testutil.Database(t)
	if e := database.Migrate(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	return New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Sandbox: true, Merchant: "sandbox", SigningKey: []byte("this-local-test-signing-key-is-at-least-32-bytes")})
}
func call(h http.Handler, method, path, token, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAPIRejectsMissingAuthAndCrossAccountAccess(t *testing.T) {
	h := setup(t)
	if w := call(h, "GET", "/v1/accounts", "", "", ""); w.Code != 401 {
		t.Fatalf("missing auth %d", w.Code)
	}
	w := call(h, "GET", "/v1/accounts", "alice", "", "")
	if w.Code != 200 {
		t.Fatalf("accounts: %d %s", w.Code, w.Body)
	}
	var as []accounts.Account
	if e := json.Unmarshal(w.Body.Bytes(), &as); e != nil || len(as) != 1 {
		t.Fatalf("accounts %s", w.Body)
	}
	if w := call(h, "GET", "/v1/accounts/"+as[0].ID+"/wallets", "bob", "", ""); w.Code != 403 {
		t.Fatalf("cross-account: %d", w.Code)
	}
	path := "/v1/accounts/" + as[0].ID + "/recharges"
	for _, b := range []string{`{"amount":"1000000","extra":"bad"}`, `{"amount":1000000}`, `{"amount":"1.5"}`} {
		if w := call(h, "POST", path, "alice", b, "key"); w.Code != 400 {
			t.Fatalf("bad body %s => %d", b, w.Code)
		}
	}
	if w := call(h, "POST", path, "alice", `{"amount":"1000000"}`, ""); w.Code != 400 {
		t.Fatalf("missing key %d", w.Code)
	}
	if w := call(h, "POST", path, "alice", `{"amount":"1000000"}`, "key"); w.Code != 201 {
		t.Fatalf("order: %d %s", w.Code, w.Body)
	}
}

func TestProfileContractAndPasswordRevokesSession(t *testing.T) {
	p := testutil.Database(t)
	if e := database.Migrate(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	f := &profileFixture{profile: identityProfile{ID: "alice", Username: "alice", DisplayName: "Alice", AvatarURL: "https://avatar.test/a.png", Email: "alice@example.test", Version: "1"}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Identity: f})
	w := call(h, "GET", "/v1/me/profile", "alice", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"username":"alice"`) {
		t.Fatalf("profile: %d %s", w.Code, w.Body)
	}
	w = call(h, "PATCH", "/v1/me/profile", "alice", `{"display_name":"New name"}`, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"display_name":"New name"`) {
		t.Fatalf("display name: %d %s", w.Code, w.Body)
	}
	w = call(h, "PATCH", "/v1/me/profile", "alice", `{"username":"changed"}`, "")
	if w.Code != 400 {
		t.Fatalf("username mutation: %d %s", w.Code, w.Body)
	}
	w = call(h, "POST", "/v1/me/password", "alice", `{"current_password":"old","new_password":"new"}`, "")
	if w.Code != 204 {
		t.Fatalf("password: %d %s", w.Code, w.Body)
	}
	w = call(h, "GET", "/v1/me/profile", "alice", "", "")
	if w.Code != 401 {
		t.Fatalf("revoked session: %d %s", w.Code, w.Body)
	}
}

func TestPendingPasswordMutationBlocksTrustedBrowserClaims(t *testing.T) {
	p := testutil.Database(t)
	if err := database.Migrate(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	f := &profileFixture{profile: identityProfile{Username: "alice"}, revoked: true}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), Options{Identity: f})
	r := httptest.NewRequest("GET", "/v1/me/profile", nil)
	r = r.WithContext(auth.WithBrowserClaims(r.Context(), auth.Claims{Issuer: "issuer", Subject: "alice"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("pending mutation browser claims: %d", w.Code)
	}
}

func TestProfileProductScopesAndBoundedAvatar(t *testing.T) {
	p := testutil.Database(t)
	if err := database.Migrate(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	f := &profileFixture{profile: identityProfile{Username: "alice", DisplayName: "Alice", Version: "1"}}
	options := Options{Identity: f, ProductVerifiers: map[string]auth.Verifier{"client": productVerifier{}}, ProductAccess: map[string]products.Client{"client": {ProductID: "superwagie", ClientID: "client", Scopes: []string{"accounts:read"}}}}
	h := New(verifier{}, a, wallet.New(p, a, noLedger{}), options)
	if w := call(h, "GET", "/v1/me/profile", "product-alice", "", ""); w.Code != 200 {
		t.Fatalf("profile read %d %s", w.Code, w.Body)
	}
	if w := call(h, "PATCH", "/v1/me/profile", "product-alice", `{"display_name":"Changed"}`, ""); w.Code != 403 {
		t.Fatalf("write scope missing %d", w.Code)
	}
	client := options.ProductAccess["client"]
	client.Scopes = append(client.Scopes, "profile:write")
	options.ProductAccess["client"] = client
	h = New(verifier{}, a, wallet.New(p, a, noLedger{}), options)
	if w := call(h, "PATCH", "/v1/me/profile", "product-alice", `{"display_name":"Changed","is_admin":true}`, ""); w.Code != 400 {
		t.Fatalf("field injection %d", w.Code)
	}
	var imageData bytes.Buffer
	png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	body, _ := json.Marshal(map[string]string{"content_type": "image/png", "data_base64": base64.StdEncoding.EncodeToString(imageData.Bytes())})
	if w := call(h, "PUT", "/v1/me/avatar", "product-alice", string(body), ""); w.Code != 200 {
		t.Fatalf("avatar write %d %s", w.Code, w.Body)
	}
	w := call(h, "GET", "/v1/me/avatar", "product-alice", "", "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), imageData.Bytes()) || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("avatar read %d", w.Code)
	}
	if w = call(h, "GET", "/v1/me/avatar", "", "", ""); w.Code != 401 {
		t.Fatalf("anonymous avatar %d", w.Code)
	}
	if w = call(h, "PUT", "/v1/me/avatar", "product-alice", `{"content_type":"image/png","data_base64":"`+strings.Repeat("A", 1<<20)+`"}`, ""); w.Code != 413 {
		t.Fatalf("oversized avatar %d %s", w.Code, w.Body)
	}
}

func TestSandboxSettlementRequiresCurrentFinanceRole(t *testing.T) {
	for _, role := range []string{"member", "admin", "billing_admin"} {
		t.Run(role, func(t *testing.T) {
			p := testutil.Database(t)
			ctx := context.Background()
			if err := database.Migrate(ctx, p); err != nil {
				t.Fatal(err)
			}
			a := accounts.New(p)
			owner, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
			if err != nil {
				t.Fatal(err)
			}
			member, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
			if err != nil {
				t.Fatal(err)
			}
			team, err := a.CreateTeam(ctx, owner.ID, "Team")
			if err != nil {
				t.Fatal(err)
			}
			inv, err := a.Invite(ctx, owner.ID, team.ID, member.ID, role)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.AcceptInvitation(ctx, member.ID, inv.ID); err != nil {
				t.Fatal(err)
			}
			service := wallet.New(p, a, noLedger{})
			order, err := service.CreateRecharge(ctx, owner.ID, team.ID, "", "1000000", "order-1", "sandbox")
			if err != nil {
				t.Fatal(err)
			}
			h := New(verifier{}, a, service, Options{Sandbox: true, Merchant: "sandbox", SigningKey: []byte("this-local-test-signing-key-is-at-least-32-bytes")})
			response := call(h, "POST", "/v1/accounts/"+team.ID+"/recharges/"+order.ID+"/sandbox-settle", "bob", "", "")
			want := 403
			if role == "billing_admin" {
				want = 202
			}
			if response.Code != want {
				t.Fatalf("role %s settlement: %d %s; want %d", role, response.Code, response.Body, want)
			}
			var receipts int
			if err = p.QueryRow(ctx, `SELECT count(*) FROM payment_receipts WHERE order_id=$1`, order.ID).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if (role == "billing_admin" && receipts != 1) || (role != "billing_admin" && receipts != 0) {
				t.Fatalf("role %s persisted %d receipts", role, receipts)
			}
		})
	}
}

func TestOperationLookupScopesRecordAndAccount(t *testing.T) {
	p := testutil.Database(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	alice, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	aliceAccounts, err := a.ListAccounts(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	bobAccounts, err := a.ListAccounts(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := wallet.New(p, a, noLedger{})
	order, err := service.CreateRecharge(ctx, alice.ID, aliceAccounts[0].ID, "", "1000000", "private-order", "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	op, err := service.Receive(ctx, wallet.Receipt{Merchant: "sandbox", OrderID: order.ID, TransactionID: "payment-1", Amount: order.Amount, Currency: order.Currency, Status: "SUCCEEDED"})
	if err != nil {
		t.Fatal(err)
	}
	h := New(verifier{}, a, service, Options{})
	for _, tc := range []struct {
		account, actor string
		want           int
	}{
		{aliceAccounts[0].ID, "alice", 200},
		{aliceAccounts[0].ID, "bob", 403},
		{bobAccounts[0].ID, "bob", 404},
	} {
		response := call(h, "GET", "/v1/accounts/"+tc.account+"/operations/"+op.ID, tc.actor, "", "")
		if response.Code != tc.want {
			t.Fatalf("actor=%s path-account=%s: %d %s; want %d", tc.actor, tc.account, response.Code, response.Body, tc.want)
		}
		if tc.want != 200 && strings.Contains(response.Body.String(), op.ID) {
			t.Fatal("denial leaked operation identity")
		}
	}
}

func TestDisabledSandboxExposesNeitherSettlementNorWebhook(t *testing.T) {
	h := New(verifier{}, nil, nil, Options{Sandbox: false})
	for _, path := range []string{"/webhooks/sandbox", "/v1/accounts/account/recharges/order/sandbox-settle"} {
		response := call(h, "POST", path, "alice", `{}`, "")
		if response.Code != 404 {
			t.Fatalf("disabled sandbox route %s returned %d", path, response.Code)
		}
	}
}
