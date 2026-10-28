package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/database"
)

func (s *Store) OperationOverview(ctx context.Context, actor string) (map[string]any, error) {
	p, e := s.Principal(ctx, actor)
	if e != nil {
		return nil, e
	}
	if !p.Enabled {
		return nil, accounts.ErrForbidden
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer database.Rollback(tx)
	var users, enabled, teams, products, identities int64
	e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM users WHERE active),(SELECT count(*) FROM accounts WHERE kind='team'),(SELECT count(*) FROM products),(SELECT count(*) FROM identities)`).Scan(&users, &enabled, &teams, &products, &identities)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"central_users": users, "enabled_users": enabled, "disabled_users": users - enabled, "teams": teams, "products": products, "materialized_identity_links": identities, "registered_identity_total": nil, "identity_directory_status": "separate_provider_query_required", "as_of": time.Now().UTC(), "consistency": "query_snapshot", "income_status": "not_computed_payment_capability_required"}
	finance := false
	for _, permission := range p.Permissions {
		if permission == "finance:read" {
			finance = true
		}
	}
	if finance {
		var orders, refunds, pending, unknown int64
		e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM billing_orders),(SELECT count(*) FROM billing_orders WHERE snapshot->>'refund_status'='MANUAL_REVIEW'),(SELECT count(*) FROM billing_commands WHERE status IN ('CREATED','SUBMITTING','UNKNOWN'))+(SELECT count(*) FROM wallet_operations WHERE status IN ('CREATED','SUBMITTING','UNKNOWN')),(SELECT count(*) FROM billing_commands WHERE status='UNKNOWN')+(SELECT count(*) FROM wallet_operations WHERE status='UNKNOWN')`).Scan(&orders, &refunds, &pending, &unknown)
		if e != nil {
			return nil, e
		}
		var calls, settled, released, processing int64
		from := time.Now().UTC().Add(-24 * time.Hour)
		e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE snapshot->>'status'='SETTLED'),count(*) FILTER(WHERE snapshot->>'status'='RELEASED'),count(*) FILTER(WHERE snapshot->>'status' IN ('PENDING','RESERVED','MISSING_USAGE')) FROM billing_calls WHERE created_at >=$1`, from).Scan(&calls, &settled, &released, &processing)
		if e != nil {
			return nil, e
		}
		out["orders"] = orders
		out["refund_reviews"] = refunds
		out["pending_commands"] = pending
		out["unknown_commands"] = unknown
		out["calls"] = map[string]any{"window_from": from, "window_to": out["as_of"], "timezone": "UTC", "logical_calls": calls, "settled": settled, "released": released, "processing": processing, "failure_rate": nil, "failure_rate_status": "supplier_outcome_telemetry_unavailable", "attempts_status": "supplier_attempt_telemetry_unavailable"}
	}
	return out, tx.Commit(ctx)
}

func (s *Store) OperationSystemStatus(ctx context.Context, actor string) (map[string]any, error) {
	if e := s.Require(ctx, actor, "system:read"); e != nil {
		return nil, e
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if e := s.pool.Ping(probe); e != nil {
		return nil, e
	}
	principal, e := s.Principal(ctx, actor)
	if e != nil {
		return nil, e
	}
	finance := false
	for _, permission := range principal.Permissions {
		if permission == "finance:read" {
			finance = true
		}
	}
	out := map[string]any{"as_of": time.Now().UTC(), "postgresql": map[string]any{"status": "available"}, "ledger": map[string]any{"status": "not_probed", "reason": "select an account wallet to perform an authoritative ledger read"}, "oidc": map[string]any{"status": "not_probed", "reason": "identity capability adapter reports provider capabilities separately"}, "configuration_source": "deployment", "platform_authority": "database", "secret_configuration_editable": false}
	if finance {
		var count int64
		var oldest *time.Time
		if e := s.pool.QueryRow(probe, `SELECT count(*),min(created_at) FROM (SELECT created_at FROM billing_commands WHERE status IN ('CREATED','SUBMITTING','UNKNOWN') UNION ALL SELECT created_at FROM wallet_operations WHERE status IN ('CREATED','SUBMITTING','UNKNOWN')) pending`).Scan(&count, &oldest); e != nil {
			return nil, e
		}
		out["pending_commands"] = count
		out["oldest_pending_at"] = oldest
		rows, e := s.pool.Query(probe, `SELECT worker_id,last_run_at,last_success_at,last_error_at,last_error_code,updated_at FROM platform_worker_status ORDER BY worker_id`)
		if e != nil {
			return nil, e
		}
		workers := []map[string]any{}
		for rows.Next() {
			var id, code string
			var run, success, failure *time.Time
			var updated time.Time
			if e = rows.Scan(&id, &run, &success, &failure, &code, &updated); e != nil {
				break
			}
			workers = append(workers, map[string]any{"worker_id": id, "last_run_at": run, "last_success_at": success, "last_error_at": failure, "last_error_code": code, "updated_at": updated})
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return nil, e
		}
		out["workers"] = workers
		if len(workers) == 0 {
			out["worker_telemetry_status"] = "not_recorded"
		} else {
			out["worker_telemetry_status"] = "recorded"
		}
	}
	return out, nil
}

func (s *Store) RevokeOperationKey(ctx context.Context, actor, key, reason string, version int64) (map[string]any, error) {
	if !validReason(reason) || version < 0 || version > 1 {
		return nil, billing.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer database.Rollback(tx)
	if e = lockAuthority(ctx, tx); e != nil {
		return nil, e
	}
	if e = s.requireOn(ctx, tx, actor, "keys:revoke"); e != nil {
		return nil, e
	}
	var owner, account, product string
	var revoked *time.Time
	e = tx.QueryRow(ctx, `SELECT actor_id,account_id,origin_product_id,revoked_at FROM model_credentials WHERE id=$1 AND credential_kind='api_key' FOR UPDATE`, key).Scan(&owner, &account, &product, &revoked)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, accounts.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if e = s.requireTargetOn(ctx, tx, actor, owner, "keys:revoke"); e != nil {
		return nil, e
	}
	current := int64(0)
	if revoked != nil {
		current = 1
	}
	if current != version {
		return nil, billing.ErrConflict
	}
	if revoked == nil {
		e = tx.QueryRow(ctx, `UPDATE model_credentials SET revoked_at=clock_timestamp() WHERE id=$1 RETURNING revoked_at`, key).Scan(&revoked)
		if e != nil {
			return nil, e
		}
	}
	out := map[string]any{"id": key, "user_id": owner, "account_id": account, "product_id": product, "revoked_at": revoked, "version": 1, "status": "revoked"}
	if e = s.WriteAudit(ctx, tx, actor, "key.revoked", owner, reason, out); e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}

// CompleteOperationPage loads live quantities only for the bounded page. The
// list of batches never presents stale snapshot balances as authoritative.
func (s *Store) CompleteOperationPage(ctx context.Context, actor, resource string, b *billing.Service, page *Page[map[string]any]) error {
	if resource != "api-keys" && resource != "credit-batches" && resource != "usage-budgets" && resource != "billing-profiles" {
		return nil
	}
	if e := s.Require(ctx, actor, "finance:read"); e != nil {
		return e
	}
	for _, v := range page.Items {
		if resource == "billing-profiles" {
			if v["configuration_source"] == "implicit_legacy" && v["status"] != "product_disabled" {
				if b == nil {
					return billing.ErrUnsupported
				}
				product, _ := v["product_id"].(string)
				profile, e := b.Profile(ctx, product)
				if e != nil {
					return e
				}
				raw, _ := json.Marshal(profile)
				var value map[string]any
				if e = json.Unmarshal(raw, &value); e != nil {
					return e
				}
				for k, x := range value {
					v[k] = x
				}
			}
			continue
		}
		if resource == "credit-batches" {
			if b == nil {
				return billing.ErrUnsupported
			}
			raw, _ := json.Marshal(v)
			var batch billing.Batch
			if e := json.Unmarshal(raw, &batch); e != nil {
				return e
			}
			if e := b.AdminBatchQuantities(ctx, s, actor, &batch); e != nil {
				return e
			}
			v["available"] = batch.Available
			v["reserved"] = batch.Reserved
			v["consumed"] = batch.Consumed
			v["expired"] = batch.Expired
			v["withdrawn"] = batch.Withdrawn
			v["quantities_status"] = "available"
			v["quantities_as_of"] = time.Now().UTC()
		} else {
			account, _ := v["account_id"].(string)
			product, _ := v["product_id"].(string)
			instance, _ := v["instance_id"].(string)
			asset, _ := v["asset"].(string)
			key := ""
			if resource == "api-keys" {
				key, _ = v["id"].(string)
			}
			held, used, e := billing.UsageTotalsOn(ctx, s.pool, account, product, instance, asset, key)
			if e != nil {
				return e
			}
			v["held"] = held
			v["used"] = used
			total, _ := v["max_total"].(string)
			v["remaining"] = billing.UsageRemaining(total, held, used)
		}
	}
	return nil
}

func (s *Store) RevokeOperationKeysByUser(ctx context.Context, actor, target, reason string, version int64) (map[string]any, error) {
	if !validReason(reason) || version < 1 {
		return nil, billing.ErrInvalid
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer database.Rollback(tx)
	if e = lockAuthority(ctx, tx); e != nil {
		return nil, e
	}
	if e = s.requireTargetOn(ctx, tx, actor, target, "keys:revoke"); e != nil {
		return nil, e
	}
	var current int64
	if e = tx.QueryRow(ctx, `SELECT status_version FROM users WHERE id=$1 FOR UPDATE`, target).Scan(&current); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, accounts.ErrNotFound
		}
		return nil, e
	}
	if current != version {
		return nil, billing.ErrConflict
	}
	result, e := tx.Exec(ctx, `UPDATE model_credentials SET revoked_at=clock_timestamp() WHERE actor_id=$1 AND credential_kind='api_key' AND revoked_at IS NULL`, target)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"user_id": target, "revoked_count": result.RowsAffected(), "version": current, "status": "revoked"}
	if e = s.WriteAudit(ctx, tx, actor, "key.user_revoked", target, reason, out); e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}
