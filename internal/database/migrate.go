// Package database contains schema migrations and shared account mutation locks.
package database

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Rollback bounds cleanup independently of the canceled request. pgx closes the
// connection if rollback fails, so an aborted transaction is not reused.
func Rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func closeConnection(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

// Migrate applies each embedded migration once, in filename order. Each migration
// and its version record commit together. A session lock serializes runners.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('unihub:migrations:' || current_schema(), 0))`); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanupCtx, `SELECT pg_advisory_unlock(hashtextextended('unihub:migrations:' || current_schema(), 0))`); err != nil {
			closeConnection(conn.Conn())
		}
	}()
	if _, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		if err := migrateOne(ctx, conn, name); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

func migrateOne(ctx context.Context, conn *pgxpool.Conn, name string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	var applied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return tx.Commit(ctx)
	}
	sql, err := migrations.ReadFile(name)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, string(sql)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LockAccount holds the account advisory lock on a dedicated acquired connection.
// Call release before returning conn to the pool. The release is idempotent and
// closes the underlying connection if unlocking fails, preventing lock leakage.
// All membership changes use the matching transaction lock, so authorization and
// external submission can remain inside this session's lock boundary.
func LockAccount(ctx context.Context, conn *pgxpool.Conn, accountID string) (func(context.Context) error, error) {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, accountID); err != nil {
		return nil, err
	}
	var once sync.Once
	var releaseErr error
	return func(ctx context.Context) error {
		once.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			var unlocked bool
			releaseErr = conn.QueryRow(cleanupCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, accountID).Scan(&unlocked)
			if releaseErr == nil && !unlocked {
				releaseErr = fmt.Errorf("account lock was not held")
			}
			if releaseErr != nil {
				closeConnection(conn.Conn())
			}
		})
		return releaseErr
	}, nil
}

// LockAccountTx serializes account mutations with LockAccount until transaction end.
func LockAccountTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, accountID)
	return err
}
