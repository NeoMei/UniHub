package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/platformadmin"
	"unihub/internal/testutil"
	"unihub/internal/wallet"
)

// transferVerifier accepts only console subjects; product-prefixed tokens are
// rejected so authenticated() falls through to the product verifier, which is
// how a real product token gets its originProduct identity.
type transferVerifier struct{ allowed map[string]bool }

func (v transferVerifier) Verify(ctx context.Context, token string) (auth.Claims, error) {
	if !v.allowed[token] {
		return auth.Claims{}, errors.New("invalid token")
	}
	return auth.Claims{Issuer: "https://test", Subject: token, Name: token}, nil
}

type transferProductVerifier struct{}

func (transferProductVerifier) Verify(ctx context.Context, token string) (auth.Claims, error) {
	if strings.HasPrefix(token, "product-") {
		return auth.Claims{Issuer: "https://test", Subject: strings.TrimPrefix(token, "product-"), Name: token}, nil
	}
	return auth.Claims{}, errors.New("not a product token")
}

type transferFixture struct {
	h       http.Handler
	ids     map[string]string
	team    string
	target  string
}

func newTransferFixture(t *testing.T) transferFixture {
	t.Helper()
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	ids := map[string]string{}
	// superadmin, finance, owner, member, stranger each get a personal account.
	for _, name := range []string{"superadmin", "finance", "owner", "member", "stranger"} {
		u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: name, Name: name})
		if e != nil {
			t.Fatal(e)
		}
		ids[name] = u.ID
	}
	admin := platformadmin.New(p, nil)
	if e := admin.Bootstrap(ctx, "fixture", ids["superadmin"], "bootstrap fixture"); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.SetRoles(ctx, ids["superadmin"], ids["finance"], []string{"finance_admin"}, 0, "fixture authority"); e != nil {
		t.Fatal(e)
	}
	team, e := a.CreateTeam(ctx, ids["owner"], "owner team")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'member')`, team.ID, ids["member"]); e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, ids["member"])
	if e != nil || len(as) < 1 {
		t.Fatalf("member accounts: %+v %v", as, e)
	}
	var personal string
	if e = p.QueryRow(ctx, `SELECT id FROM accounts WHERE kind='personal' AND personal_user_id=$1`, ids["member"]).Scan(&personal); e != nil {
		t.Fatal(e)
	}
	v := transferVerifier{allowed: map[string]bool{"superadmin": true, "finance": true, "owner": true, "member": true, "stranger": true}}
	w := wallet.New(p, a, noLedger{})
	h := New(v, a, w, Options{
		PlatformAdmin:    admin,
		ProductVerifiers: map[string]auth.Verifier{"sw": transferProductVerifier{}},
	})
	return transferFixture{h: h, ids: ids, team: team.ID, target: personal}
}

func (f transferFixture) post(method, path, token, body, key string) int {
	w := call(f.h, method, path, token, body, key)
	return w.Code
}

// super_admin may transfer to any account; finance_admin (only finance:write)
// does NOT hold admins:manage; an ordinary user and a product token are refused.
func TestAdminTransferRouteSuperAdminOnly(t *testing.T) {
	f := newTransferFixture(t)
	body := `{"account_id":"` + f.target + `","product_id":"","amount":"1000000"}`
	if got := f.post("POST", "/admin/v1/transfers", "superadmin", body, "at-1"); got != http.StatusAccepted {
		t.Fatalf("super_admin: %d want 202", got)
	}
	for _, token := range []string{"finance", "stranger", "product-superadmin"} {
		if got := f.post("POST", "/admin/v1/transfers", token, body, "at-"+token); got != http.StatusForbidden {
			t.Fatalf("%s: %d want 403", token, got)
		}
	}
	// Missing idempotency key is a bad request even for a super_admin.
	if got := f.post("POST", "/admin/v1/transfers", "superadmin", body, ""); got != http.StatusBadRequest {
		t.Fatalf("missing key: %d want 400", got)
	}
}

// A team owner may transfer to one of its own members; a mere member, a
// stranger, or a product token may not. The destination is the member's own
// personal account, never the team's shared wallet.
func TestMemberTransferRouteOwnerToOwnMemberOnly(t *testing.T) {
	f := newTransferFixture(t)
	path := "/v1/accounts/" + f.team + "/member-transfers"
	body := `{"user_id":"` + f.ids["member"] + `","product_id":"","amount":"1000000"}`
	if got := f.post("POST", path, "owner", body, "mt-1"); got != http.StatusAccepted {
		t.Fatalf("team owner to own member: %d want 202", got)
	}
	// A member of the team is not an owner.
	if got := f.post("POST", path, "member", `{"user_id":"`+f.ids["member"]+`","amount":"1000000"}`, "mt-2"); got != http.StatusForbidden {
		t.Fatalf("member acting as owner: %d want 403", got)
	}
	// A user who is not on the team at all.
	if got := f.post("POST", path, "stranger", body, "mt-3"); got != http.StatusForbidden {
		t.Fatalf("stranger: %d want 403", got)
	}
	// Target user is not a member of this team.
	notMember := `{"user_id":"` + f.ids["stranger"] + `","amount":"1000000"}`
	if got := f.post("POST", path, "owner", notMember, "mt-4"); got != http.StatusForbidden {
		t.Fatalf("non-member target: %d want 403", got)
	}
	// Product tokens are refused at the route scope, before the action.
	if got := f.post("POST", path, "product-owner", body, "mt-5"); got != http.StatusForbidden {
		t.Fatalf("product token: %d want 403", got)
	}
	// A personal account is not a team; using it as {account} is forbidden.
	personalPath := "/v1/accounts/" + f.target + "/member-transfers"
	if got := f.post("POST", personalPath, "owner", `{"user_id":"`+f.ids["member"]+`","amount":"1000000"}`, "mt-6"); got != http.StatusForbidden {
		t.Fatalf("personal account as team: %d want 403", got)
	}
	// Whole-cent amounts only; a sub-cent amount is rejected.
	if got := f.post("POST", path, "owner", `{"user_id":"`+f.ids["member"]+`","amount":"1"}`, "mt-7"); got != http.StatusBadRequest {
		t.Fatalf("sub-cent amount: %d want 400", got)
	}
}
