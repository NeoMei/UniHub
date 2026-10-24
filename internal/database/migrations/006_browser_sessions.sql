-- Browser credentials are random opaque handles; only their SHA-256 digests
-- survive in storage. No access token or refresh token is persisted.
CREATE TABLE browser_sessions (
    id_hash text PRIMARY KEY,
    issuer text NOT NULL,
    subject text NOT NULL,
    name text NOT NULL,
    email text NOT NULL,
    csrf_token text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX browser_sessions_expiry ON browser_sessions(expires_at);

-- Short lived, one-use login transactions bind state to an HttpOnly browser
-- cookie. PKCE verifier and nonce disappear atomically before token exchange.
CREATE TABLE browser_login_transactions (
    state_hash text PRIMARY KEY,
    binding_hash text NOT NULL,
    nonce text NOT NULL,
    pkce_verifier text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX browser_login_transactions_expiry ON browser_login_transactions(expires_at);
