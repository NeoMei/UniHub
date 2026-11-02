-- UniHub refresh tokens are opaque, one-use, and stored only as hashes. The
-- upstream provider refresh credential is sealed with the private identity key
-- so access renewal survives a restart without putting a raw provider token in
-- the database.
CREATE TABLE identity_refresh_tokens (
 token_hash text PRIMARY KEY,
 family_hash text NOT NULL,
 client_id text NOT NULL,
 subject text NOT NULL,
 upstream_refresh_ciphertext text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 used_at timestamptz,
 revoked_at timestamptz,
 replaced_by text
);
CREATE INDEX identity_refresh_tokens_family ON identity_refresh_tokens(family_hash);
CREATE INDEX identity_refresh_tokens_expiry ON identity_refresh_tokens(expires_at);
