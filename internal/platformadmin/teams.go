package platformadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/database"
)

type Team struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Version         int64  `json:"version"`
	ManagementState string `json:"management_state"`
	MemberCount     int64  `json:"member_count"`
}
type TeamMember struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Active bool   `json:"active"`
}
type TeamInvitation struct {
	ID                  string    `json:"id"`
	AccountID           string    `json:"account_id"`
	RecipientID         string    `json:"recipient_id"`
	InvitedBy           string    `json:"invited_by"`
	Role                string    `json:"role"`
	AuthorizationSource string    `json:"authorization_source"`
	TransferOwnerID     string    `json:"transfer_owner_id,omitempty"`
	Version             int64     `json:"version"`
	ExpiresAt           time.Time `json:"expires_at"`
}
type TeamNameInput struct {
	Name            string `json:"name"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}
type TeamMemberInput struct {
	Role            string `json:"role"`
	Remove          bool   `json:"remove"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}
type TeamInvitationInput struct {
	RecipientID     string `json:"recipient_id"`
	Role            string `json:"role"`
	TransferOwnerID string `json:"transfer_owner_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

func identityID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func teamReason(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 1000 }
func identityDBError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return accounts.ErrNotFound
	}
	return e
}
func (s *Store) TeamDetail(ctx context.Context, actor, id string) (Team, error) {
	if e := s.Require(ctx, actor, "accounts:read"); e != nil {
		return Team{}, e
	}
	return s.teamOn(ctx, s.pool, id)
}
func (s *Store) teamOn(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (Team, error) {
	var t Team
	e := q.QueryRow(ctx, `SELECT a.id,a.kind,a.name,a.admin_version,CASE WHEN EXISTS(SELECT 1 FROM account_members m JOIN users u ON u.id=m.user_id WHERE m.account_id=a.id AND m.role='owner' AND u.active) THEN 'active' ELSE 'owner_disabled_recovery_required' END,(SELECT count(*) FROM account_members m WHERE m.account_id=a.id) FROM accounts a WHERE a.id=$1`, id).Scan(&t.ID, &t.Kind, &t.Name, &t.Version, &t.ManagementState, &t.MemberCount)
	return t, identityDBError(e)
}
func (s *Store) TeamMembers(ctx context.Context, actor, id string, o ListOptions) (Page[TeamMember], error) {
	if e := s.Require(ctx, actor, "accounts:read"); e != nil {
		return Page[TeamMember]{}, e
	}
	if o.Role != "" || !o.CreatedFrom.IsZero() || !o.CreatedTo.IsZero() {
		return Page[TeamMember]{}, accounts.ErrInvalid
	}
	t, e := s.teamOn(ctx, s.pool, id)
	if e != nil {
		return Page[TeamMember]{}, e
	}
	o, c, e := directoryOptions(o, "team-members", actor, id, strconv.FormatInt(t.Version, 10))
	if e != nil {
		return Page[TeamMember]{}, e
	}
	p := Page[TeamMember]{Items: []TeamMember{}, AsOf: c.AsOf}
	where := ` FROM account_members m JOIN users u ON u.id=m.user_id WHERE m.account_id=$1 AND ($2='' OR u.name ILIKE '%'||$2||'%' OR u.email ILIKE '%'||$2||'%') AND m.created_at<=$3 AND ($4='' OR ($4='active' AND u.active) OR ($4='disabled' AND NOT u.active))`
	e = s.pool.QueryRow(ctx, `SELECT count(*)`+where, id, o.Search, c.AsOf, o.Status).Scan(&p.Total)
	if e != nil {
		return p, e
	}
	rows, e := s.pool.Query(ctx, `SELECT u.id,u.name,u.email,m.role,u.active`+where+` AND u.id>$5 ORDER BY u.id LIMIT $6`, id, o.Search, c.AsOf, o.Status, c.ID, o.Limit+1)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		var m TeamMember
		if e = rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role, &m.Active); e != nil {
			return p, e
		}
		if len(p.Items) == o.Limit {
			c.ID = p.Items[len(p.Items)-1].UserID
			p.NextCursor = encodeDirectoryCursor(c)
			break
		}
		p.Items = append(p.Items, m)
	}
	return p, rows.Err()
}
func (s *Store) teamMutation(ctx context.Context, actor, id, reason string, version int64, allowRecovery bool, fn func(pgx.Tx, Team) error) (Team, error) {
	if !teamReason(reason) || version < 1 {
		return Team{}, accounts.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Team{}, e
	}
	defer database.Rollback(tx)
	// Same lock/order as platform-grant changes: invitation acceptance cannot race revocation.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`); e != nil {
		return Team{}, e
	}
	if e = s.requireOn(ctx, tx, actor, "teams:write"); e != nil {
		return Team{}, e
	}
	if e = database.LockAccountTx(ctx, tx, id); e != nil {
		return Team{}, e
	}
	t, e := s.teamOn(ctx, tx, id)
	if e != nil {
		return t, e
	}
	if t.Kind != "team" {
		return t, accounts.ErrForbidden
	}
	if t.Version != version {
		return t, accounts.ErrConflict
	}
	if !allowRecovery && t.ManagementState != "active" {
		return t, accounts.ErrConflict
	}
	if e = fn(tx, t); e != nil {
		return t, e
	}
	t, e = s.teamOn(ctx, tx, id)
	if e != nil {
		return t, e
	}
	return t, tx.Commit(ctx)
}
func (s *Store) RenameTeam(ctx context.Context, actor, id string, in TeamNameInput) (Team, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 100 {
		return Team{}, accounts.ErrInvalid
	}
	return s.teamMutation(ctx, actor, id, in.Reason, in.ExpectedVersion, true, func(tx pgx.Tx, t Team) error {
		if _, e := tx.Exec(ctx, `UPDATE accounts SET name=$2,admin_version=admin_version+1 WHERE id=$1`, id, in.Name); e != nil {
			return e
		}
		return WriteAudit(ctx, tx, actor, "team.name", id, in.Reason, map[string]any{"before": t.Name, "after": in.Name, "version": t.Version + 1})
	})
}
func (s *Store) ChangeTeamMember(ctx context.Context, actor, id, user string, in TeamMemberInput) (Team, error) {
	if !in.Remove && in.Role != "member" && in.Role != "admin" && in.Role != "billing_admin" && in.Role != "owner" {
		return Team{}, accounts.ErrInvalid
	}
	return s.teamMutation(ctx, actor, id, in.Reason, in.ExpectedVersion, false, func(tx pgx.Tx, t Team) error {
		if e := s.requireTargetOn(ctx, tx, actor, user, "teams:write"); e != nil {
			return e
		}
		var current string
		if e := tx.QueryRow(ctx, `SELECT role FROM account_members WHERE account_id=$1 AND user_id=$2`, id, user).Scan(&current); e != nil {
			return identityDBError(e)
		}
		if current != "member" || (!in.Remove && in.Role != "member") {
			if e := s.requireOn(ctx, tx, actor, "teams:privileged"); e != nil {
				return e
			}
		}
		if !in.Remove && in.Role == "owner" && current != "owner" {
			return accounts.ErrInvalid
		}
		if current == "owner" && (in.Remove || in.Role != "owner") {
			var n int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM account_members m JOIN users u ON u.id=m.user_id WHERE m.account_id=$1 AND m.role='owner' AND u.active AND m.user_id<>$2`, id, user).Scan(&n); e != nil {
				return e
			}
			if n == 0 {
				return accounts.ErrConflict
			}
		}
		var e error
		if in.Remove {
			_, e = tx.Exec(ctx, `DELETE FROM account_members WHERE account_id=$1 AND user_id=$2`, id, user)
		} else {
			_, e = tx.Exec(ctx, `UPDATE account_members SET role=$3 WHERE account_id=$1 AND user_id=$2`, id, user, in.Role)
		}
		if e != nil {
			return e
		}
		return WriteAudit(ctx, tx, actor, "team.member", user, in.Reason, map[string]any{"account_id": id, "before": current, "after": in.Role, "remove": in.Remove, "version": t.Version})
	})
}
func (s *Store) CreateTeamInvitation(ctx context.Context, actor, id string, in TeamInvitationInput) (TeamInvitation, error) {
	if in.Role != "member" && in.Role != "admin" && in.Role != "billing_admin" && in.Role != "owner" || in.RecipientID == "" || in.TransferOwnerID != "" && in.Role != "owner" {
		return TeamInvitation{}, accounts.ErrInvalid
	}
	v := TeamInvitation{ID: identityID(), AccountID: id, RecipientID: in.RecipientID, InvitedBy: actor, Role: in.Role, AuthorizationSource: "platform", TransferOwnerID: in.TransferOwnerID, Version: 1}
	_, e := s.teamMutation(ctx, actor, id, in.Reason, in.ExpectedVersion, in.Role == "owner", func(tx pgx.Tx, t Team) error {
		if e := s.requireTargetOn(ctx, tx, actor, in.RecipientID, "teams:write"); e != nil {
			return e
		}
		if in.Role != "member" {
			if e := s.requireOn(ctx, tx, actor, "teams:privileged"); e != nil {
				return e
			}
		}
		var active bool
		if e := tx.QueryRow(ctx, `SELECT active FROM users WHERE id=$1`, in.RecipientID).Scan(&active); e != nil {
			return identityDBError(e)
		}
		if !active {
			return accounts.ErrConflict
		}
		if t.ManagementState != "active" {
			var member bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_members WHERE account_id=$1 AND user_id=$2)`, id, in.RecipientID).Scan(&member); e != nil {
				return e
			}
			if !member {
				return accounts.ErrConflict
			}
		}
		if in.TransferOwnerID != "" {
			var owner bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_members WHERE account_id=$1 AND user_id=$2 AND role='owner')`, id, in.TransferOwnerID).Scan(&owner); e != nil {
				return e
			}
			if !owner || in.TransferOwnerID == in.RecipientID {
				return accounts.ErrInvalid
			}
		}
		if _, e := tx.Exec(ctx, `UPDATE platform_team_invitations SET revoked_at=now(),version=version+1 WHERE account_id=$1 AND recipient_id=$2 AND expires_at<=now() AND accepted_at IS NULL AND revoked_at IS NULL`, id, in.RecipientID); e != nil {
			return e
		}
		e := tx.QueryRow(ctx, `INSERT INTO platform_team_invitations(id,account_id,recipient_id,invited_by,role,transfer_owner_id,reason) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7) RETURNING expires_at`, v.ID, id, in.RecipientID, actor, in.Role, in.TransferOwnerID, in.Reason).Scan(&v.ExpiresAt)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE accounts SET admin_version=admin_version+1 WHERE id=$1`, id); e != nil {
			return e
		}
		return WriteAudit(ctx, tx, actor, "team.invitation", in.RecipientID, in.Reason, v)
	})
	return v, e
}
func (s *Store) RevokeTeamInvitation(ctx context.Context, actor, id, inv string, version int64, reason string) (Team, error) {
	return s.teamMutation(ctx, actor, id, reason, version, true, func(tx pgx.Tx, t Team) error {
		var role string
		var recipient string
		e := tx.QueryRow(ctx, `SELECT role,recipient_id FROM platform_team_invitations WHERE id=$1 AND account_id=$2 AND accepted_at IS NULL AND revoked_at IS NULL FOR UPDATE`, inv, id).Scan(&role, &recipient)
		if e != nil {
			return identityDBError(e)
		}
		if e = s.requireTargetOn(ctx, tx, actor, recipient, "teams:write"); e != nil {
			return e
		}
		if role != "member" {
			if e = s.requireOn(ctx, tx, actor, "teams:privileged"); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE platform_team_invitations SET revoked_at=now(),version=version+1 WHERE id=$1`, inv); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE accounts SET admin_version=admin_version+1 WHERE id=$1`, id); e != nil {
			return e
		}
		return WriteAudit(ctx, tx, actor, "team.invitation.revoke", recipient, reason, map[string]string{"invitation_id": inv, "account_id": id})
	})
}

func (s *Store) AcceptPlatformInvitation(ctx context.Context, user, inv string, version int64) (Team, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return Team{}, e
	}
	defer database.Rollback(tx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('unihub:platform-administration',0))`); e != nil {
		return Team{}, e
	}
	var v TeamInvitation
	var pending, active bool
	var reason string
	e = tx.QueryRow(ctx, `SELECT i.account_id,i.recipient_id,i.invited_by,i.role,COALESCE(i.transfer_owner_id,''),i.version,i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now(),u.active,i.reason FROM platform_team_invitations i JOIN users u ON u.id=i.recipient_id WHERE i.id=$1 FOR UPDATE OF i`, inv).Scan(&v.AccountID, &v.RecipientID, &v.InvitedBy, &v.Role, &v.TransferOwnerID, &v.Version, &pending, &active, &reason)
	if e != nil {
		return Team{}, identityDBError(e)
	}
	if user != v.RecipientID {
		return Team{}, accounts.ErrForbidden
	}
	if !pending || !active || version != v.Version {
		return Team{}, accounts.ErrConflict
	}
	if e = database.LockAccountTx(ctx, tx, v.AccountID); e != nil {
		return Team{}, e
	}
	if e = s.requireTargetOn(ctx, tx, v.InvitedBy, user, "teams:write"); e != nil {
		return Team{}, e
	}
	if v.Role != "member" {
		if e = s.requireOn(ctx, tx, v.InvitedBy, "teams:privileged"); e != nil {
			return Team{}, e
		}
	}
	t, e := s.teamOn(ctx, tx, v.AccountID)
	if e != nil {
		return t, e
	}
	if t.Kind != "team" {
		return t, accounts.ErrForbidden
	}
	if t.ManagementState != "active" && v.Role != "owner" {
		return t, accounts.ErrConflict
	}
	var current string
	e = tx.QueryRow(ctx, `SELECT role FROM account_members WHERE account_id=$1 AND user_id=$2`, t.ID, user).Scan(&current)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return t, e
	}
	if e == nil && (v.Role != "owner" || current == "owner") {
		return t, accounts.ErrConflict
	}
	if t.ManagementState != "active" && e != nil {
		return t, accounts.ErrConflict
	}
	if _, e = tx.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,$3) ON CONFLICT(account_id,user_id) DO UPDATE SET role=EXCLUDED.role`, t.ID, user, v.Role); e != nil {
		return t, e
	}
	if v.TransferOwnerID != "" {
		r, e := tx.Exec(ctx, `UPDATE account_members SET role='member' WHERE account_id=$1 AND user_id=$2 AND role='owner'`, t.ID, v.TransferOwnerID)
		if e != nil {
			return t, e
		}
		if r.RowsAffected() != 1 {
			return t, accounts.ErrConflict
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE platform_team_invitations SET accepted_at=now(),version=version+1 WHERE id=$1`, inv); e != nil {
		return t, e
	}
	if e = WriteAudit(ctx, tx, user, "team.invitation.accept", user, "recipient accepted platform invitation", map[string]any{"invitation_id": inv, "account_id": t.ID, "invited_by": v.InvitedBy, "source": "platform", "role": v.Role, "transfer_owner_id": v.TransferOwnerID}); e != nil {
		return t, e
	}
	t, e = s.teamOn(ctx, tx, t.ID)
	if e != nil {
		return t, e
	}
	return t, tx.Commit(ctx)
}
func (s *Store) PlatformInvitations(ctx context.Context, actor, account string, o ListOptions) (Page[TeamInvitation], error) {
	if account != "" {
		if e := s.Require(ctx, actor, "accounts:read"); e != nil {
			return Page[TeamInvitation]{}, e
		}
	}
	if o.Search != "" || o.Status != "" || o.Role != "" || !o.CreatedFrom.IsZero() || !o.CreatedTo.IsZero() {
		return Page[TeamInvitation]{}, accounts.ErrInvalid
	}
	o, c, e := directoryOptions(o, "platform-invitations", actor, account)
	if e != nil {
		return Page[TeamInvitation]{}, e
	}
	p := Page[TeamInvitation]{Items: []TeamInvitation{}, AsOf: c.AsOf}
	where := ` FROM platform_team_invitations i WHERE i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>now() AND i.created_at<=$3 AND (($2='' AND i.recipient_id=$1) OR ($2<>'' AND i.account_id=$2))`
	if e = s.pool.QueryRow(ctx, `SELECT count(*)`+where, actor, account, c.AsOf).Scan(&p.Total); e != nil {
		return p, e
	}
	rows, e := s.pool.Query(ctx, `SELECT i.id,i.account_id,i.recipient_id,i.invited_by,i.role,i.authorization_source,COALESCE(i.transfer_owner_id,''),i.version,i.expires_at`+where+` AND i.id>$4 ORDER BY i.id LIMIT $5`, actor, account, c.AsOf, c.ID, o.Limit+1)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		var v TeamInvitation
		if e = rows.Scan(&v.ID, &v.AccountID, &v.RecipientID, &v.InvitedBy, &v.Role, &v.AuthorizationSource, &v.TransferOwnerID, &v.Version, &v.ExpiresAt); e != nil {
			return p, e
		}
		if len(p.Items) == o.Limit {
			c.ID = p.Items[len(p.Items)-1].ID
			p.NextCursor = encodeDirectoryCursor(c)
			break
		}
		p.Items = append(p.Items, v)
	}
	return p, rows.Err()
}
