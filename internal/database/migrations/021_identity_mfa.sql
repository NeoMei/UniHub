-- Pending second-factor login continuations. Only durable, hashed bindings are
-- stored; the upstream session handle is provider-scoped, short-lived and
-- single-use, and rows expire faster than one authorization code.
CREATE TABLE identity_mfa_pending (
    id_hash text PRIMARY KEY,
    client_id text NOT NULL,
    redirect_uri text NOT NULL,
    state_hash text NOT NULL,
    nonce_hash text NOT NULL,
    pkce_challenge text NOT NULL,
    provider_session text NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX identity_mfa_pending_expiry ON identity_mfa_pending(expires_at);
