package accounts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"unihub/internal/accounts"
	"unihub/internal/database"
	"unihub/internal/testutil"
)

func joinGroupTeam(t *testing.T, s *accounts.Store, ctx context.Context, owner, team, user, role string) {
	t.Helper()
	invite, err := s.Invite(ctx, owner, team, user, role)
	require.NoError(t, err)
	_, err = s.AcceptInvitation(ctx, user, invite.ID)
	require.NoError(t, err)
}

func TestTeamGroupDirectoryAndMembershipLifecycle(t *testing.T) {
	s, ctx := setup(t)
	owner := identity(t, s, ctx, "group-owner")
	admin := identity(t, s, ctx, "group-admin")
	member := identity(t, s, ctx, "group-member")
	team, err := s.CreateTeam(ctx, owner.ID, "Team")
	require.NoError(t, err)
	joinGroupTeam(t, s, ctx, owner.ID, team.ID, admin.ID, "admin")
	joinGroupTeam(t, s, ctx, owner.ID, team.ID, member.ID, "member")

	directory, err := s.TeamGroups(ctx, member.ID, team.ID)
	require.NoError(t, err)
	require.Equal(t, accounts.TeamGroupDirectory{Groups: []accounts.TeamGroup{}, GroupIDs: []string{}}, directory)
	memberships, err := s.TeamGroupMemberships(ctx, member.ID, team.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []accounts.TeamGroupMembership{
		{UserID: owner.ID, GroupIDs: []string{}},
		{UserID: admin.ID, GroupIDs: []string{}},
		{UserID: member.ID, GroupIDs: []string{}},
	}, memberships)

	research, err := s.CreateTeamGroup(ctx, owner.ID, team.ID, "  Équipe  ")
	require.NoError(t, err)
	require.Equal(t, "Équipe", research.Name)
	_, err = s.CreateTeamGroup(ctx, admin.ID, team.ID, "équipe")
	require.ErrorIs(t, err, accounts.ErrConflict)
	_, err = s.CreateTeamGroup(ctx, admin.ID, team.ID, strings.Repeat("组", 81))
	require.ErrorIs(t, err, accounts.ErrInvalid)
	_, err = s.CreateTeamGroup(ctx, admin.ID, team.ID, "　")
	require.ErrorIs(t, err, accounts.ErrInvalid)
	support, err := s.CreateTeamGroup(ctx, admin.ID, team.ID, "Support")
	require.NoError(t, err)
	_, err = s.RenameTeamGroup(ctx, owner.ID, team.ID, research.ID, " SUPPORT ")
	require.ErrorIs(t, err, accounts.ErrConflict)
	renamed, err := s.RenameTeamGroup(ctx, admin.ID, team.ID, research.ID, "Research")
	require.NoError(t, err)
	require.Equal(t, research.ID, renamed.ID)
	require.Equal(t, "Research", renamed.Name)

	require.NoError(t, s.SetTeamGroupMembership(ctx, admin.ID, team.ID, research.ID, member.ID, true))
	require.NoError(t, s.SetTeamGroupMembership(ctx, admin.ID, team.ID, research.ID, member.ID, true))
	require.NoError(t, s.SetTeamGroupMembership(ctx, admin.ID, team.ID, support.ID, member.ID, true))
	require.NoError(t, s.SetTeamGroupMembership(ctx, admin.ID, team.ID, support.ID, owner.ID, true))
	directory, err = s.TeamGroups(ctx, member.ID, team.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []accounts.TeamGroup{renamed, support}, directory.Groups)
	require.ElementsMatch(t, []string{research.ID, support.ID}, directory.GroupIDs)
	memberships, err = s.TeamGroupMemberships(ctx, admin.ID, team.ID)
	require.NoError(t, err)
	require.Len(t, memberships, 3)
	for _, membership := range memberships {
		switch membership.UserID {
		case owner.ID:
			require.Equal(t, []string{support.ID}, membership.GroupIDs)
		case admin.ID:
			require.Equal(t, []string{}, membership.GroupIDs)
		case member.ID:
			require.ElementsMatch(t, []string{research.ID, support.ID}, membership.GroupIDs)
		}
	}

	require.NoError(t, s.SetTeamGroupMembership(ctx, owner.ID, team.ID, research.ID, member.ID, false))
	require.NoError(t, s.SetTeamGroupMembership(ctx, owner.ID, team.ID, research.ID, member.ID, false))
	require.NoError(t, s.DeleteTeamGroup(ctx, admin.ID, team.ID, support.ID))
	directory, err = s.TeamGroups(ctx, member.ID, team.ID)
	require.NoError(t, err)
	require.Equal(t, accounts.TeamGroupDirectory{Groups: []accounts.TeamGroup{renamed}, GroupIDs: []string{}}, directory)
	for _, want := range []struct{ user, role string }{{owner.ID, "owner"}, {admin.ID, "admin"}, {member.ID, "member"}} {
		role, err := s.Role(ctx, want.user, team.ID)
		require.NoError(t, err)
		require.Equal(t, want.role, role)
	}
	accountList, err := s.ListAccounts(ctx, member.ID)
	require.NoError(t, err)
	require.Len(t, accountList, 2)
	_, err = s.Role(ctx, member.ID, research.ID)
	require.ErrorIs(t, err, accounts.ErrForbidden)
}

func TestTeamGroupsEnforceTeamAndRoleBoundaries(t *testing.T) {
	s, ctx := setup(t)
	owner := identity(t, s, ctx, "group-owner")
	member := identity(t, s, ctx, "group-member")
	billing := identity(t, s, ctx, "group-billing")
	outsider := identity(t, s, ctx, "group-outsider")
	team, err := s.CreateTeam(ctx, owner.ID, "Team")
	require.NoError(t, err)
	other, err := s.CreateTeam(ctx, owner.ID, "Other team")
	require.NoError(t, err)
	joinGroupTeam(t, s, ctx, owner.ID, team.ID, member.ID, "member")
	joinGroupTeam(t, s, ctx, owner.ID, team.ID, billing.ID, "billing_admin")
	group, err := s.CreateTeamGroup(ctx, owner.ID, team.ID, "Same name")
	require.NoError(t, err)
	otherGroup, err := s.CreateTeamGroup(ctx, owner.ID, other.ID, "Same name")
	require.NoError(t, err)

	for _, actor := range []string{member.ID, billing.ID} {
		_, err = s.TeamGroups(ctx, actor, team.ID)
		require.NoError(t, err)
		_, err = s.CreateTeamGroup(ctx, actor, team.ID, "Unauthorized")
		require.ErrorIs(t, err, accounts.ErrForbidden)
		_, err = s.RenameTeamGroup(ctx, actor, team.ID, group.ID, "Unauthorized")
		require.ErrorIs(t, err, accounts.ErrForbidden)
		require.ErrorIs(t, s.DeleteTeamGroup(ctx, actor, team.ID, group.ID), accounts.ErrForbidden)
		require.ErrorIs(t, s.SetTeamGroupMembership(ctx, actor, team.ID, group.ID, actor, true), accounts.ErrForbidden)
	}
	_, err = s.TeamGroups(ctx, outsider.ID, team.ID)
	require.ErrorIs(t, err, accounts.ErrForbidden)
	_, err = s.TeamGroupMemberships(ctx, outsider.ID, team.ID)
	require.ErrorIs(t, err, accounts.ErrForbidden)
	personal, err := s.ListAccounts(ctx, outsider.ID)
	require.NoError(t, err)
	require.Len(t, personal, 1)
	_, err = s.TeamGroups(ctx, outsider.ID, personal[0].ID)
	require.ErrorIs(t, err, accounts.ErrForbidden)
	_, err = s.CreateTeamGroup(ctx, outsider.ID, personal[0].ID, "Personal")
	require.ErrorIs(t, err, accounts.ErrForbidden)
	_, err = s.RenameTeamGroup(ctx, owner.ID, team.ID, otherGroup.ID, "Foreign group")
	require.ErrorIs(t, err, accounts.ErrNotFound)
	require.ErrorIs(t, s.DeleteTeamGroup(ctx, owner.ID, team.ID, otherGroup.ID), accounts.ErrNotFound)
	require.ErrorIs(t, s.SetTeamGroupMembership(ctx, owner.ID, team.ID, otherGroup.ID, member.ID, true), accounts.ErrNotFound)
	require.ErrorIs(t, s.SetTeamGroupMembership(ctx, owner.ID, other.ID, otherGroup.ID, member.ID, false), accounts.ErrNotFound)
}

func TestTeamGroupMembershipForeignKeysAndRemovalCascade(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	require.NoError(t, database.Migrate(ctx, pool))
	s := accounts.New(pool)
	owner := identity(t, s, ctx, "group-owner")
	member := identity(t, s, ctx, "group-member")
	team, err := s.CreateTeam(ctx, owner.ID, "Team")
	require.NoError(t, err)
	other, err := s.CreateTeam(ctx, owner.ID, "Other team")
	require.NoError(t, err)
	joinGroupTeam(t, s, ctx, owner.ID, team.ID, member.ID, "member")
	group, err := s.CreateTeamGroup(ctx, owner.ID, team.ID, "Team group")
	require.NoError(t, err)
	otherGroup, err := s.CreateTeamGroup(ctx, owner.ID, other.ID, "Other group")
	require.NoError(t, err)

	_, err = pool.Exec(ctx, "INSERT INTO team_group_members (team_id,group_id,user_id) VALUES ($1,$2,$3)", team.ID, otherGroup.ID, member.ID)
	require.Error(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO team_group_members (team_id,group_id,user_id) VALUES ($1,$2,$3)", other.ID, otherGroup.ID, member.ID)
	require.Error(t, err)
	require.NoError(t, s.SetTeamGroupMembership(ctx, owner.ID, team.ID, group.ID, member.ID, true))
	require.NoError(t, s.RemoveMember(ctx, owner.ID, team.ID, member.ID))
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM team_group_members WHERE team_id=$1 AND user_id=$2", team.ID, member.ID).Scan(&remaining))
	require.Zero(t, remaining)
	joinGroupTeam(t, s, ctx, owner.ID, team.ID, member.ID, "member")
	directory, err := s.TeamGroups(ctx, member.ID, team.ID)
	require.NoError(t, err)
	require.Equal(t, accounts.TeamGroupDirectory{Groups: []accounts.TeamGroup{group}, GroupIDs: []string{}}, directory)
}
