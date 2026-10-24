package accounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"unihub/internal/database"
)

// Invite records a pending invitation addressed to an existing UniHub user.
// It grants no access until that same user explicitly accepts it.
func (s *Store) Invite(ctx context.Context, actorID, accountID, recipientID, role string) (Invitation, error) {
	if !validRole(role) || recipientID == "" {
		return Invitation{}, ErrInvalid
	}
	inv := Invitation{ID: newID(), AccountID: accountID, RecipientID: recipientID, Role: role}
	err := s.teamMutation(ctx, actorID, accountID, func(tx pgx.Tx, actorRole string) error {
		if actorRole != "owner" && (actorRole != "admin" || role != "member") {
			return ErrForbidden
		}
		var current string
		err := tx.QueryRow(ctx, `SELECT role FROM account_members WHERE account_id=$1 AND user_id=$2`, accountID, recipientID).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && (role != "owner" || current == "owner") {
			return ErrConflict
		}
		// A previously authorized inviter may since have lost authority. Revoke
		// that stale invitation under the account lock before issuing a new one;
		// valid pending invitations must still conflict rather than be replaced.
		var oldID, oldInviter, oldRole string
		err = tx.QueryRow(ctx, `SELECT id,invited_by,role FROM account_invitations WHERE account_id=$1 AND recipient_id=$2 AND accepted_at IS NULL AND revoked_at IS NULL`, accountID, recipientID).Scan(&oldID, &oldInviter, &oldRole)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			inviterRole, roleErr := s.RoleOn(ctx, tx, oldInviter, accountID)
			if roleErr != nil && !errors.Is(roleErr, ErrForbidden) {
				return roleErr
			}
			if roleErr == nil && (inviterRole == "owner" || (inviterRole == "admin" && oldRole == "member")) {
				return ErrConflict
			}
			if _, err = tx.Exec(ctx, `UPDATE account_invitations SET revoked_at=now() WHERE id=$1`, oldID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO account_invitations(id,account_id,recipient_id,invited_by,role) VALUES($1,$2,$3,$4,$5)`, inv.ID, accountID, recipientID, actorID, role)
		return err
	})
	if err != nil {
		return Invitation{}, err
	}
	return inv, nil
}

func (s *Store) AcceptInvitation(ctx context.Context, userID, invitationID string) (Account, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer database.Rollback(tx)
	var accountID, recipient string
	if err = tx.QueryRow(ctx, `SELECT account_id,recipient_id FROM account_invitations WHERE id=$1`, invitationID).Scan(&accountID, &recipient); err != nil {
		return Account{}, domainError(err)
	}
	if recipient != userID {
		return Account{}, ErrForbidden
	}
	if err = database.LockAccountTx(ctx, tx, accountID); err != nil {
		return Account{}, err
	}
	var a Account
	var pending bool
	var invitedBy string
	if err = tx.QueryRow(ctx, `SELECT a.id,a.kind,a.name,i.role,i.accepted_at IS NULL AND i.revoked_at IS NULL,i.invited_by FROM account_invitations i JOIN accounts a ON a.id=i.account_id WHERE i.id=$1`, invitationID).Scan(&a.ID, &a.Kind, &a.Name, &a.Role, &pending, &invitedBy); err != nil {
		return Account{}, domainError(err)
	}
	if !pending {
		return Account{}, ErrConflict
	}
	if a.Kind != "team" {
		return Account{}, ErrForbidden
	}
	// Recheck the inviter's current authority so a revoked manager cannot leave
	// usable invitations granting authority after their removal or demotion.
	inviterRole, err := s.RoleOn(ctx, tx, invitedBy, accountID)
	if err != nil {
		return Account{}, err
	}
	if inviterRole != "owner" && (inviterRole != "admin" || a.Role != "member") {
		return Account{}, ErrForbidden
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT role FROM account_members WHERE account_id=$1 AND user_id=$2`, a.ID, userID).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Account{}, err
	}
	if err == nil {
		if a.Role != "owner" || current == "owner" {
			return Account{}, ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE account_members SET role='owner' WHERE account_id=$1 AND user_id=$2`, a.ID, userID)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,$3)`, a.ID, userID, a.Role)
	}
	if err != nil {
		return Account{}, domainError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE account_invitations SET accepted_at=now() WHERE id=$1`, invitationID); err != nil {
		return Account{}, err
	}
	return a, tx.Commit(ctx)
}

func (s *Store) ListMembers(ctx context.Context, actorID, accountID string) ([]Member, error) {
	if _, err := s.Role(ctx, actorID, accountID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT u.id,u.name,u.email,m.role FROM account_members m JOIN users u ON u.id=m.user_id WHERE m.account_id=$1 ORDER BY u.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Member{}
	for rows.Next() {
		var m Member
		if err = rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Store) ChangeRole(ctx context.Context, actorID, accountID, userID, role string) error {
	if !validRole(role) {
		return ErrInvalid
	}
	return s.mutateMember(ctx, actorID, accountID, userID, role, false)
}
func (s *Store) RemoveMember(ctx context.Context, actorID, accountID, userID string) error {
	return s.mutateMember(ctx, actorID, accountID, userID, "", true)
}
func (s *Store) mutateMember(ctx context.Context, actorID, accountID, userID, nextRole string, remove bool) error {
	return s.teamMutation(ctx, actorID, accountID, func(tx pgx.Tx, actorRole string) error {
		var current string
		if err := tx.QueryRow(ctx, `SELECT role FROM account_members WHERE account_id=$1 AND user_id=$2`, accountID, userID).Scan(&current); err != nil {
			return err
		}
		if actorRole != "owner" && (actorRole != "admin" || current != "member" || (!remove && nextRole != "member")) {
			return ErrForbidden
		}
		if !remove && nextRole == "owner" && current != "owner" {
			return fmt.Errorf("%w: ownership requires invitation acceptance", ErrInvalid)
		}
		if current == "owner" && (remove || nextRole != "owner") {
			var owners int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_members WHERE account_id=$1 AND role='owner'`, accountID).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				return ErrConflict
			}
		}
		var err error
		if remove {
			_, err = tx.Exec(ctx, `DELETE FROM account_members WHERE account_id=$1 AND user_id=$2`, accountID, userID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE account_members SET role=$3 WHERE account_id=$1 AND user_id=$2`, accountID, userID, nextRole)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
}
