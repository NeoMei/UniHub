-- Add the column without a default first: a migration timestamp is not evidence
-- of when an old logical call happened. Existing reserve commands are evidence.
ALTER TABLE billing_calls ADD COLUMN created_at timestamptz;
ALTER TABLE billing_calls ADD COLUMN time_source text NOT NULL DEFAULT 'unknown';
UPDATE billing_calls c
SET created_at = evidence.created_at, time_source = 'reserve_command'
FROM (
    SELECT account_id, request_key, min(created_at) AS created_at
    FROM billing_commands
    WHERE kind = 'reserve'
    GROUP BY account_id, request_key
) evidence
WHERE c.account_id = evidence.account_id AND c.id = evidence.request_key;
-- Future inserts timestamp receipt of the logical call, not upstream execution.
ALTER TABLE billing_calls ALTER COLUMN created_at SET DEFAULT clock_timestamp();
ALTER TABLE billing_calls ALTER COLUMN time_source SET DEFAULT 'reserve_received';
CREATE INDEX billing_calls_account_time ON billing_calls(account_id, created_at DESC NULLS LAST, id DESC);
CREATE INDEX billing_calls_origin_time ON billing_calls(account_id, (snapshot->>'origin_product_id'), created_at DESC NULLS LAST, id DESC);
CREATE INDEX billing_calls_actor_time ON billing_calls(account_id, (snapshot->>'origin_product_id'), (snapshot->>'actor_id'), created_at DESC NULLS LAST, id DESC);
CREATE INDEX billing_calls_model_time ON billing_calls(account_id, (snapshot->>'origin_product_id'), (snapshot#>>'{rate,model}'), created_at DESC NULLS LAST, id DESC);
CREATE INDEX billing_commands_unknown_call ON billing_commands(account_id, (effect#>>'{call,call_id}')) WHERE status='UNKNOWN' AND kind IN ('reserve','topup','settle','release');
