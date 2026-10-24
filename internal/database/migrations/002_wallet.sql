CREATE TABLE products (
    id text PRIMARY KEY CHECK (id ~ '^[a-z][a-z0-9_-]{0,63}$'),
    name text NOT NULL,
    enabled boolean NOT NULL DEFAULT true
);
INSERT INTO products(id,name) VALUES ('superwagie','SuperWagie'),('agentwiki','AgentWiki'),('llmrouter','llmrouter'),('xuanji','璇玑'),('lingxi','灵犀'),('xiashuo','虾说');

CREATE TABLE wallet_operations (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id),
    actor_id text NOT NULL REFERENCES users(id),
    kind text NOT NULL CHECK (kind IN ('transfer','recharge')),
    request_key text NOT NULL,
    payload_hash text NOT NULL,
    command jsonb NOT NULL,
    status text NOT NULL DEFAULT 'CREATED' CHECK (status IN ('CREATED','SUBMITTING','APPLIED','REJECTED','UNKNOWN','CANCELED')),
    ledger_transaction_id text,
    error_code text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(account_id,kind,request_key)
);
CREATE INDEX wallet_recovery ON wallet_operations(next_attempt_at,created_at) WHERE status IN ('CREATED','SUBMITTING','UNKNOWN');
CREATE TABLE recharge_orders (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id),
    actor_id text NOT NULL REFERENCES users(id),
    product_id text REFERENCES products(id),
    amount bigint NOT NULL CHECK (amount > 0 AND amount <= 1000000000000000 AND amount % 10000=0),
    currency text NOT NULL CHECK (currency='CNY'),
    merchant text NOT NULL,
    request_key text NOT NULL,
    payload_hash text NOT NULL,
    operation_id text UNIQUE REFERENCES wallet_operations(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(account_id,request_key)
);
CREATE TABLE payment_receipts (
    merchant text NOT NULL,
    transaction_id text NOT NULL,
    order_id text NOT NULL UNIQUE REFERENCES recharge_orders(id),
    payload_hash text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(merchant,transaction_id)
);
CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id),
    actor_id text REFERENCES users(id),
    operation_id text,
    action text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
