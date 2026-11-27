-- Stage 2b: wallets, payments, orders, subscriptions, seats, invitations,
-- credit batches, immutable ledgers, and durable credit holds.

CREATE TABLE wallet_accounts (
  account_id uuid PRIMARY KEY REFERENCES accounts(id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  available_minor bigint NOT NULL DEFAULT 0 CHECK (available_minor >= 0),
  arrears_minor bigint NOT NULL DEFAULT 0 CHECK (arrears_minor >= 0),
  frozen_reason text,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE payment_orders (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  initiator_user_id uuid NOT NULL REFERENCES users(id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  provider text NOT NULL CHECK (provider IN ('wechat', 'alipay', 'stripe')),
  merchant_id text NOT NULL,
  provider_trade_no text,
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  refundable_minor bigint NOT NULL DEFAULT 0 CHECK (refundable_minor >= 0 AND refundable_minor <= amount_minor),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed', 'refunded', 'chargeback')),
  pending_checkout_json jsonb,
  return_url text,
  paid_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, merchant_id, provider_trade_no)
);

CREATE TABLE cash_ledger_entries (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  entry_type text NOT NULL CHECK (entry_type IN (
    'recharge', 'purchase', 'upgrade_return', 'upgrade_return_adjust',
    'original_refund', 'chargeback_debit', 'arrears_repay'
  )),
  delta_minor bigint NOT NULL,
  balance_after_minor bigint NOT NULL CHECK (balance_after_minor >= 0),
  arrears_after_minor bigint NOT NULL CHECK (arrears_after_minor >= 0),
  ref_type text NOT NULL,
  ref_id uuid NOT NULL,
  idempotency_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE commerce_orders (
  id uuid PRIMARY KEY,
  order_no text NOT NULL UNIQUE,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  buyer_user_id uuid NOT NULL REFERENCES users(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  order_type text NOT NULL CHECK (order_type IN ('subscribe', 'renew', 'upgrade', 'seat_expand', 'credit_pack')),
  commerce_version_id uuid NOT NULL REFERENCES commerce_versions(id),
  card_tier_id text NOT NULL,
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity > 0),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  paid_minor bigint NOT NULL CHECK (paid_minor >= 0),
  refunded_minor bigint NOT NULL DEFAULT 0 CHECK (refunded_minor >= 0 AND refunded_minor <= paid_minor),
  snapshot_json jsonb NOT NULL,
  status text NOT NULL CHECK (status IN ('confirmed', 'fulfilled')),
  lago_invoice_id text,
  lago_sync_status text NOT NULL DEFAULT 'skipped' CHECK (lago_sync_status IN ('skipped', 'pending', 'synced', 'failed')),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE billing_outbox (
  id uuid PRIMARY KEY,
  dedupe_key text NOT NULL UNIQUE,
  topic text NOT NULL CHECK (topic IN ('grant_entitlement', 'lago_invoice_sync', 'renewal_notice')),
  payload_json jsonb NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'dead')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_retry_at timestamptz NOT NULL DEFAULT now(),
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX billing_outbox_poll_idx ON billing_outbox (status, next_retry_at) WHERE status = 'pending';

CREATE TABLE app_subscriptions (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  commerce_version_id uuid NOT NULL REFERENCES commerce_versions(id),
  source_order_id uuid NOT NULL REFERENCES commerce_orders(id),
  tier_id text NOT NULL,
  tier_rank integer NOT NULL,
  billing_cycle text NOT NULL CHECK (billing_cycle IN ('monthly', 'yearly')),
  seat_count integer NOT NULL DEFAULT 1 CHECK (seat_count >= 1),
  status text NOT NULL CHECK (status IN ('active', 'expired', 'superseded')),
  auto_renew boolean NOT NULL DEFAULT false,
  authorized_renew_price_minor bigint,
  period_start timestamptz NOT NULL,
  period_end timestamptz NOT NULL,
  current_grant_cycle_index integer NOT NULL DEFAULT 0 CHECK (current_grant_cycle_index >= 0),
  scheduled_change_json jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (period_end > period_start),
  CHECK (authorized_renew_price_minor IS NULL OR authorized_renew_price_minor >= 0)
);

CREATE UNIQUE INDEX app_subscriptions_one_active
  ON app_subscriptions (account_id, app_id) WHERE status = 'active';

CREATE TABLE enterprise_app_seats (
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  subscription_id uuid NOT NULL REFERENCES app_subscriptions(id),
  user_id uuid NOT NULL REFERENCES users(id),
  assigned_by uuid NOT NULL REFERENCES users(id),
  assigned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, app_id, subscription_id, user_id),
  FOREIGN KEY (account_id, user_id) REFERENCES enterprise_members(account_id, user_id) ON DELETE CASCADE
);

CREATE INDEX enterprise_app_seats_user_idx
  ON enterprise_app_seats (account_id, app_id, user_id) WHERE assigned_at IS NOT NULL;

CREATE TABLE enterprise_member_limits (
  account_id uuid NOT NULL,
  app_id uuid NOT NULL,
  user_id uuid NOT NULL,
  cycle_limit_units bigint CHECK (cycle_limit_units IS NULL OR cycle_limit_units >= 0),
  PRIMARY KEY (account_id, app_id, user_id),
  FOREIGN KEY (account_id, user_id) REFERENCES enterprise_members(account_id, user_id) ON DELETE CASCADE
);

CREATE TABLE enterprise_member_cycle_usage (
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  user_id uuid NOT NULL REFERENCES users(id),
  subscription_id uuid NOT NULL REFERENCES app_subscriptions(id),
  cycle_index integer NOT NULL CHECK (cycle_index >= 0),
  held_units bigint NOT NULL DEFAULT 0 CHECK (held_units >= 0),
  settled_units bigint NOT NULL DEFAULT 0 CHECK (settled_units >= 0),
  PRIMARY KEY (account_id, app_id, user_id, subscription_id, cycle_index)
);

CREATE TABLE enterprise_invites (
  token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  role text NOT NULL CHECK (role IN ('admin', 'member')),
  created_by uuid NOT NULL REFERENCES users(id),
  expires_at timestamptz NOT NULL,
  claimed_by uuid REFERENCES users(id),
  claimed_at timestamptz,
  CHECK ((claimed_by IS NULL) = (claimed_at IS NULL))
);

CREATE TABLE credit_batches (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  source_type text NOT NULL CHECK (source_type IN ('plan_base', 'plan_bonus', 'credit_pack')),
  source_order_id uuid NOT NULL REFERENCES commerce_orders(id),
  subscription_id uuid REFERENCES app_subscriptions(id),
  grant_cycle_index integer,
  granted_units bigint NOT NULL CHECK (granted_units >= 0),
  available_units bigint NOT NULL CHECK (available_units >= 0),
  held_units bigint NOT NULL DEFAULT 0 CHECK (held_units >= 0),
  settled_units bigint NOT NULL DEFAULT 0 CHECK (settled_units >= 0),
  expired_units bigint NOT NULL DEFAULT 0 CHECK (expired_units >= 0),
  frozen_for_new_holds boolean NOT NULL DEFAULT false,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (granted_units = available_units + held_units + settled_units + expired_units),
  CHECK (grant_cycle_index IS NULL OR grant_cycle_index >= 0)
);

CREATE INDEX credit_batches_consume_idx
  ON credit_batches (account_id, app_id, expires_at ASC, id ASC)
  WHERE available_units > 0 AND NOT frozen_for_new_holds;

CREATE TABLE credit_ledger_entries (
  id uuid PRIMARY KEY,
  batch_id uuid NOT NULL REFERENCES credit_batches(id),
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  entry_type text NOT NULL CHECK (entry_type IN (
    'grant', 'hold', 'settle', 'release', 'expire', 'upgrade_reclaim'
  )),
  delta_available_units bigint NOT NULL,
  delta_held_units bigint NOT NULL DEFAULT 0,
  delta_settled_units bigint NOT NULL DEFAULT 0,
  delta_expired_units bigint NOT NULL DEFAULT 0,
  ref_id text NOT NULL,
  idempotency_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE credit_holds (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  user_id uuid NOT NULL REFERENCES users(id),
  enterprise_subscription_id uuid REFERENCES app_subscriptions(id),
  enterprise_cycle_index integer CHECK (enterprise_cycle_index IS NULL OR enterprise_cycle_index >= 0),
  request_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('reserved', 'dispatching', 'in_flight', 'reconciling', 'settled', 'released')),
  authorized_units bigint NOT NULL CHECK (authorized_units > 0),
  settled_units bigint NOT NULL DEFAULT 0 CHECK (settled_units >= 0),
  released_units bigint NOT NULL DEFAULT 0 CHECK (released_units >= 0),
  pricing_snapshot jsonb NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  settled_at timestamptz,
  UNIQUE (account_id, request_id),
  CHECK (settled_units + released_units <= authorized_units)
);

CREATE TABLE credit_hold_batches (
  hold_id uuid NOT NULL REFERENCES credit_holds(id) ON DELETE CASCADE,
  batch_id uuid NOT NULL REFERENCES credit_batches(id),
  held_units bigint NOT NULL CHECK (held_units > 0),
  settled_units bigint NOT NULL DEFAULT 0 CHECK (settled_units >= 0),
  released_units bigint NOT NULL DEFAULT 0 CHECK (released_units >= 0),
  PRIMARY KEY (hold_id, batch_id),
  CHECK (settled_units + released_units <= held_units)
);

CREATE TABLE subscription_upgrades (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL REFERENCES wallet_accounts(account_id),
  app_id uuid NOT NULL REFERENCES apps(id),
  old_subscription_id uuid NOT NULL REFERENCES app_subscriptions(id),
  old_order_id uuid NOT NULL REFERENCES commerce_orders(id),
  new_subscription_id uuid NOT NULL REFERENCES app_subscriptions(id),
  new_order_id uuid NOT NULL REFERENCES commerce_orders(id),
  old_current_batch_id uuid REFERENCES credit_batches(id),
  p_current_minor bigint NOT NULL CHECK (p_current_minor >= 0),
  f_future_minor bigint NOT NULL CHECK (f_future_minor >= 0),
  b_base_units bigint NOT NULL CHECK (b_base_units >= 0),
  c_cap_minor bigint NOT NULL CHECK (c_cap_minor >= 0),
  returned_so_far_minor bigint NOT NULL DEFAULT 0 CHECK (returned_so_far_minor >= 0),
  finalized_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (old_subscription_id)
);

CREATE OR REPLACE FUNCTION forbid_billing_ledger_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'billing ledger entries are immutable';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_cash_ledger_immutable
  BEFORE UPDATE OR DELETE ON cash_ledger_entries
  FOR EACH ROW EXECUTE FUNCTION forbid_billing_ledger_mutation();

CREATE TRIGGER trg_credit_ledger_immutable
  BEFORE UPDATE OR DELETE ON credit_ledger_entries
  FOR EACH ROW EXECUTE FUNCTION forbid_billing_ledger_mutation();

CREATE OR REPLACE FUNCTION ensure_wallet_account() RETURNS trigger AS $$
BEGIN
  INSERT INTO wallet_accounts (account_id, region, currency)
  SELECT NEW.id, region, currency FROM deployment_region WHERE singleton
  ON CONFLICT (account_id) DO NOTHING;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_account_wallet
  AFTER INSERT ON accounts
  FOR EACH ROW EXECUTE FUNCTION ensure_wallet_account();
