ALTER TABLE cash_checkouts ADD COLUMN last_recovery_attempt timestamptz;

CREATE INDEX cash_checkouts_recovery
    ON cash_checkouts(last_recovery_attempt NULLS FIRST, created_at, id)
    WHERE snapshot->>'payment_status' = 'CREATED';
