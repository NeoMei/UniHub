ALTER TABLE product_browser_handoffs
    ADD COLUMN native_session_id text CHECK (native_session_id ~ '^[0-9a-f]{64}$');

ALTER TABLE product_browser_session_origins
    ADD COLUMN native_session_id text CHECK (native_session_id ~ '^[0-9a-f]{64}$');

CREATE INDEX product_browser_session_native_origin
    ON product_browser_session_origins(binding_id,native_session_id)
    WHERE native_session_id IS NOT NULL;

CREATE TABLE product_browser_native_session_revocations (
    binding_id text NOT NULL REFERENCES product_auth_subjects(binding_id) ON DELETE CASCADE,
    native_session_id text NOT NULL CHECK (native_session_id ~ '^[0-9a-f]{64}$'),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (binding_id,native_session_id)
);
