package database

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InstallPlatformFaultWaiverSchema installs only the operator-case evidence table.
// Unlike Migrate it never creates the application schema or applies other migrations.
func InstallPlatformFaultWaiverSchema(ctx context.Context, pool *pgxpool.Pool) error {
	c, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer c.Release()
	if _, err = c.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('unihub:migrations:' || current_schema(),0))`); err != nil {
		return err
	}
	defer func() {
		if _, e := c.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('unihub:migrations:' || current_schema(),0))`); e != nil {
			closeConnection(c.Conn())
		}
	}()
	return migrateOne(ctx, c, "migrations/023_platform_fault_waivers.sql")
}
