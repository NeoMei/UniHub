-- Asset quantities live exclusively in Formance. JSON snapshots below contain
-- immutable commercial terms/allocations, never mutable available balances.
CREATE TABLE billing_offers (id text PRIMARY KEY, product_id text NOT NULL REFERENCES products(id), status text NOT NULL CHECK(status IN ('draft','published')), snapshot jsonb NOT NULL);
CREATE TABLE billing_rates (id text PRIMARY KEY, status text NOT NULL CHECK(status='published'), snapshot jsonb NOT NULL);
CREATE TABLE billing_subscriptions (id text PRIMARY KEY, account_id text NOT NULL REFERENCES accounts(id), product_id text NOT NULL REFERENCES products(id), snapshot jsonb NOT NULL, UNIQUE(account_id,product_id));
CREATE TABLE billing_orders (id text PRIMARY KEY, account_id text NOT NULL REFERENCES accounts(id), actor_id text NOT NULL REFERENCES users(id), request_key text NOT NULL, request_digest text NOT NULL, snapshot jsonb NOT NULL, UNIQUE(account_id,request_key));
CREATE TABLE billing_quotes (id text PRIMARY KEY, account_id text NOT NULL REFERENCES accounts(id), snapshot jsonb NOT NULL);
CREATE TABLE billing_authorizations (account_id text NOT NULL REFERENCES accounts(id), product_id text NOT NULL REFERENCES products(id), purpose text NOT NULL CHECK(purpose IN ('funding','renewal')), snapshot jsonb NOT NULL, PRIMARY KEY(account_id,product_id,purpose));
CREATE TABLE billing_batches (id text PRIMARY KEY, account_id text NOT NULL REFERENCES accounts(id), order_id text NOT NULL REFERENCES billing_orders(id), source_key text NOT NULL UNIQUE, snapshot jsonb NOT NULL);
CREATE INDEX billing_batches_account ON billing_batches(account_id);
CREATE TABLE billing_calls (id text PRIMARY KEY, account_id text NOT NULL REFERENCES accounts(id), request_digest text NOT NULL, snapshot jsonb NOT NULL);
CREATE TABLE billing_commands (id text PRIMARY KEY, account_id text NOT NULL REFERENCES accounts(id), actor_id text NOT NULL, kind text NOT NULL, request_key text NOT NULL, request_digest text NOT NULL, command jsonb NOT NULL, effect jsonb NOT NULL, status text NOT NULL CHECK(status IN ('CREATED','SUBMITTING','UNKNOWN','APPLIED','REJECTED','CANCELED')), error_code text NOT NULL DEFAULT '', ledger_transaction_id text, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(account_id,kind,request_key));
CREATE INDEX billing_commands_unresolved ON billing_commands(account_id,status);
CREATE TABLE billing_events (id bigserial PRIMARY KEY, account_id text NOT NULL REFERENCES accounts(id), actor_id text NOT NULL, entity_id text NOT NULL, action text NOT NULL, detail jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now());
