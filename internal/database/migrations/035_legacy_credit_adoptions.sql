-- Adoption preserves the ledger addresses, monetary receipts and original lot times.
CREATE TABLE billing_legacy_credit_adoption_runs (
 plan_digest text PRIMARY KEY CHECK (plan_digest ~ '^[0-9a-f]{64}$'),
 account_id text NOT NULL REFERENCES accounts(id),
 product_id text NOT NULL REFERENCES products(id),
 actor_id text NOT NULL REFERENCES users(id),
 receipt jsonb NOT NULL
);
CREATE TABLE billing_legacy_credit_adopted_batches (
 batch_id text PRIMARY KEY REFERENCES billing_batches(id),
 account_id text NOT NULL REFERENCES accounts(id),
 product_id text NOT NULL REFERENCES products(id),
 order_id text NOT NULL REFERENCES billing_orders(id),
 plan_digest text NOT NULL REFERENCES billing_legacy_credit_adoption_runs(plan_digest)
);
