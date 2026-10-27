package main

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/billing"
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
		commercial := len(c.BillingProducts) > 0
		for _, scope := range c.Scopes {
			if scope == "catalog:read" || scope == "subscriptions:read" || scope == "credits:read" || scope == "wallets:read" || scope == "recharges:read" || scope == "recharges:write" || scope == "transfers:write" || scope == "operations:read" || scope == "orders:read" || scope == "orders:write" || scope == "settings:read" || scope == "settings:write" || scope == "refunds:write" || scope == "usage:read" || scope == "usage:all-products" || scope == "model-credentials:write" {
				commercial = true
			}
		}
		if !commercial {
			continue
		}
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

func ensureConfiguredBillingProducts(ctx context.Context, pool *pgxpool.Pool, profiles []billing.BillingProfile) error {
	configured := map[string]products.Client{}
	for _, profile := range profiles {
		configured[profile.ProductID] = products.Client{ProductID: profile.ProductID, BillingProducts: []string{profile.ProductID}}
	}
	return ensureConfiguredProducts(ctx, pool, configured)
}
