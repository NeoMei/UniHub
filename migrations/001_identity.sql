CREATE TABLE deployment_region (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  region text NOT NULL,
  currency text NOT NULL,
  CHECK ((region, currency) IN (('cn', 'CNY'), ('us', 'USD')))
);

CREATE TABLE users (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  email text NOT NULL
);

CREATE TABLE oidc_identities (
  issuer text NOT NULL,
  subject text NOT NULL,
  user_id uuid NOT NULL REFERENCES users(id),
  PRIMARY KEY (issuer, subject)
);

CREATE TABLE accounts (
  id uuid PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN ('personal', 'enterprise')),
  name text NOT NULL,
  owner_id uuid NOT NULL REFERENCES users(id)
);

CREATE UNIQUE INDEX accounts_personal_owner ON accounts(owner_id) WHERE kind = 'personal';

CREATE TABLE enterprise_members (
  account_id uuid NOT NULL REFERENCES accounts(id),
  user_id uuid NOT NULL REFERENCES users(id),
  role text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
  can_recharge boolean NOT NULL DEFAULT false,
  can_purchase boolean NOT NULL DEFAULT false,
  has_seat boolean NOT NULL DEFAULT false,
  PRIMARY KEY (account_id, user_id)
);

CREATE INDEX enterprise_members_user ON enterprise_members(user_id);

CREATE TABLE portal_sessions (
  id uuid PRIMARY KEY,
  issuer text NOT NULL,
  subject text NOT NULL,
  active_account_id uuid NOT NULL REFERENCES accounts(id),
  csrf text NOT NULL,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  FOREIGN KEY (issuer, subject) REFERENCES oidc_identities(issuer, subject)
);

CREATE TABLE apps (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  client_secret_hash bytea NOT NULL CHECK (octet_length(client_secret_hash) = 32),
  redirect_uris text[] NOT NULL CHECK (cardinality(redirect_uris) BETWEEN 1 AND 20),
  enabled boolean NOT NULL DEFAULT true
);

CREATE TABLE app_operators (
  app_id uuid NOT NULL REFERENCES apps(id),
  user_id uuid NOT NULL REFERENCES users(id),
  PRIMARY KEY (app_id, user_id)
);

CREATE TABLE app_login_codes (
  code_hash bytea PRIMARY KEY CHECK (octet_length(code_hash) = 32),
  app_id uuid NOT NULL REFERENCES apps(id),
  session_id uuid NOT NULL REFERENCES portal_sessions(id),
  redirect_uri text NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz
);
