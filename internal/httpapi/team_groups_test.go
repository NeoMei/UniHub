package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/auth"
	"unihub/internal/database"
	"unihub/internal/products"
	"unihub/internal/testutil"
	"unihub/internal/wallet"

	"github.com/stretchr/testify/require"
)

func TestTeamGroupProductScopeSeparation(t *testing.T) {
	const groups = "/v1/accounts/{account}/groups"
	for _, tc := range []struct {
		pattern, scope string
		allowed        bool
	}{
		{"GET " + groups, "groups:read", true},
		{"GET " + groups + "/memberships", "groups:members:read", true},
		{"GET " + groups, "accounts:read", false},
		{"GET " + groups, "members:read", false},
		{"GET " + groups + "/memberships", "groups:read", false},
		{"GET " + groups + "/memberships", "members:read", false},
		{"GET " + groups, "groups:members:read", false},
		{"POST " + groups, "members:write", false},
		{"PUT " + groups + "/{group}/members/{member}", "groups:members:read", false},
		{"DELETE " + groups + "/{group}", "groups:read", false},
	} {
		client := products.Client{Scopes: []string{tc.scope}}
		require.Equal(t, tc.allowed, productScopeAllowed(tc.pattern, client), "%s with %s", tc.pattern, tc.scope)
		require.False(t, productRouteAllowed(tc.pattern), "legacy product access to %s", tc.pattern)
	}
}

func TestTeamGroupRoutesKeepTeamAsTheAuthorizationBoundary(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	require.NoError(t, database.Migrate(ctx, pool))
	store := accounts.New(pool)
	alice, err := store.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice", Name: "alice"})
	require.NoError(t, err)
	bob, err := store.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "bob", Name: "bob"})
	require.NoError(t, err)
	team, err := store.CreateTeam(ctx, alice.ID, "Engineering")
	require.NoError(t, err)
	otherTeam, err := store.CreateTeam(ctx, alice.ID, "Research")
	require.NoError(t, err)
	personal, err := store.ListAccounts(ctx, bob.ID)
	require.NoError(t, err)
	require.Len(t, personal, 1)
	policy := map[string]products.Client{"client": {ProductID: "notes", ClientID: "client", Origins: []string{"https://notes.test"}, Scopes: []string{"groups:read", "members:write"}}}
	h := New(verifier{}, store, wallet.New(pool, store, noLedger{}), Options{
		ProductVerifiers: map[string]auth.Verifier{"client": productVerifier{}},
		ProductAccess:    policy,
	})
	teamPath := "/v1/accounts/" + team.ID
	groupsPath := teamPath + "/groups"
	request := func(method, path, token, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := call(h, method, path, token, body, "")
		require.Equal(t, want, w.Code, "%s %s: %s", method, path, w.Body)
		return w
	}

	empty := request("GET", groupsPath, "alice", "", 200)
	require.JSONEq(t, `{"groups":[],"group_ids":[]}`, empty.Body.String())
	request("GET", groupsPath, "bob", "", 403)
	request("GET", "/v1/accounts/"+personal[0].ID+"/groups", "bob", "", 403)
	request("GET", groupsPath, "", "", 401)
	request("POST", groupsPath, "alice", `{"name":"Support","role":"owner"}`, 400)
	request("POST", groupsPath, "product-alice", `{"name":"Support"}`, 403)
	request("GET", groupsPath+"/memberships", "product-alice", "", 403)
	request("GET", groupsPath, "product-alice", "", 200)
	badOrigin := httptest.NewRequest(http.MethodGet, groupsPath, nil)
	badOrigin.Header.Set("Authorization", "Bearer product-alice")
	badOrigin.Header.Set("Origin", "https://other.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, badOrigin)
	require.Equal(t, 403, w.Code)

	create := func(path, name string) string {
		t.Helper()
		body, err := json.Marshal(map[string]string{"name": name})
		require.NoError(t, err)
		response := request("POST", path, "alice", string(body), 201)
		var group struct{ ID, Name string }
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &group))
		require.NotEmpty(t, group.ID)
		require.Equal(t, strings.TrimSpace(name), group.Name)
		return group.ID
	}
	one := create(groupsPath, " Platform ")
	two := create(groupsPath, "Support")
	foreign := create("/v1/accounts/"+otherTeam.ID+"/groups", "Platform")
	request("POST", groupsPath, "alice", `{"name":"pLaTfOrM"}`, 409)
	request("PUT", groupsPath+"/"+one+"/members/"+bob.ID, "alice", "", 404)

	invitation, err := store.Invite(ctx, alice.ID, team.ID, bob.ID, "member")
	require.NoError(t, err)
	_, err = store.AcceptInvitation(ctx, bob.ID, invitation.ID)
	require.NoError(t, err)
	memberPath := func(group string) string { return groupsPath + "/" + group + "/members/" + bob.ID }
	request("PUT", memberPath(foreign), "alice", "", 404)
	request("PUT", memberPath(one), "bob", "", 403)
	request("PUT", memberPath(one), "alice", "", 204)
	request("PUT", memberPath(one), "alice", "", 204)
	request("PUT", memberPath(two), "alice", "", 204)
	request("PATCH", groupsPath+"/"+two, "alice", `{"name":"Enablement"}`, 200)
	response := request("GET", groupsPath, "bob", "", 200)
	var directory struct {
		Groups   []struct{ ID, Name string } `json:"groups"`
		GroupIDs []string                    `json:"group_ids"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &directory))
	require.ElementsMatch(t, []string{one, two}, directory.GroupIDs)
	require.Len(t, directory.Groups, 2)
	require.Equal(t, "Enablement", directory.Groups[0].Name)
	role, err := store.Role(ctx, bob.ID, team.ID)
	require.NoError(t, err)
	require.Equal(t, "member", role)
	request("GET", teamPath+"/wallets", "bob", "", 403)
	request("GET", "/v1/accounts/"+one+"/wallets", "alice", "", 403)

	policy["client"] = products.Client{ProductID: "notes", ClientID: "client", Scopes: []string{"groups:members:read"}}
	response = request("GET", groupsPath+"/memberships", "product-alice", "", 200)
	var memberships []struct {
		UserID   string   `json:"user_id"`
		GroupIDs []string `json:"group_ids"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &memberships))
	require.Len(t, memberships, 2)
	for _, membership := range memberships {
		if membership.UserID == bob.ID {
			require.ElementsMatch(t, []string{one, two}, membership.GroupIDs)
		} else {
			require.Equal(t, alice.ID, membership.UserID)
			require.Equal(t, []string{}, membership.GroupIDs)
		}
	}
	request("DELETE", memberPath(one), "alice", "", 204)
	request("DELETE", memberPath(one), "alice", "", 204)
	request("DELETE", groupsPath+"/"+two, "alice", "", 204)
	response = request("GET", groupsPath, "bob", "", 200)
	require.JSONEq(t, `{"groups":[{"id":"`+one+`","name":"Platform"}],"group_ids":[]}`, response.Body.String())
	request("DELETE", teamPath+"/members/"+bob.ID, "alice", "", 204)
	request("GET", groupsPath+"/memberships", "bob", "", 403)
}
