CREATE TABLE billing_product_credit_auto_addon_policies (
 account_id text NOT NULL REFERENCES accounts(id),
 product_id text NOT NULL REFERENCES products(id),
 snapshot jsonb NOT NULL,
 armed boolean NOT NULL DEFAULT true,
 PRIMARY KEY(account_id,product_id)
);

CREATE TABLE billing_product_credit_auto_addon_triggers (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account_id text NOT NULL,
 product_id text NOT NULL,
 policy_version bigint NOT NULL CHECK (policy_version > 0),
 source_id text NOT NULL,
 trigger_kind text NOT NULL CHECK(trigger_kind IN ('settled','insufficient','expired','funded')),
 minimum_credit bigint NOT NULL CHECK (minimum_credit >= 0),
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','PURCHASED','IGNORED','FAILED')),
 attempted_at timestamptz,
 FOREIGN KEY(account_id,product_id) REFERENCES billing_product_credit_auto_addon_policies(account_id,product_id),
 UNIQUE(account_id,product_id,policy_version,source_id,trigger_kind)
);
CREATE INDEX billing_product_credit_auto_addon_pending ON billing_product_credit_auto_addon_triggers(attempted_at NULLS FIRST, id) WHERE status='PENDING';
