-- Passwords and provider tokens never enter UniHub persistence. Reservation rows
-- survive uncertain provider writes and reserve the organization-wide username.
CREATE TABLE identity_registrations (
 id text PRIMARY KEY,
 client_id text NOT NULL,
 username text NOT NULL UNIQUE,
 subject_id text NOT NULL UNIQUE,
 idempotency_hash text NOT NULL,
 metadata_hash text NOT NULL,
 status text NOT NULL CHECK (status IN ('UNKNOWN','CREATED','CONFLICT')),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (client_id,idempotency_hash)
);
CREATE TABLE identity_authorizations (
 id text PRIMARY KEY,
 client_id text NOT NULL,
 redirect_uri text NOT NULL,
 code_hash text NOT NULL,
 pkce_challenge text NOT NULL,
 nonce_hash text NOT NULL,
 state_hash text NOT NULL,
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (client_id,state_hash),
 UNIQUE (client_id,nonce_hash)
);
CREATE INDEX identity_authorizations_expiry ON identity_authorizations(expires_at);
CREATE TABLE identity_rate_limits (
 key_hash text NOT NULL,
 window_start timestamptz NOT NULL,
 hits integer NOT NULL CHECK (hits>0),
 PRIMARY KEY (key_hash,window_start)
);
CREATE INDEX identity_rate_limits_window ON identity_rate_limits(window_start);
