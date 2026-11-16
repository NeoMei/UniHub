-- Real-money cash wallet topups. Distinct from billing_checkouts, which settle
-- commercial entitlements and never credit a user cash balance.
CREATE TABLE cash_checkouts (
    id text PRIMARY KEY CHECK (id ~ '^ct_[A-Za-z0-9]{1,32}$'),
    account_id text NOT NULL REFERENCES accounts(id),
    actor_id text NOT NULL REFERENCES users(id),
    product_id text REFERENCES products(id),
    amount bigint NOT NULL CHECK (amount > 0 AND amount <= 1000000000000000 AND amount % 10000 = 0),
    currency text NOT NULL CHECK (currency = 'CNY'),
    provider text NOT NULL CHECK (provider IN ('wechat','alipay')),
    merchant_id text NOT NULL,
    merchant_version text NOT NULL,
    request_key text NOT NULL,
    request_digest text NOT NULL,
    snapshot jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (account_id, request_key)
);
CREATE INDEX cash_checkouts_account ON cash_checkouts(account_id);

CREATE TABLE cash_payment_receipts (
    checkout_id text PRIMARY KEY REFERENCES cash_checkouts(id),
    provider text NOT NULL,
    merchant_id text NOT NULL,
    transaction_id text NOT NULL,
    snapshot jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (provider, merchant_id, transaction_id)
);

-- cash_topup is a distinct kind so the worker posts world->cash without the
-- sandbox-only recharge authorization gate.
ALTER TABLE wallet_operations DROP CONSTRAINT IF EXISTS wallet_operations_kind_check;
ALTER TABLE wallet_operations ADD CONSTRAINT wallet_operations_kind_check
    CHECK (kind IN ('transfer','recharge','admin_transfer','cash_topup'));
