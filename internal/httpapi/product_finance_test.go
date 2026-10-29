package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/ledger"
	"unihub/internal/productauth"
	"unihub/internal/products"
	"unihub/internal/productusers"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

func TestProductFinanceRoutesRequireBothCredentialsAndStayFinite(t *testing.T) {
	s := &api{options: Options{ProductRuntime: map[string]RuntimeService{
		"lr":          {Token: strings.Repeat("s", 40), ProductID: "llmrouter", InstanceID: "default", FinanceClientID: "lr-client", AllowedOps: []string{"finance:read", "finance:write"}},
		"lr-readonly": {Token: strings.Repeat("r", 40), ProductID: "llmrouter", InstanceID: "default", FinanceClientID: "lr-client", AllowedOps: []string{"finance:read"}},
	}}}
	actions := map[string]action{}
	for _, pattern := range productFinanceRoutes {
		actions[pattern] = func(w http.ResponseWriter, _ *http.Request, u accounts.User) error {
			return respond(w, http.StatusOK, map[string]string{"user": u.ID})
		}
	}
	mux := http.NewServeMux()
	s.productFinanceRoutes(mux, actions)
	call := func(method, path, service, actor, origin string, duplicateActor, duplicateService bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if service != "" {
			r.Header.Add("Authorization", "Bearer "+service)
		}
		if duplicateService {
			r.Header.Add("Authorization", "Bearer "+service)
		}
		if actor != "" {
			r.Header.Add("X-UniHub-Actor-Authorization", "Bearer "+actor)
		}
		if duplicateActor {
			r.Header.Add("X-UniHub-Actor-Authorization", "Bearer upa_second")
		}
		if origin != "" {
			r.Header.Add("Origin", origin)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	validService := strings.Repeat("s", 40)
	readOnlyService := strings.Repeat("r", 40)
	for _, tc := range []struct {
		name, method, path, service, actor, origin string
		duplicate, duplicateService                bool
		want                                       int
	}{
		{"service only", "GET", "/internal/v1/product-finance/accounts", validService, "", "", false, false, 401},
		{"actor only", "GET", "/internal/v1/product-finance/accounts", "", "upa_actor", "", false, false, 401},
		{"duplicate actor", "GET", "/internal/v1/product-finance/accounts", validService, "upa_actor", "", true, false, 401},
		{"duplicate service", "GET", "/internal/v1/product-finance/accounts", validService, "upa_actor", "", false, true, 401},
		{"origin", "GET", "/internal/v1/product-finance/accounts", validService, "upa_actor", "https://product.test", false, false, 403},
		{"read-only service may read", "GET", "/internal/v1/product-finance/accounts", readOnlyService, "upa_actor", "", false, false, 401},
		{"method outside route", "PATCH", "/internal/v1/product-finance/accounts", validService, "upa_actor", "", false, false, 405},
		{"read-only service cannot write", "POST", "/internal/v1/product-finance/accounts/acct-1/recharges", readOnlyService, "upa_actor", "", false, false, 403},
		{"unknown path", "GET", "/internal/v1/product-finance/admin/users", validService, "upa_actor", "", false, false, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := call(tc.method, tc.path, tc.service, tc.actor, tc.origin, tc.duplicate, tc.duplicateService); w.Code != tc.want {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body, tc.want)
			}
		})
	}
}

func TestProductFinanceSandboxLedgerReplayAndFreshActorIsolation(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	a := accounts.New(p)
	reg := productusers.Registry{"llmrouter/default": {ProductID: "llmrouter", InstanceID: "default", Version: 1, DefaultRoles: []string{"member"}, Roles: []productusers.Role{{ID: "member", Name: "Member", Permissions: []string{"product:use"}}}}}
	users := productusers.New(p, reg, "https://hub.test", nil)
	pa := productauth.New(p, a, users, "https://hub.test")
	token := strings.Repeat("s", 40)
	policy := products.Client{ClientID: "lr-finance", ProductID: "llmrouter", InstanceID: "default", Scopes: []string{"accounts:read"}}
	services := map[string]RuntimeService{
		"lr":    {Token: token, ProductID: "llmrouter", InstanceID: "default", FinanceClientID: policy.ClientID, AllowedOps: []string{"finance:read", "finance:write"}},
		"other": {Token: strings.Repeat("o", 40), ProductID: "other", InstanceID: "default", FinanceClientID: "other-client", AllowedOps: []string{"finance:read", "finance:write"}},
	}
	otherPolicy := products.Client{ClientID: "other-client", ProductID: "other", InstanceID: "default", Scopes: []string{"accounts:read"}}
	walletPolicy := products.Client{ClientID: "lr-finance-wallet", ProductID: "llmrouter", InstanceID: "default", Scopes: []string{"accounts:read", "wallets:read"}}
	rechargePolicy := products.Client{ClientID: "lr-finance-recharge", ProductID: "llmrouter", InstanceID: "default", Scopes: []string{"accounts:read", "recharges:read", "recharges:write"}}
	services["lr-wallet"] = RuntimeService{Token: strings.Repeat("w", 40), ProductID: "llmrouter", InstanceID: "default", FinanceClientID: walletPolicy.ClientID, AllowedOps: []string{"finance:read", "finance:write"}}
	services["lr-recharge"] = RuntimeService{Token: strings.Repeat("q", 40), ProductID: "llmrouter", InstanceID: "default", FinanceClientID: rechargePolicy.ClientID, AllowedOps: []string{"finance:read", "finance:write"}}
	var formance *ledger.Formance
	if base := os.Getenv("UNIHUB_TEST_LEDGER_URL"); base != "" {
		formance = ledger.NewFormance(base, fmt.Sprintf("product-finance-%d", time.Now().UnixNano()), &http.Client{Timeout: 10 * time.Second})
		if err := formance.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	walletService := wallet.New(p, a, formance)
	h := New(verifier{}, a, walletService, Options{Sandbox: true, Merchant: "finance-test-sandbox", SigningKey: []byte(strings.Repeat("k", 40)), ProductUsers: users, ProductAuth: pa, ProductAccess: map[string]products.Client{policy.ClientID: policy, otherPolicy.ClientID: otherPolicy, walletPolicy.ClientID: walletPolicy, rechargePolicy.ClientID: rechargePolicy}, ProductRuntime: services})
	actorFor := func(local string, version int64) productauth.Identity {
		t.Helper()
		registered, err := pa.Register(ctx, "llmrouter", "default", productauth.Registration{LocalUserID: local, OperationID: "registration-" + local, Username: local, Email: "shared@example.test", AuthVersion: version})
		if err != nil {
			t.Fatal(err)
		}
		actor, err := pa.Authenticate(ctx, "llmrouter", "default", productauth.Authentication{LocalUserID: local, AuthenticationID: "authentication-" + local, Method: "session", AuthenticatedAt: time.Now().Unix(), AuthVersion: registered.AuthVersion})
		if err != nil {
			t.Fatal(err)
		}
		return actor
	}
	first := actorFor("native-a", 1)
	second := actorFor("native-b", 1)
	if first.Binding.UserID == second.Binding.UserID {
		t.Fatal("same-email users were merged")
	}
	request := func(actor string, duplicate, origin bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/internal/v1/product-finance/accounts", nil)
		r.Header.Add("Authorization", "Bearer "+token)
		r.Header.Add("X-UniHub-Actor-Authorization", "Bearer "+actor)
		if duplicate {
			r.Header.Add("X-UniHub-Actor-Authorization", "Bearer "+actor)
		}
		if origin {
			r.Header.Set("Origin", "https://product.test")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	var firstAccounts []accounts.Account
	w := request(first.ActorToken, false, false)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &firstAccounts) != nil || len(firstAccounts) != 1 || firstAccounts[0].ID != first.Binding.PersonalAccountID {
		t.Fatalf("delegated actor received wrong accounts: %d %s", w.Code, w.Body)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(first.ActorToken)) || bytes.Contains(w.Body.Bytes(), []byte(token)) {
		t.Fatal("credentials leaked in response")
	}
	create := httptest.NewRequest("POST", "/internal/v1/product-finance/accounts/"+first.Binding.PersonalAccountID+"/recharges", bytes.NewBufferString(`{"product_id":"llmrouter","amount":"1000000"}`))
	create.Header.Set("Authorization", "Bearer "+services["lr-recharge"].Token)
	create.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+first.ActorToken)
	create.Header.Set("Idempotency-Key", "finance-recharge-1")
	created := httptest.NewRecorder()
	h.ServeHTTP(created, create)
	var order wallet.Order
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &order) != nil {
		t.Fatalf("create sandbox order: %d %s", created.Code, created.Body)
	}
	settle := func() (*httptest.ResponseRecorder, wallet.Operation) {
		r := httptest.NewRequest("POST", "/internal/v1/product-finance/accounts/"+first.Binding.PersonalAccountID+"/recharges/"+order.ID+"/sandbox-settle", nil)
		r.Header.Set("Authorization", "Bearer "+services["lr-recharge"].Token)
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+first.ActorToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var op wallet.Operation
		if w.Code == http.StatusAccepted {
			_ = json.Unmarshal(w.Body.Bytes(), &op)
		}
		return w, op
	}
	settled, op1 := settle()
	replayed, op2 := settle()
	if settled.Code != http.StatusAccepted || replayed.Code != http.StatusAccepted || op1.ID == "" || op1.ID != op2.ID {
		t.Fatalf("sandbox settlement replay diverged: %d %s / %d %s", settled.Code, settled.Body, replayed.Code, replayed.Body)
	}
	var operations int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM wallet_operations WHERE account_id=$1 AND kind='recharge'`, first.Binding.PersonalAccountID).Scan(&operations); err != nil || operations != 1 {
		t.Fatalf("replay duplicated ledger operation count=%d err=%v", operations, err)
	}
	if formance != nil {
		if err := walletService.Process(ctx, op1.ID); err != nil {
			t.Fatal(err)
		}
		applied, err := walletService.GetOperation(ctx, first.Binding.UserID, first.Binding.PersonalAccountID, op1.ID)
		if err != nil || applied.Status != "APPLIED" {
			t.Fatalf("sandbox charge status=%+v err=%v", applied, err)
		}
		balance, err := formance.Balance(ctx, wallet.Address(first.Binding.PersonalAccountID, ""), wallet.Asset)
		if err != nil || balance != 1_000_000 {
			t.Fatalf("sandbox replay balance=%d err=%v, want one 1000000-unit credit", balance, err)
		}
	}
	if w = request(first.ActorToken, true, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate actor header status=%d", w.Code)
	}
	if w = request(first.ActorToken, false, true); w.Code != http.StatusForbidden {
		t.Fatalf("Origin status=%d", w.Code)
	}
	requestPath := func(path, service, actor string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+service)
		r.Header.Set("X-UniHub-Actor-Authorization", "Bearer "+actor)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w = requestPath("/internal/v1/product-finance/accounts/"+first.Binding.PersonalAccountID+"/wallets", token, second.ActorToken); w.Code != http.StatusForbidden {
		t.Fatalf("client missing wallet scope status=%d", w.Code)
	}
	if w = requestPath("/internal/v1/product-finance/accounts", services["other"].Token, first.ActorToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong product service accepted actor: %d %s", w.Code, w.Body)
	}
	if w = requestPath("/internal/v1/product-finance/accounts/"+first.Binding.PersonalAccountID+"/wallets", services["lr-wallet"].Token, second.ActorToken); w.Code != http.StatusForbidden {
		t.Fatalf("same-product nonmember accessed unrelated wallet: %d %s", w.Code, w.Body)
	}
	if _, err := p.Exec(ctx, `UPDATE product_auth_assertions a SET expires_at=now()-interval '1 second' FROM product_runtime_bindings b WHERE a.binding_id=b.id AND b.product_id='llmrouter' AND b.instance_id='default' AND b.local_user_id='native-a'`); err != nil {
		t.Fatal(err)
	}
	if w = request(first.ActorToken, false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired actor status=%d", w.Code)
	}
	fresh, err := pa.Authenticate(ctx, "llmrouter", "default", productauth.Authentication{LocalUserID: "native-a", AuthenticationID: "authentication-version-a", Method: "session", AuthenticatedAt: time.Now().Unix(), AuthVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.SyncLifecycle(ctx, "llmrouter", "default", productauth.Lifecycle{LocalUserID: "native-a", OperationID: "revoke-auth-a", PreviousAuthVersion: 1, NextAuthVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if w = request(fresh.ActorToken, false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked actor status=%d body=%s", w.Code, w.Body)
	}
	w = request(second.ActorToken, false, false)
	var secondAccounts []accounts.Account
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &secondAccounts) != nil || len(secondAccounts) != 1 || secondAccounts[0].ID != second.Binding.PersonalAccountID || secondAccounts[0].ID == firstAccounts[0].ID {
		t.Fatalf("other same-email user crossed account boundary: %d %s", w.Code, w.Body)
	}
	if _, err := p.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, second.Binding.UserID); err != nil {
		t.Fatal(err)
	}
	if w = request(second.ActorToken, false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("globally disabled actor status=%d", w.Code)
	}
}
