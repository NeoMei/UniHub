package main

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/products"
)

// Explicit operator-owned client configuration can introduce a new product
// slug without a source-code enum or seed edit. It never re-enables a product
// that an operator disabled and never rewrites commercial terms or balances.
func ensureConfiguredProducts(ctx context.Context, pool *pgxpool.Pool, clients map[string]products.Client) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	seen := map[string]bool{}
	for _, c := range clients {
		if seen[c.ProductID] {
			continue
		}
		seen[c.ProductID] = true
		if _, e = tx.Exec(ctx, `INSERT INTO products(id,name,enabled) VALUES($1,$1,true) ON CONFLICT(id) DO NOTHING`, c.ProductID); e != nil {
			return e
		}
		var enabled bool
		if e = tx.QueryRow(ctx, `SELECT enabled FROM products WHERE id=$1`, c.ProductID).Scan(&enabled); e != nil {
			return e
		}
		if !enabled {
			return errors.New("configured product is disabled")
		}
	}
	return tx.Commit(ctx)
}
