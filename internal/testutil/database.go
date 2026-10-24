// Package testutil provides isolated, disposable PostgreSQL schemas for tests.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("UNIHUB_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("UNIHUB_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 12)
	if _, err = rand.Read(b); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(b)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	return pool
}

// NewDB returns a pool whose search path is a newly created isolated schema.
// Cleanup removes only that schema and closes the pool.
func NewDB(t *testing.T) *pgxpool.Pool { return Database(t) }
