package modelaccess

import (
	"context"
	"strings"
	"testing"
	"time"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

type catalog struct{ until time.Time }

func (c *catalog) Subscriptions(context.Context, string, string) ([]billing.Subscription, error) {
	return []billing.Subscription{{ProductID: "superwagie", Status: "active", PaidThrough: c.until}}, nil
}
func (c *catalog) Rates(context.Context) ([]billing.Rate, error) {
	return []billing.Rate{{ID: "test-rate", Model: "test-model", Status: "published"}}, nil
}
func TestCredentialIsScopedRevocableAndDoesNotPersistBearer(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "alice"})
	if e != nil {
		t.Fatal(e)
	}
	as, e := a.ListAccounts(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	c := &catalog{time.Now().Add(time.Hour)}
	s := New(p, a, c)
	grant, e := s.Issue(ctx, u.ID, as[0].ID, Request{OriginProductID: "superwagie", Models: []string{"test-model"}, TaskID: "task-1"})
	if e != nil {
		t.Fatal(e)
	}
	if grant.Audience != "llmrouter" || grant.Token == "" || grant.ActorID != u.ID || grant.ExpiresAt.After(time.Now().Add(10*time.Minute)) {
		t.Fatalf("wrong grant: %+v", grant.Claims)
	}
	var hash string
	if e = p.QueryRow(ctx, `SELECT token_hash FROM model_credentials WHERE id=$1`, grant.ID).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(hash, grant.Token) || len(hash) != 64 {
		t.Fatal("bearer not securely digested")
	}
	verified, e := s.Verify(ctx, grant.Token, "test-model")
	if e != nil || verified.AccountID != as[0].ID {
		t.Fatalf("verify: %+v %v", verified, e)
	}
	if _, e = s.Verify(ctx, grant.Token, "other-model"); e == nil {
		t.Fatal("credential escaped model scope")
	}
	if _, e = s.Verify(ctx, "Bearer "+grant.Token, "test-model"); e == nil {
		t.Fatal("malformed token accepted")
	}
	if e = s.Revoke(ctx, u.ID, as[0].ID, grant.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, grant.Token, "test-model"); e == nil {
		t.Fatal("revocation ignored")
	}
	if _, e = s.Issue(ctx, u.ID, as[0].ID, Request{OriginProductID: "agentwiki", Models: []string{"test-model"}, TaskID: "task-2"}); e == nil {
		t.Fatal("membership not required")
	}
}
func TestCredentialCannotCrossAccountAndMembershipOrTimeRevocation(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	u, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "owner"})
	v, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "https://test", Subject: "member"})
	team, e := a.CreateTeam(ctx, u.ID, "Team")
	if e != nil {
		t.Fatal(e)
	}
	c := &catalog{time.Now().Add(time.Hour)}
	s := New(p, a, c)
	req := Request{OriginProductID: "superwagie", Models: []string{"test-model"}, TaskID: "task"}
	if _, e = s.Issue(ctx, v.ID, team.ID, req); e == nil {
		t.Fatal("cross-account issue")
	}
	inv, e := a.Invite(ctx, u.ID, team.ID, v.ID, "member")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.AcceptInvitation(ctx, v.ID, inv.ID); e != nil {
		t.Fatal(e)
	}
	grant, e := s.Issue(ctx, v.ID, team.ID, req)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.RemoveMember(ctx, u.ID, team.ID, v.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, grant.Token, "test-model"); e == nil {
		t.Fatal("departed member still authorized")
	}
	grant, e = s.Issue(ctx, u.ID, team.ID, req)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `UPDATE model_credentials SET expires_at=now()-interval '1 second' WHERE id=$1`, grant.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, grant.Token, "test-model"); e == nil {
		t.Fatal("expired credential accepted")
	}
	c.until = time.Now().Add(-time.Second)
	if _, e = s.Issue(ctx, u.ID, team.ID, req); e == nil {
		t.Fatal("expired membership issued token")
	}
}
