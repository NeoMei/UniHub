// Package accounts implements identities, isolated accounts and team membership.
package accounts

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/database"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrConflict  = errors.New("conflict")
	ErrInvalid   = errors.New("invalid")
)

type Identity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	Name    string `json:"name"`
	Email   string `json:"email"`
}
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
type Account struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Role string `json:"role"`
}
type Invitation struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	RecipientID string `json:"recipient_id"`
	Role        string `json:"role"`
}
type Member struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Querier permits authorization on an already locked connection or transaction.
type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func domainError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return fmt.Errorf("%w: duplicate record", ErrConflict)
		case "23503":
			return fmt.Errorf("%w: referenced record", ErrNotFound)
		case "23514":
			return fmt.Errorf("%w: constraint", ErrInvalid)
		}
	}
	return err
}
func validRole(role string) bool {
	return role == "owner" || role == "admin" || role == "billing_admin" || role == "member"
}

func (s *Store) ListAccounts(ctx context.Context, userID string) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT a.id,a.kind,a.name,m.role FROM accounts a JOIN account_members m ON m.account_id=a.id WHERE m.user_id=$1 ORDER BY a.created_at,a.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Account{}
	for rows.Next() {
		var a Account
		if err = rows.Scan(&a.ID, &a.Kind, &a.Name, &a.Role); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
func (s *Store) CreateTeam(ctx context.Context, userID, name string) (Account, error) {
	name = strings.TrimSpace(name)
	if name == "" || userID == "" {
		return Account{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer database.Rollback(tx)
	a := Account{ID: newID(), Kind: "team", Name: name, Role: "owner"}
	if _, err = tx.Exec(ctx, `INSERT INTO accounts(id,kind,name) VALUES($1,'team',$2)`, a.ID, a.Name); err != nil {
		return Account{}, domainError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_members(account_id,user_id,role) VALUES($1,$2,'owner')`, a.ID, userID); err != nil {
		return Account{}, domainError(err)
	}
	return a, tx.Commit(ctx)
}
func (s *Store) Role(ctx context.Context, userID, accountID string) (string, error) {
	return s.RoleOn(ctx, s.pool, userID, accountID)
}
func (s *Store) RoleOn(ctx context.Context, q Querier, userID, accountID string) (string, error) {
	var role string
	err := q.QueryRow(ctx, `SELECT role FROM account_members WHERE user_id=$1 AND account_id=$2`, userID, accountID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	return role, err
}
func (s *Store) teamMutation(ctx context.Context, actorID, accountID string, fn func(pgx.Tx, string) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer database.Rollback(tx)
	if err = database.LockAccountTx(ctx, tx, accountID); err != nil {
		return err
	}
	role, err := s.RoleOn(ctx, tx, actorID, accountID)
	if err != nil {
		return err
	}
	var kind string
	if err = tx.QueryRow(ctx, `SELECT kind FROM accounts WHERE id=$1`, accountID).Scan(&kind); err != nil {
		return domainError(err)
	}
	if kind != "team" {
		return ErrForbidden
	}
	if err = fn(tx, role); err != nil {
		return domainError(err)
	}
	return tx.Commit(ctx)
}
