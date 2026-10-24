package accounts

import (
	"context"
	"strings"
	"unihub/internal/database"
)

// EnsureIdentity keys users only by issuer + subject. Profile email is never
// used to link identities. Its user and personal account are created atomically.
func (s *Store) EnsureIdentity(ctx context.Context, identity Identity) (User, error) {
	if strings.TrimSpace(identity.Issuer) == "" || strings.TrimSpace(identity.Subject) == "" {
		return User{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer database.Rollback(tx)
	// Serialize creation for this exact identity before checking for existence.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('identity:' || length($1::text)::text || ':' || $1 || $2,0))`, identity.Issuer, identity.Subject); err != nil {
		return User{}, err
	}
	var user User
	err = tx.QueryRow(ctx, `SELECT u.id,u.name,u.email FROM users u JOIN identities i ON i.user_id=u.id WHERE i.issuer=$1 AND i.subject=$2`, identity.Issuer, identity.Subject).Scan(&user.ID, &user.Name, &user.Email)
	if err == nil {
		return user, tx.Commit(ctx)
	}
	if domainError(err) != ErrNotFound {
		return User{}, err
	}
	user = User{ID: newID(), Name: identity.Name, Email: identity.Email}
	if _, err = tx.Exec(ctx, `INSERT INTO users(id,name,email) VALUES($1,$2,$3)`, user.ID, user.Name, user.Email); err != nil {
		return User{}, domainError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identities(issuer,subject,user_id) VALUES($1,$2,$3)`, identity.Issuer, identity.Subject, user.ID); err != nil {
		return User{}, domainError(err)
	}
	accountID := newID()
	name := strings.TrimSpace(identity.Name)
	if name == "" {
		name = "Personal"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO accounts(id,kind,name,personal_user_id) VALUES($1,'personal',$2,$3)`, accountID, name, user.ID); err != nil {
		return User{}, domainError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'owner')`, accountID, user.ID); err != nil {
		return User{}, domainError(err)
	}
	return user, tx.Commit(ctx)
}
