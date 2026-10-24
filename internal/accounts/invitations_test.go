package accounts

import (
	"context"
	"testing"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestInvitationQueriesRespectRecipientManagerAndRevocation(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	s := New(p)
	owner, _ := s.EnsureIdentity(ctx, Identity{Issuer: "https://test", Subject: "inv-query-owner"})
	member, _ := s.EnsureIdentity(ctx, Identity{Issuer: "https://test", Subject: "inv-query-member"})
	other, _ := s.EnsureIdentity(ctx, Identity{Issuer: "https://test", Subject: "inv-query-other"})
	team, e := s.CreateTeam(ctx, owner.ID, "invitation queries")
	if e != nil {
		t.Fatal(e)
	}
	inv, e := s.Invite(ctx, owner.ID, team.ID, member.ID, "member")
	if e != nil {
		t.Fatal(e)
	}
	incoming, e := s.ListInvitations(ctx, member.ID, "")
	if e != nil || len(incoming) != 1 || incoming[0].ID != inv.ID {
		t.Fatalf("incoming %v %v", incoming, e)
	}
	none, e := s.ListInvitations(ctx, other.ID, "")
	if e != nil || len(none) != 0 {
		t.Fatal("other recipient saw invite", e)
	}
	if _, e = s.ListInvitations(ctx, member.ID, team.ID); e == nil {
		t.Fatal("pending invite granted manager reads")
	}
	out, e := s.ListInvitations(ctx, owner.ID, team.ID)
	if e != nil || len(out) != 1 {
		t.Fatal("owner could not read invite", e)
	}
	if _, e = s.AcceptInvitation(ctx, member.ID, inv.ID); e != nil {
		t.Fatal(e)
	}
	incoming, e = s.ListInvitations(ctx, member.ID, "")
	if e != nil || len(incoming) != 0 {
		t.Fatal("accepted invite still pending", e)
	}
	if _, e = s.ListInvitations(ctx, member.ID, team.ID); e == nil {
		t.Fatal("ordinary member read manager invitations")
	}
}
