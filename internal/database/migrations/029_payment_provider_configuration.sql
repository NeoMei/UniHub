CREATE TABLE payment_configuration_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 version bigint NOT NULL CHECK (version >= 0)
);
INSERT INTO payment_configuration_state(singleton,version) VALUES(true,0);

-- Ciphertext includes credentials and metadata, with provider/version AAD.
-- Retain revisions for original merchant/application checkout recovery.
CREATE TABLE payment_provider_revisions (
 provider text NOT NULL CHECK (provider IN ('wechat','alipay')),
 version bigint NOT NULL CHECK (version > 0),
 configuration_id text NOT NULL,
 encrypted_payload bytea NOT NULL CHECK (octet_length(encrypted_payload) BETWEEN 28 AND 2097152),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(provider,version)
);
CREATE INDEX payment_provider_identity_revision ON payment_provider_revisions(provider,configuration_id,version DESC);
