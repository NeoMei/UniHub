ALTER TABLE billing_orders ADD COLUMN upgrade_return_attempted_at timestamptz;
CREATE INDEX billing_order_upgrade_returns ON billing_orders (upgrade_return_attempted_at, id)
 WHERE snapshot->>'refund_status' IN ('UPGRADE_PENDING', 'UPGRADE_RELEASING');
