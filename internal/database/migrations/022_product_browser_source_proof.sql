ALTER TABLE product_browser_codes
    ADD COLUMN source_session_hash text REFERENCES browser_sessions(id_hash) ON DELETE CASCADE;

CREATE TABLE product_browser_source_proofs (
    token_hash text PRIMARY KEY,
    client_id text NOT NULL,
    user_id text NOT NULL REFERENCES users(id),
    source_session_hash text NOT NULL REFERENCES browser_sessions(id_hash) ON DELETE CASCADE,
    source_binding_id text REFERENCES product_auth_subjects(binding_id),
    source_auth_version bigint,
    product_id text NOT NULL,
    instance_id text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX product_browser_source_proofs_expiry
    ON product_browser_source_proofs(expires_at);
