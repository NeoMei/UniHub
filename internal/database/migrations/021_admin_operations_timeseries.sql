-- Admin overview timeseries aggregates scan billing_calls by a global time
-- window. The usage indexes all lead with account_id, so platform-wide daily
-- counts and per-model failure rates need a created_at leading index.
CREATE INDEX admin_billing_calls_created ON billing_calls(created_at);
