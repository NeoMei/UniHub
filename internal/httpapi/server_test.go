package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

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
