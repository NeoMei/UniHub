CREATE TABLE application_integrations (
    product_id text NOT NULL,
    instance_id text NOT NULL,
    name text NOT NULL,
    origin text NOT NULL,
    entry_url text NOT NULL,
    callback_url text NOT NULL,
    native_login_url text NOT NULL,
    client_id text NOT NULL UNIQUE,
    state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','pilot','active','paused')),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    credential_hash bytea UNIQUE CHECK (octet_length(credential_hash) = 32),
    pilot_started_at timestamptz,
    inbound_verified boolean NOT NULL DEFAULT false,
    outbound_verified boolean NOT NULL DEFAULT false,
    PRIMARY KEY (product_id, instance_id)
);
