-- Retired offers stop new purchases; historical snapshots remain unchanged.
ALTER TABLE billing_offers DROP CONSTRAINT billing_offers_status_check;
ALTER TABLE billing_offers ADD CONSTRAINT billing_offers_status_check CHECK(status IN ('draft','published','retired'));

CREATE TABLE platform_worker_status (
 worker_id text PRIMARY KEY,
 last_run_at timestamptz NOT NULL,
 last_success_at timestamptz,
 last_error_at timestamptz,
 last_error_code text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX admin_accounts_created ON accounts(created_at,id);
CREATE INDEX admin_recharges_created ON recharge_orders(created_at,id);
CREATE INDEX admin_wallet_operations_created ON wallet_operations(created_at,id);
CREATE INDEX admin_billing_orders_created ON billing_orders(((snapshot->>'created_at')),id);
CREATE INDEX admin_platform_audit_created ON platform_admin_audit(created_at,id);
