CREATE TABLE product_browser_handoffs (
 id_hash text PRIMARY KEY, client_id text NOT NULL, state_hash text NOT NULL,
 browser_hash text NOT NULL, approved_user_id text REFERENCES users(id),
 source_binding_id text REFERENCES product_auth_subjects(binding_id),
 source_auth_version bigint, expires_at timestamptz NOT NULL,
 consumed boolean NOT NULL DEFAULT false
);
CREATE TABLE product_browser_codes (
 code_hash text PRIMARY KEY, client_id text NOT NULL, user_id text NOT NULL REFERENCES users(id),
 source_binding_id text REFERENCES product_auth_subjects(binding_id), source_auth_version bigint,
 pkce_challenge text NOT NULL, nonce_hash text NOT NULL, expires_at timestamptz NOT NULL,
 consumed boolean NOT NULL DEFAULT false
);
CREATE TABLE product_browser_proofs (
 token_hash text PRIMARY KEY, client_id text NOT NULL, user_id text NOT NULL REFERENCES users(id),
 source_binding_id text REFERENCES product_auth_subjects(binding_id), source_auth_version bigint,
 expires_at timestamptz NOT NULL, consumed boolean NOT NULL DEFAULT false
);
CREATE TABLE product_browser_session_origins (
 session_id_hash text PRIMARY KEY REFERENCES browser_sessions(id_hash) ON DELETE CASCADE,
 binding_id text NOT NULL REFERENCES product_auth_subjects(binding_id),
 auth_version bigint NOT NULL
);
