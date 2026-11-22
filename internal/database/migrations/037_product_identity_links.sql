CREATE TABLE product_identity_links (
    id_hash text PRIMARY KEY,
    client_id text NOT NULL,
    product_id text NOT NULL,
    instance_id text NOT NULL,
    local_user_id text NOT NULL,
    auth_version bigint NOT NULL CHECK (auth_version > 0),
    native_auth_id text NOT NULL,
    state_hash text NOT NULL,
    nonce_hash text NOT NULL,
    pkce_challenge text NOT NULL,
    expires_at timestamptz NOT NULL,
    code_hash text,
    central_user_id text REFERENCES users(id),
    source_session_hash text,
    completed_digest text,
    UNIQUE (product_id, instance_id, native_auth_id)
);

CREATE INDEX product_identity_links_expiry ON product_identity_links(expires_at);
