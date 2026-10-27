-- Durable credentials reuse the central hashed capability registry and billing
-- ledger. Empty limit strings mean unlimited; zero is an explicit deny cap.
ALTER TABLE model_credentials
 ADD COLUMN credential_kind text NOT NULL DEFAULT 'task',
 ADD COLUMN name text NOT NULL DEFAULT '',
 ADD COLUMN instance_id text NOT NULL DEFAULT 'default',
 ADD COLUMN max_total text NOT NULL DEFAULT '',
 ADD COLUMN max_per_call text NOT NULL DEFAULT '',
 ADD CONSTRAINT model_credentials_kind CHECK (credential_kind IN ('task','api_key')),
 ADD CONSTRAINT model_credentials_total CHECK (max_total='' OR max_total ~ '^(0|[1-9][0-9]{0,18})$'),
 ADD CONSTRAINT model_credentials_per_call CHECK (max_per_call='' OR max_per_call ~ '^(0|[1-9][0-9]{0,18})$');
CREATE INDEX model_api_keys_scope ON model_credentials(account_id,origin_product_id,instance_id,actor_id) WHERE credential_kind='api_key';

CREATE TABLE usage_budgets (
 account_id text NOT NULL REFERENCES accounts(id),
 origin_product_id text NOT NULL REFERENCES products(id),
 instance_id text NOT NULL,
 asset text NOT NULL CHECK (asset IN ('CREDIT/6','CNY/6')),
 version bigint NOT NULL CHECK (version>0),
 max_total text NOT NULL DEFAULT '' CHECK (max_total='' OR max_total ~ '^(0|[1-9][0-9]{0,18})$'),
 max_per_call text NOT NULL DEFAULT '' CHECK (max_per_call='' OR max_per_call ~ '^(0|[1-9][0-9]{0,18})$'),
 updated_by text NOT NULL REFERENCES users(id),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(account_id,origin_product_id,instance_id,asset)
);
CREATE INDEX billing_calls_credential ON billing_calls(account_id,(snapshot->>'credential_id'));
CREATE INDEX billing_calls_budget_scope ON billing_calls(account_id,(snapshot->>'origin_product_id'),(snapshot->>'instance_id'),(snapshot->>'charge_asset'));
