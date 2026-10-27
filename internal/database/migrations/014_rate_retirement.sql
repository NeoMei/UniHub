-- Retire prospective selection without rewriting immutable price/call snapshots.
ALTER TABLE billing_rates DROP CONSTRAINT billing_rates_status_check;
ALTER TABLE billing_rates ADD CONSTRAINT billing_rates_status_check CHECK(status IN ('published','retired'));
