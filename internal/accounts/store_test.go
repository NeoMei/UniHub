package accounts_test

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func setup(t *testing.T) (*accounts.Store, context.Context) {
	t.Helper()
	p := testutil.Database(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	return accounts.New(p), ctx
}

func identity(t *testing.T, s *accounts.Store, ctx context.Context, subject string) accounts.User {
	t.Helper()
	u, err := s.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: subject, Name: subject, Email: "shared@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestIdentityUsesIssuerAndSubjectAndCreatesOnePersonalAccount(t *testing.T) {
	s, ctx := setup(t)
	a := identity(t, s, ctx, "a")
	again := identity(t, s, ctx, "a")
	b := identity(t, s, ctx, "b")
	otherIssuer, err := s.EnsureIdentity(ctx, accounts.Identity{Issuer: "other", Subject: "a", Email: "shared@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != again.ID || a.ID == b.ID || a.ID == otherIssuer.ID {
		t.Fatal("identity key must be issuer + subject, never email")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a.ID) {
		t.Fatalf("user ID must be UUID v4: %q", a.ID)
	}
	accts, err := s.ListAccounts(ctx, a.ID)
	if err != nil || len(accts) != 1 || accts[0].Kind != "personal" || accts[0].Role != "owner" {
		t.Fatalf("personal accounts: %+v %v", accts, err)
	}
	if _, err = s.Invite(ctx, a.ID, accts[0].ID, b.ID, "member"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("personal membership must remain private: %v", err)
	}
}

func TestConcurrentOwnerRemovalsPreserveAnOwner(t *testing.T) {
	s, ctx := setup(t)
	a := identity(t, s, ctx, "a")
	b := identity(t, s, ctx, "b")
	team, err := s.CreateTeam(ctx, a.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.Invite(ctx, a.ID, team.ID, b.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, b.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 2)
	go func() { result <- s.RemoveMember(ctx, a.ID, team.ID, a.ID) }()
	go func() { result <- s.RemoveMember(ctx, b.ID, team.ID, b.ID) }()
	first, second := <-result, <-result
	if !((first == nil && errors.Is(second, accounts.ErrConflict)) || (second == nil && errors.Is(first, accounts.ErrConflict))) {
		t.Fatalf("concurrent removals: %v / %v", first, second)
	}
	var owners int
	for _, id := range []string{a.ID, b.ID} {
		role, err := s.Role(ctx, id, team.ID)
		if err == nil && role == "owner" {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("remaining owners: %d", owners)
	}
}

func TestOwnershipPromotionRequiresExistingMembersAcceptance(t *testing.T) {
	s, ctx := setup(t)
	o := identity(t, s, ctx, "o")
	m := identity(t, s, ctx, "m")
	team, err := s.CreateTeam(ctx, o.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.Invite(ctx, o.ID, team.ID, m.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, m.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeRole(ctx, o.ID, team.ID, m.ID, "owner"); !errors.Is(err, accounts.ErrInvalid) {
		t.Fatalf("ownership changed without acceptance: %v", err)
	}
	owner, err := s.Invite(ctx, o.ID, team.ID, m.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	role, err := s.Role(ctx, m.ID, team.ID)
	if err != nil || role != "member" {
		t.Fatalf("pending promotion: %q %v", role, err)
	}
	if _, err = s.AcceptInvitation(ctx, m.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	role, err = s.Role(ctx, m.ID, team.ID)
	if err != nil || role != "owner" {
		t.Fatalf("accepted promotion: %q %v", role, err)
	}
}

func TestPendingFinanceInvitationCannotOutliveInvitersAuthority(t *testing.T) {
	s, ctx := setup(t)
	a := identity(t, s, ctx, "a")
	b := identity(t, s, ctx, "b")
	c := identity(t, s, ctx, "c")
	team, err := s.CreateTeam(ctx, a.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.Invite(ctx, a.ID, team.ID, b.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, b.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	finance, err := s.Invite(ctx, a.ID, team.ID, c.ID, "billing_admin")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeRole(ctx, b.ID, team.ID, a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, c.ID, finance.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("demoted inviter granted finance: %v", err)
	}
}

func TestAuthorizedOwnerCanReplaceInvitationAfterInviterLosesAuthority(t *testing.T) {
	s, ctx := setup(t)
	a := identity(t, s, ctx, "a")
	b := identity(t, s, ctx, "b")
	c := identity(t, s, ctx, "c")
	team, err := s.CreateTeam(ctx, a.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	backup, err := s.Invite(ctx, a.ID, team.ID, b.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, b.ID, backup.ID); err != nil {
		t.Fatal(err)
	}
	old, err := s.Invite(ctx, a.ID, team.ID, c.ID, "billing_admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Invite(ctx, b.ID, team.ID, c.ID, "member"); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("valid invitation was replaced: %v", err)
	}
	if err = s.ChangeRole(ctx, b.ID, team.ID, a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, c.ID, old.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("old inviter still grants finance: %v", err)
	}
	replacement, err := s.Invite(ctx, b.ID, team.ID, c.ID, "billing_admin")
	if err != nil {
		t.Fatalf("authorized replacement blocked by stale invitation: %v", err)
	}
	if replacement.ID == old.ID {
		t.Fatal("replacement reused invalidated invitation identity")
	}
	// Restoring the old inviter's authority must not revive the revoked record.
	restore, err := s.Invite(ctx, b.ID, team.ID, a.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, a.ID, restore.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, c.ID, old.ID); err == nil {
		t.Fatal("invalidated invitation was accepted")
	}
	if _, err = s.AcceptInvitation(ctx, c.ID, replacement.ID); err != nil {
		t.Fatalf("replacement acceptance: %v", err)
	}
	if role, err := s.Role(ctx, c.ID, team.ID); err != nil || role != "billing_admin" {
		t.Fatalf("replacement role=%s err=%v", role, err)
	}
}

func TestInvalidInputsAndUnknownRecipientsFailWithoutMembership(t *testing.T) {
	s, ctx := setup(t)
	if _, err := s.EnsureIdentity(ctx, accounts.Identity{Issuer: " ", Subject: "a"}); !errors.Is(err, accounts.ErrInvalid) {
		t.Fatalf("blank issuer: %v", err)
	}
	a := identity(t, s, ctx, "a")
	if _, err := s.CreateTeam(ctx, a.ID, " "); !errors.Is(err, accounts.ErrInvalid) {
		t.Fatalf("blank team: %v", err)
	}
	team, err := s.CreateTeam(ctx, a.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Invite(ctx, a.ID, team.ID, "unknown-user", "member"); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("nonexistent recipient: %v", err)
	}
	if _, err = s.Invite(ctx, a.ID, team.ID, a.ID, "member"); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("existing membership: %v", err)
	}
	if _, err = s.Invite(ctx, a.ID, team.ID, a.ID, "super_admin"); !errors.Is(err, accounts.ErrInvalid) {
		t.Fatalf("invalid role: %v", err)
	}
	if _, err = s.AcceptInvitation(ctx, a.ID, "missing"); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("missing invitation: %v", err)
	}
}

func TestConcurrentIdentityCreatesOneUserAndPersonalAccount(t *testing.T) {
	s, ctx := setup(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errCh := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			u, err := s.EnsureIdentity(ctx, accounts.Identity{Issuer: "test", Subject: "same"})
			ids <- u.ID
			errCh <- err
		})
	}
	wg.Wait()
	close(ids)
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate users")
		}
		id = got
	}
	all, err := s.ListAccounts(ctx, id)
	if err != nil || len(all) != 1 {
		t.Fatalf("accounts=%+v err=%v", all, err)
	}
}

func TestInvitationRequiresRecipientAcceptanceAndAdminCannotGrantFinance(t *testing.T) {
	s, ctx := setup(t)
	owner := identity(t, s, ctx, "owner")
	admin := identity(t, s, ctx, "admin")
	member := identity(t, s, ctx, "member")
	stranger := identity(t, s, ctx, "stranger")
	team, err := s.CreateTeam(ctx, owner.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.Invite(ctx, owner.ID, team.ID, admin.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Role(ctx, admin.ID, team.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("invitation granted early access: %v", err)
	}
	if _, err = s.AcceptInvitation(ctx, stranger.ID, inv.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("wrong recipient accepted: %v", err)
	}
	if _, err = s.AcceptInvitation(ctx, admin.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Invite(ctx, admin.ID, team.ID, member.ID, "billing_admin"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("admin granted finance: %v", err)
	}
	if _, err = s.Invite(ctx, admin.ID, team.ID, member.ID, "owner"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("admin granted ownership: %v", err)
	}
	inv, err = s.Invite(ctx, admin.ID, team.ID, member.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, member.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, member.ID, inv.ID); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("reused invitation: %v", err)
	}
	if err = s.ChangeRole(ctx, admin.ID, team.ID, member.ID, "billing_admin"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("admin promoted finance: %v", err)
	}
	if err = s.ChangeRole(ctx, owner.ID, team.ID, member.ID, "billing_admin"); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveMember(ctx, admin.ID, team.ID, member.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("admin removed finance: %v", err)
	}
	members, err := s.ListMembers(ctx, owner.ID, team.ID)
	if err != nil || len(members) != 3 {
		t.Fatalf("members=%+v err=%v", members, err)
	}
	if _, err = s.ListMembers(ctx, stranger.ID, team.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("stranger listed members: %v", err)
	}
	if err = s.RemoveMember(ctx, owner.ID, team.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Role(ctx, member.ID, team.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("removed member retained access: %v", err)
	}
	personal, err := s.ListAccounts(ctx, member.ID)
	if err != nil || len(personal) != 1 || personal[0].Kind != "personal" {
		t.Fatalf("personal isolation=%+v err=%v", personal, err)
	}
}

func TestAuthorizedManagersCanRevokePendingInvitations(t *testing.T) {
	s, ctx := setup(t)
	owner := identity(t, s, ctx, "revoke-owner")
	admin := identity(t, s, ctx, "revoke-admin")
	member := identity(t, s, ctx, "revoke-member")
	team, err := s.CreateTeam(ctx, owner.ID, "Revoke team")
	if err != nil {
		t.Fatal(err)
	}
	adminInvite, err := s.Invite(ctx, owner.ID, team.ID, admin.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, admin.ID, adminInvite.ID); err != nil {
		t.Fatal(err)
	}
	memberInvite, err := s.Invite(ctx, admin.ID, team.ID, member.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeInvitation(ctx, admin.ID, team.ID, memberInvite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, member.ID, memberInvite.ID); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("revoked invitation accepted: %v", err)
	}
	if _, err = s.Invite(ctx, owner.ID, team.ID, member.ID, "member"); err != nil {
		t.Fatalf("revoked invitation still blocks replacement: %v", err)
	}
}

func TestAdminCannotRevokeElevatedInvitation(t *testing.T) {
	s, ctx := setup(t)
	owner := identity(t, s, ctx, "revoke-elevated-owner")
	admin := identity(t, s, ctx, "revoke-elevated-admin")
	target := identity(t, s, ctx, "revoke-elevated-target")
	team, err := s.CreateTeam(ctx, owner.ID, "Elevated revoke team")
	if err != nil {
		t.Fatal(err)
	}
	adminInvite, err := s.Invite(ctx, owner.ID, team.ID, admin.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, admin.ID, adminInvite.ID); err != nil {
		t.Fatal(err)
	}
	ownerInvite, err := s.Invite(ctx, owner.ID, team.ID, target.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeInvitation(ctx, admin.ID, team.ID, ownerInvite.ID); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("admin revoked elevated invitation: %v", err)
	}
}

func TestLastOwnerCannotBeRemovedOrDemoted(t *testing.T) {
	s, ctx := setup(t)
	o := identity(t, s, ctx, "owner")
	b := identity(t, s, ctx, "backup")
	team, err := s.CreateTeam(ctx, o.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveMember(ctx, o.ID, team.ID, o.ID); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("removed final owner: %v", err)
	}
	if err = s.ChangeRole(ctx, o.ID, team.ID, o.ID, "member"); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("demoted final owner: %v", err)
	}
	inv, err := s.Invite(ctx, o.ID, team.ID, b.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, b.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveMember(ctx, b.ID, team.ID, o.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveMember(ctx, b.ID, team.ID, b.ID); !errors.Is(err, accounts.ErrConflict) {
		t.Fatalf("removed remaining owner: %v", err)
	}
}

func TestAccountSessionLockBlocksMembershipChanges(t *testing.T) {
	p := testutil.Database(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	s := accounts.New(p)
	o := identity(t, s, ctx, "o")
	m := identity(t, s, ctx, "m")
	team, err := s.CreateTeam(ctx, o.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.Invite(ctx, o.ID, team.ID, m.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptInvitation(ctx, m.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	conn, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	unlock, err := database.LockAccount(ctx, conn, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(ctx)
	limited, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	if err = s.RemoveMember(limited, o.ID, team.ID, m.ID); err == nil {
		t.Fatal("membership changed through held account lock")
	}
	role, err := s.Role(ctx, m.ID, team.ID)
	if err != nil || role != "member" {
		t.Fatalf("role=%s err=%v", role, err)
	}
}
