package database_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"sync"
	"testing"
	"time"

	"unihub/internal/database"
	"unihub/internal/testutil"
)

type rollbackProbe struct {
	pgx.Tx
	deadline     time.Time
	called       bool
	contextError error
}

func (p *rollbackProbe) Rollback(ctx context.Context) error {
	p.called = true
	p.deadline, _ = ctx.Deadline()
	p.contextError = ctx.Err()
	return nil
}

func TestRollbackCleanupHasIndependentBoundedContext(t *testing.T) {
	p := &rollbackProbe{}
	started := time.Now()
	database.Rollback(p)
	if !p.called || p.contextError != nil || p.deadline.IsZero() {
		t.Fatalf("cleanup was unbounded or already canceled: %#v", p)
	}
	if limit := p.deadline.Sub(started); limit <= 0 || limit > 5*time.Second+100*time.Millisecond {
		t.Fatalf("cleanup deadline outside bounded budget: %v", limit)
	}
}

func TestCanceledAccountUnlockDiscardsConnectionAndReleasesLock(t *testing.T) {
	p := testutil.NewDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := database.LockAccount(ctx, conn, "cleanup-test")
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err = unlock(canceled); err == nil {
		conn.Release()
		t.Fatal("canceled unlock unexpectedly succeeded")
	}
	closed := conn.Conn().IsClosed()
	conn.Release()
	if !closed {
		t.Fatal("failed unlock returned a locked connection to the pool")
	}
	fresh, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Release()
	release, err := database.LockAccount(ctx, fresh, "cleanup-test")
	if err != nil {
		t.Fatalf("account lock leaked after connection discard: %v", err)
	}
	if err = release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFailureRollsBackSchemaAndVersionTogether(t *testing.T) {
	p := testutil.NewDB(t)
	ctx := context.Background()
	// An incompatible table in this disposable schema forces migration 001 to
	// fail after it has created users, identities and accounts.
	if _, err := p.Exec(ctx, `CREATE TABLE account_members (incompatible boolean)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, p); err == nil {
		t.Fatal("incompatible schema was accepted")
	}
	var exists bool
	if err := p.QueryRow(ctx, `SELECT to_regclass('users') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("failed migration left partial users table")
	}
	var versions int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 0 {
		t.Fatalf("failed migration marked applied: %d", versions)
	}
	if _, err := p.Exec(ctx, `DROP TABLE account_members`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions < 1 {
		t.Fatal("successful retry was not recorded")
	}
}

func TestConcurrentMigrationRunnersApplyEachVersionOnce(t *testing.T) {
	p := testutil.NewDB(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() { results <- database.Migrate(ctx, p) })
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, unique int
	if err := p.QueryRow(ctx, `SELECT count(*),count(DISTINCT version) FROM schema_migrations`).Scan(&count, &unique); err != nil {
		t.Fatal(err)
	}
	if count < 1 || count != unique {
		t.Fatalf("versions count=%d unique=%d", count, unique)
	}
	if err := database.Migrate(ctx, p); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}
