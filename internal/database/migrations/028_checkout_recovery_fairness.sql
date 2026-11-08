-- Provider outages must not permanently starve newer checkout/refund work.
ALTER TABLE billing_checkouts ADD COLUMN last_recovery_attempt timestamptz;
CREATE INDEX billing_checkouts_recovery_attempt ON billing_checkouts(last_recovery_attempt NULLS FIRST, id);
