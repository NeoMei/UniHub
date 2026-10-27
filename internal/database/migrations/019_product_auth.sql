-- Mature products retain their authenticators; only identity links and final
-- product authority are central. No password/OTP/Passkey secret is copied here.
ALTER TABLE product_user_access ADD COLUMN product_role integer NOT NULL DEFAULT 1 CHECK(product_role IN (1,10,100));
ALTER TABLE product_user_access ADD COLUMN permission_overrides jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(permission_overrides)='object');
CREATE TABLE product_auth_subjects (
 binding_id text PRIMARY KEY REFERENCES product_runtime_bindings(id),
 auth_version bigint NOT NULL CHECK(auth_version>0),
 registration_key text NOT NULL,
 registration_digest text NOT NULL,
 imported_batch text NOT NULL DEFAULT '',
 snapshot_digest text NOT NULL DEFAULT '',
 deleted boolean NOT NULL DEFAULT false
);
CREATE TABLE product_auth_assertions (
 token_hash text PRIMARY KEY,
 binding_id text NOT NULL REFERENCES product_auth_subjects(binding_id),
 authentication_id text NOT NULL,
 authentication_digest text NOT NULL,
 auth_version bigint NOT NULL,
 expires_at timestamptz NOT NULL,
 UNIQUE(binding_id,authentication_id)
);
CREATE INDEX product_auth_assertion_expiry ON product_auth_assertions(expires_at);
CREATE TABLE product_auth_events (
 binding_id text NOT NULL REFERENCES product_auth_subjects(binding_id),
 operation_id text NOT NULL,
 request_digest text NOT NULL,
 actor_id text NOT NULL REFERENCES users(id),
 event text NOT NULL,
 snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(binding_id,operation_id)
);
