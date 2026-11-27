-- Stage 3: Unified model API keys, extended credit hold state machine fields,
-- platform cost difference tracking, and Lago invoice synchronization state.

CREATE TABLE model_api_keys (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  user_id uuid NOT NULL REFERENCES users(id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  name text NOT NULL,
  key_prefix text NOT NULL,
  key_hash bytea NOT NULL UNIQUE CHECK (octet_length(key_hash) = 32),
  router_key_id text,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz
);

CREATE INDEX model_api_keys_scope_idx
  ON model_api_keys (account_id, app_id, user_id, status);

ALTER TABLE credit_holds
  ADD COLUMN IF NOT EXISTS region text CHECK (region IN ('cn', 'us')),
  ADD COLUMN IF NOT EXISTS currency text CHECK (currency IN ('CNY', 'USD')),
  ADD COLUMN IF NOT EXISTS gateway_version text,
  ADD COLUMN IF NOT EXISTS commit_attempt integer NOT NULL DEFAULT 0 CHECK (commit_attempt >= 0),
  ADD COLUMN IF NOT EXISTS committed_by text,
  ADD COLUMN IF NOT EXISTS committed_at timestamptz,
  ADD COLUMN IF NOT EXISTS upstream_ref text,
  ADD COLUMN IF NOT EXISTS reconcile_deadline timestamptz,
  ADD COLUMN IF NOT EXISTS official_amount_json jsonb,
  ADD COLUMN IF NOT EXISTS base_amount_json jsonb,
  ADD COLUMN IF NOT EXISTS actual_usage_json jsonb;

CREATE INDEX credit_holds_reconcile_idx
  ON credit_holds (status, expires_at)
  WHERE status IN ('reserved', 'dispatching', 'in_flight', 'reconciling');

CREATE TABLE platform_cost_differences (
  id uuid PRIMARY KEY,
  hold_id uuid NOT NULL REFERENCES credit_holds(id),
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  user_id uuid NOT NULL REFERENCES users(id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  reason text NOT NULL CHECK (reason IN ('overage', 'reconcile_timeout', 'late_evidence', 'failed_attempt')),
  expected_units bigint NOT NULL CHECK (expected_units >= 0),
  settled_units bigint NOT NULL CHECK (settled_units >= 0),
  diff_units bigint NOT NULL,
  details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (hold_id, reason)
);

CREATE TABLE lago_sync_records (
  id uuid PRIMARY KEY,
  order_id uuid NOT NULL REFERENCES commerce_orders(id) UNIQUE,
  lago_customer_id text NOT NULL,
  lago_invoice_id text,
  status text NOT NULL CHECK (status IN ('pending', 'synced', 'failed')),
  payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  response_json jsonb,
  last_error text,
  updated_at timestamptz NOT NULL DEFAULT now()
);
