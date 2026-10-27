package productusers

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
)

// RequireRuntimeCreditOn preserves the product's original management hierarchy.
// The caller holds this transaction until the immutable financial command is
// accepted, so a concurrent role change cannot authorize a peer adjustment.
func (s *Store) RequireRuntimeCreditOn(ctx context.Context, tx pgx.Tx, actor, product, instance, account string) error {
	m, ok := s.registry.Get(product, instance)
	if !ok {
		return accounts.ErrForbidden
	}
	var target string
	if e := tx.QueryRow(ctx, `SELECT user_id FROM product_runtime_bindings WHERE product_id=$1 AND instance_id=$2 AND personal_account_id=$3`, product, Instance(instance), account).Scan(&target); e != nil {
		return e
	}
	for _, query := range []string{
		`SELECT id FROM users WHERE id IN ($3,$4) AND $1::text <> '' AND $2::text <> '' ORDER BY id FOR SHARE`,
		`SELECT user_id FROM product_user_access WHERE product_id=$1 AND instance_id=$2 AND user_id IN ($3,$4) ORDER BY user_id FOR SHARE`,
	} {
		rows, e := tx.Query(ctx, query, product, Instance(instance), actor, target)
		if e != nil {
			return e
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
	}
	if e := s.RequirePermissionOn(ctx, tx, actor, product, instance, "credit:adjust"); e != nil {
		return e
	}
	role := func(user string) (int, error) {
		var native int
		var raw []byte
		e := tx.QueryRow(ctx, `SELECT product_role,roles FROM product_user_access WHERE user_id=$1 AND product_id=$2 AND instance_id=$3`, user, product, Instance(instance)).Scan(&native, &raw)
		roles := m.DefaultRoles
		if e == pgx.ErrNoRows {
			native, e = 1, nil
		} else if e == nil {
			e = json.Unmarshal(raw, &roles)
		}
		return EffectiveProductRole(m, roles, native), e
	}
	a, e := role(actor)
	if e != nil {
		return e
	}
	t, e := role(target)
	if e != nil {
		return e
	}
	// An inactive target retains its role and remains manageable by a superior.
	if a < 100 && (a < 10 || actor == target || a <= t) {
		return accounts.ErrForbidden
	}
	return nil
}
