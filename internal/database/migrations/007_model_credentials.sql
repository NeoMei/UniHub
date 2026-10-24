CREATE TABLE model_credentials (
 id text PRIMARY KEY,
 token_hash text NOT NULL UNIQUE,
 actor_id text NOT NULL REFERENCES users(id),
 account_id text NOT NULL REFERENCES accounts(id),
 origin_product_id text NOT NULL REFERENCES products(id),
 models jsonb NOT NULL,
 task_id text NOT NULL,
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_credentials_account ON model_credentials(account_id,actor_id);
