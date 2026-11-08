-- Unpaid checkouts intentionally do not enter billing_commands: they serialize
-- commercial purchases only, never ordinary model calls.
CREATE TABLE billing_checkouts (
 id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9]{1,32}$'),
 account_id text NOT NULL REFERENCES accounts(id),
 product_id text NOT NULL REFERENCES products(id),
 request_key text NOT NULL,
 request_digest text NOT NULL,
 snapshot jsonb NOT NULL,
 UNIQUE(account_id,request_key)
);
CREATE INDEX billing_checkouts_account ON billing_checkouts(account_id);
CREATE TABLE billing_payment_receipts (
 checkout_id text PRIMARY KEY REFERENCES billing_checkouts(id),
 provider text NOT NULL,
 merchant_id text NOT NULL,
 transaction_id text NOT NULL,
 snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(provider,merchant_id,transaction_id)
);
