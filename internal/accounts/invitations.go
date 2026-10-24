package accounts

import (
	"context"
	"github.com/jackc/pgx/v5"
	"unihub/internal/database"
)

// ListInvitations returns only pending, still-authorized invitations. An empty
// account lists the actor's incoming invitations; a team account requires its
// current owner/admin. An invitation alone never grants account visibility.
func (s *Store) ListInvitations(ctx context.Context, actor, account string) ([]Invitation, error) {
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer database.Rollback(tx)
	if account != "" {
		role, e := s.RoleOn(ctx, tx, actor, account)
		if e != nil {
			return nil, e
		}
		if role != "owner" && role != "admin" {
			return nil, ErrForbidden
		}
	}
	rows, e := tx.Query(ctx, `SELECT i.id,i.account_id,i.recipient_id,i.role FROM account_invitations i JOIN accounts a ON a.id=i.account_id JOIN account_members m ON m.account_id=i.account_id AND m.user_id=i.invited_by WHERE a.kind='team' AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND (($2='' AND i.recipient_id=$1) OR ($2<>'' AND i.account_id=$2)) AND (m.role='owner' OR (m.role='admin' AND i.role='member')) ORDER BY i.created_at DESC,i.id DESC LIMIT 100`, actor, account)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var inv Invitation
		if e = rows.Scan(&inv.ID, &inv.AccountID, &inv.RecipientID, &inv.Role); e != nil {
			return nil, e
		}
		out = append(out, inv)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	return out, tx.Commit(ctx)
}
