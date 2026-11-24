package accounts

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"unihub/internal/database"
)

type TeamGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TeamGroupDirectory struct {
	Groups   []TeamGroup `json:"groups"`
	GroupIDs []string    `json:"group_ids"`
}

type TeamGroupMembership struct {
	UserID   string   `json:"user_id"`
	GroupIDs []string `json:"group_ids"`
}

// Read authorization and directory rows from the same snapshot.
func (s *Store) readTeamGroups(ctx context.Context, actorID, teamID string, read func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer database.Rollback(tx)

	var member bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM account_members m
		JOIN accounts a ON a.id=m.account_id
		JOIN users u ON u.id=m.user_id
		WHERE m.account_id=$1 AND m.user_id=$2 AND a.kind='team' AND u.active
	)`, teamID, actorID).Scan(&member)
	if err != nil {
		return err
	}
	if !member {
		return ErrForbidden
	}
	if err = read(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TeamGroups lists the team's groups and the caller's own group assignments.
func (s *Store) TeamGroups(ctx context.Context, actorID, teamID string) (TeamGroupDirectory, error) {
	directory := TeamGroupDirectory{Groups: []TeamGroup{}, GroupIDs: []string{}}
	err := s.readTeamGroups(ctx, actorID, teamID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT g.id,g.name,gm.user_id IS NOT NULL
			FROM team_groups g
			LEFT JOIN team_group_members gm ON gm.team_id=g.team_id AND gm.group_id=g.id AND gm.user_id=$2
			WHERE g.team_id=$1 ORDER BY g.name_key,g.id`, teamID, actorID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var group TeamGroup
			var assigned bool
			if err := rows.Scan(&group.ID, &group.Name, &assigned); err != nil {
				return err
			}
			directory.Groups = append(directory.Groups, group)
			if assigned {
				directory.GroupIDs = append(directory.GroupIDs, group.ID)
			}
		}
		return rows.Err()
	})
	return directory, err
}

// TeamGroupMemberships includes all current team members, including those with no group.
func (s *Store) TeamGroupMemberships(ctx context.Context, actorID, teamID string) ([]TeamGroupMembership, error) {
	memberships := []TeamGroupMembership{}
	err := s.readTeamGroups(ctx, actorID, teamID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT m.user_id,
			COALESCE(array_agg(gm.group_id ORDER BY gm.group_id)
				FILTER (WHERE gm.group_id IS NOT NULL), ARRAY[]::text[])
			FROM account_members m
			LEFT JOIN team_group_members gm ON gm.team_id=m.account_id AND gm.user_id=m.user_id
			WHERE m.account_id=$1 GROUP BY m.user_id ORDER BY m.user_id`, teamID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var membership TeamGroupMembership
			if err := rows.Scan(&membership.UserID, &membership.GroupIDs); err != nil {
				return err
			}
			memberships = append(memberships, membership)
		}
		return rows.Err()
	})
	return memberships, err
}

func normalizeTeamGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || strings.ContainsRune(name, 0) || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 80 {
		return "", ErrInvalid
	}
	return name, nil
}

func (s *Store) teamGroupMutation(ctx context.Context, actorID, teamID string, mutate func(pgx.Tx) error) error {
	return s.teamMutation(ctx, actorID, teamID, func(tx pgx.Tx, role string) error {
		if role != "owner" && role != "admin" {
			return ErrForbidden
		}
		return mutate(tx)
	})
}

func (s *Store) CreateTeamGroup(ctx context.Context, actorID, teamID, name string) (TeamGroup, error) {
	name, err := normalizeTeamGroupName(name)
	if err != nil {
		return TeamGroup{}, err
	}
	group := TeamGroup{ID: newID(), Name: name}
	err = s.teamGroupMutation(ctx, actorID, teamID, func(tx pgx.Tx) error {
		// Materialize the key so case-insensitive matching works in every database locale.
		_, err := tx.Exec(ctx, `INSERT INTO team_groups(team_id,id,name,name_key) VALUES($1,$2,$3,$4)`,
			teamID, group.ID, group.Name, strings.ToLower(group.Name))
		return err
	})
	if err != nil {
		return TeamGroup{}, err
	}
	return group, nil
}

func (s *Store) RenameTeamGroup(ctx context.Context, actorID, teamID, groupID, name string) (TeamGroup, error) {
	name, err := normalizeTeamGroupName(name)
	if err != nil {
		return TeamGroup{}, err
	}
	var group TeamGroup
	err = s.teamGroupMutation(ctx, actorID, teamID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE team_groups SET name=$3,name_key=$4
			WHERE team_id=$1 AND id=$2 RETURNING id,name`, teamID, groupID, name, strings.ToLower(name)).
			Scan(&group.ID, &group.Name)
	})
	if err != nil {
		return TeamGroup{}, err
	}
	return group, nil
}

func (s *Store) DeleteTeamGroup(ctx context.Context, actorID, teamID, groupID string) error {
	return s.teamGroupMutation(ctx, actorID, teamID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM team_groups WHERE team_id=$1 AND id=$2`, teamID, groupID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) SetTeamGroupMembership(ctx context.Context, actorID, teamID, groupID, userID string, assigned bool) error {
	return s.teamGroupMutation(ctx, actorID, teamID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM team_groups g JOIN account_members m ON m.account_id=g.team_id
			WHERE g.team_id=$1 AND g.id=$2 AND m.user_id=$3
		)`, teamID, groupID, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if assigned {
			_, err := tx.Exec(ctx, `INSERT INTO team_group_members(team_id,group_id,user_id)
				VALUES($1,$2,$3) ON CONFLICT (team_id,group_id,user_id) DO NOTHING`, teamID, groupID, userID)
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM team_group_members WHERE team_id=$1 AND group_id=$2 AND user_id=$3`,
			teamID, groupID, userID)
		return err
	})
}
