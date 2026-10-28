package accounts

import (
	"context"
	"errors"
	"testing"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func TestNativeTeamManagementPausedWithoutEnabledOwner(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	s := New(p)
	owner, _ := s.EnsureIdentity(ctx, Identity{Issuer: "test", Subject: "disabled-owner"})
	admin, _ := s.EnsureIdentity(ctx, Identity{Issuer: "test", Subject: "active-admin"})
	recipient, _ := s.EnsureIdentity(ctx, Identity{Issuer: "test", Subject: "recipient"})
	team, e := s.CreateTeam(ctx, owner.ID, "recovery")
	if e != nil {
		t.Fatal(e)
	}
	inv, e := s.Invite(ctx, owner.ID, team.ID, admin.ID, "admin")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AcceptInvitation(ctx, admin.ID, inv.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, owner.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Invite(ctx, admin.ID, team.ID, recipient.ID, "member"); !errors.Is(e, ErrConflict) {
		t.Fatal("native management proceeded without enabled owner", e)
	}
	if _, e = s.ListMembers(ctx, admin.ID, team.ID); e != nil {
		t.Fatal("read disrupted", e)
	}
}
func TestNativeCannotRemoveLastEnabledOwnerWhenDisabledOwnerExists(t *testing.T) {
	ctx := context.Background()
	p := testutil.Database(t)
	if e := database.Migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	s := New(p)
	owner, _ := s.EnsureIdentity(ctx, Identity{Issuer: "test", Subject: "only-active-owner"})
	disabled, _ := s.EnsureIdentity(ctx, Identity{Issuer: "test", Subject: "other-disabled-owner"})
	team, e := s.CreateTeam(ctx, owner.ID, "active owner protection")
	if e != nil {
		t.Fatal(e)
	}
	inv, e := s.Invite(ctx, owner.ID, team.ID, disabled.ID, "owner")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AcceptInvitation(ctx, disabled.ID, inv.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Exec(ctx, `UPDATE users SET active=false WHERE id=$1`, disabled.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.RemoveMember(ctx, owner.ID, team.ID, owner.ID); !errors.Is(e, ErrConflict) {
		t.Fatal("removed last enabled owner", e)
	}
}
