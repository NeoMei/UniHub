-- Product identities keep their existing IDs. Only explicit, verified mappings
-- may connect them to a central identity; email is never an identity key.
CREATE TABLE product_runtime_bindings (
 id text PRIMARY KEY,
 product_id text NOT NULL,
 instance_id text NOT NULL,
 local_user_id text NOT NULL,
 user_id text NOT NULL REFERENCES users(id),
 issuer text NOT NULL,
 subject text NOT NULL,
 personal_account_id text NOT NULL REFERENCES accounts(id),
 binding_key text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(product_id,instance_id,local_user_id),
 UNIQUE(product_id,instance_id,user_id)
);
-- Financial reservations use the same final ledger and recoverable command
-- journal as other UniHub funds. No mutable spendable balance lives in SQL.
CREATE TABLE product_runtime_calls (
 id text PRIMARY KEY,
 account_id text NOT NULL REFERENCES accounts(id),
 request_digest text NOT NULL,
 snapshot jsonb NOT NULL
);
CREATE INDEX product_runtime_calls_account ON product_runtime_calls(account_id);
CREATE TABLE product_runtime_adjustments (
 id text PRIMARY KEY,
 account_id text NOT NULL REFERENCES accounts(id),
 product_id text NOT NULL,
 instance_id text NOT NULL,
 source_kind text NOT NULL CHECK(source_kind IN ('gift','migration','payment','redemption','checkin','referral','signup','refund')),
 source_key text NOT NULL,
 request_digest text NOT NULL,
 snapshot jsonb NOT NULL,
 UNIQUE(product_id,instance_id,source_kind,source_key)
);
ALTER TABLE usage_budgets DROP CONSTRAINT usage_budgets_asset_check;
ALTER TABLE usage_budgets ADD CONSTRAINT usage_budgets_asset_check CHECK(asset IN ('CREDIT/6','CNY/6','LR_QUOTA/0'));
