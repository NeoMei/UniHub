package accounts

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

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
	user, err := s.EnsureIdentityOn(ctx, tx, identity)
	if err != nil {
		return User{}, err
	}
	return user, tx.Commit(ctx)
}

// LockIdentity serializes writers for an exact issuer and subject until tx ends.
func LockIdentity(ctx context.Context, tx pgx.Tx, issuer, subject string) error {
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(subject) == "" {
		return ErrInvalid
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('identity:' || length($1::text)::text || ':' || $1 || $2,0))`, issuer, subject)
	return err
}

const identityEmailHintSQL = `lower(regexp_replace(email, '^[[:space:]]+|[[:space:]]+$', '', 'g'))`
const inputIdentityEmailHintSQL = `(SELECT ` + identityEmailHintSQL + ` FROM (VALUES ($1::text)) AS input(email))`

// LockIdentityEmail serializes new account writes sharing a profile email hint.
// Call after locking the exact identity. Email does not identify the account.
func LockIdentityEmail(ctx context.Context, tx pgx.Tx, email string) error {
	if strings.TrimSpace(email) == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('identity-email:' || `+inputIdentityEmailHintSQL+`,0))`, email)
	return err
}

// RequireUnusedIdentityEmail is for automatic enrollment. In a read-committed
// transaction, check in a separate statement so a creator that held the lock
// is visible after its commit. Ordinary account creation can share an email.
func RequireUnusedIdentityEmail(ctx context.Context, tx pgx.Tx, email string) error {
	if strings.TrimSpace(email) == "" {
		return ErrInvalid
	}
	if err := LockIdentityEmail(ctx, tx, email); err != nil {
		return err
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE `+identityEmailHintSQL+`=`+inputIdentityEmailHintSQL+`)`, email).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	return nil
}

// EnsureIdentityOn creates or resolves an identity and its personal account in tx.
func (s *Store) EnsureIdentityOn(ctx context.Context, tx pgx.Tx, identity Identity) (User, error) {
	if err := LockIdentity(ctx, tx, identity.Issuer, identity.Subject); err != nil {
		return User{}, err
	}
	var user User
	err := tx.QueryRow(ctx, `SELECT u.id,u.name,u.email FROM users u JOIN identities i ON i.user_id=u.id WHERE i.issuer=$1 AND i.subject=$2`, identity.Issuer, identity.Subject).Scan(&user.ID, &user.Name, &user.Email)
	if err == nil {
		return user, nil
	}
	if domainError(err) != ErrNotFound {
		return User{}, err
	}
	if err = LockIdentityEmail(ctx, tx, identity.Email); err != nil {
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
	return user, nil
}
