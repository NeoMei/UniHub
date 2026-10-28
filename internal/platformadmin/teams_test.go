package platformadmin

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestTeamLastOwnerAndSourceBoundInvitation(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	a := accounts.New(p)
	owner, e := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	admin, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "admin"})
	recipient, _ := a.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "recipient"})
	team, e := a.CreateTeam(ctx, owner.ID, "Test team")
	if e != nil {
		t.Fatal(e)
	}
	s := New(p, nil)
	if e = s.Bootstrap(ctx, "deployment-cli", admin.ID, "test bootstrap"); e != nil {
		t.Fatal(e)
	}
	d, e := s.TeamDetail(ctx, admin.ID, team.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ChangeTeamMember(ctx, admin.ID, team.ID, owner.ID, TeamMemberInput{Remove: true, ExpectedVersion: d.Version, Reason: "remove last owner"}); !errors.Is(e, accounts.ErrConflict) {
		t.Fatalf("last owner removed: %v", e)
	}
	inv, e := s.CreateTeamInvitation(ctx, admin.ID, team.ID, TeamInvitationInput{RecipientID: recipient.ID, Role: "owner", TransferOwnerID: owner.ID, ExpectedVersion: d.Version, Reason: "accepted transfer"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.AcceptInvitation(ctx, recipient.ID, inv.ID); e == nil {
		t.Fatal("ordinary account path accepted platform source")
	}
	if _, e = p.Exec(ctx, `UPDATE platform_admin_roles SET roles='[]',version=version+1 WHERE user_id=$1`, admin.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AcceptPlatformInvitation(ctx, recipient.ID, inv.ID, inv.Version); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatalf("revoked inviter accepted: %v", e)
	}
}
