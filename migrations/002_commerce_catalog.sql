-- Stage 2a: Commerce catalog, immutable product & route-pricing versions,
-- user app authorizations, runtime non-secret settings, and per-app seat contract.

ALTER TABLE apps
  ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

CREATE TABLE user_app_authorizations (
  user_id uuid NOT NULL REFERENCES users(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  first_authorized_at timestamptz NOT NULL DEFAULT now(),
  last_authorized_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  PRIMARY KEY (user_id, app_id)
);

CREATE TABLE product_card_drafts (
  id uuid PRIMARY KEY,
  app_id uuid NOT NULL REFERENCES apps(id),
  account_kind text NOT NULL CHECK (account_kind IN ('personal', 'enterprise')),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  product_type text NOT NULL CHECK (product_type IN ('membership', 'credit_pack')),
  tier_id text NOT NULL CHECK (length(tier_id) BETWEEN 1 AND 64),
  tier_rank integer NOT NULL CHECK (tier_rank >= 0),
  publish_status text NOT NULL CHECK (publish_status IN ('new', 'published', 'modified', 'offline')),
  rule_json jsonb NOT NULL,
  display_json jsonb NOT NULL,
  updated_by uuid NOT NULL REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, account_kind, region, tier_id)
);

CREATE INDEX product_card_drafts_scope_idx
  ON product_card_drafts (app_id, account_kind, region, tier_rank ASC, id ASC);

CREATE TABLE commerce_versions (
  id uuid PRIMARY KEY,
  version_code text NOT NULL UNIQUE,
  app_id uuid NOT NULL REFERENCES apps(id),
  account_kind text NOT NULL CHECK (account_kind IN ('personal', 'enterprise')),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  effective_at timestamptz NOT NULL,
  cards_snapshot jsonb NOT NULL CHECK (jsonb_typeof(cards_snapshot) = 'array'),
  change_summary text NOT NULL DEFAULT '',
  published_by uuid NOT NULL REFERENCES users(id),
  published_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, account_kind, region, effective_at)
);

CREATE INDEX commerce_versions_resolve_idx
  ON commerce_versions (app_id, account_kind, region, effective_at DESC);

CREATE TABLE route_pricing_versions (
  id uuid PRIMARY KEY,
  version_code text NOT NULL UNIQUE,
  app_id uuid NOT NULL REFERENCES apps(id),
  account_kind text NOT NULL CHECK (account_kind IN ('personal', 'enterprise')),
  region text NOT NULL CHECK (region IN ('cn', 'us')),
  currency text NOT NULL CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD'))),
  effective_at timestamptz NOT NULL,
  credits_per_currency integer NOT NULL CHECK (credits_per_currency > 0),
  fx_version text NOT NULL DEFAULT 'fx_native_v1',
  routes_snapshot jsonb NOT NULL CHECK (jsonb_typeof(routes_snapshot) = 'array'),
  published_by uuid NOT NULL REFERENCES users(id),
  published_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (app_id, account_kind, region, effective_at)
);

CREATE INDEX route_pricing_versions_resolve_idx
  ON route_pricing_versions (app_id, account_kind, region, effective_at DESC);

CREATE OR REPLACE FUNCTION forbid_version_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'published version snapshot is immutable';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_commerce_versions_immutable
  BEFORE UPDATE OR DELETE ON commerce_versions
  FOR EACH ROW EXECUTE FUNCTION forbid_version_mutation();

CREATE TRIGGER trg_route_pricing_versions_immutable
  BEFORE UPDATE OR DELETE ON route_pricing_versions
  FOR EACH ROW EXECUTE FUNCTION forbid_version_mutation();

-- Stage 2a per-app enterprise seat query contract (order-bound allocation enforced in Stage 2b).
CREATE TABLE enterprise_app_seat_contracts (
  account_id uuid NOT NULL REFERENCES accounts(id),
  app_id uuid NOT NULL REFERENCES apps(id),
  subscription_id uuid NOT NULL,
  user_id uuid NOT NULL REFERENCES users(id),
  granted_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  PRIMARY KEY (account_id, app_id, subscription_id, user_id),
  FOREIGN KEY (account_id, user_id) REFERENCES enterprise_members(account_id, user_id) ON DELETE CASCADE
);

CREATE INDEX enterprise_app_seat_contracts_active_idx
  ON enterprise_app_seat_contracts (account_id, app_id, user_id)
  WHERE revoked_at IS NULL;

CREATE TABLE runtime_settings (
  key text PRIMARY KEY,
  value_json jsonb NOT NULL,
  updated_by uuid REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now()
);
