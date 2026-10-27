ALTER TABLE users ADD COLUMN active boolean NOT NULL DEFAULT true;
ALTER TABLE users ADD COLUMN status_version bigint NOT NULL DEFAULT 1 CHECK(status_version > 0);
-- This identity directory intentionally has no foreign key to billing products.
CREATE TABLE product_user_access (
 user_id text NOT NULL REFERENCES users(id),
 product_id text NOT NULL,
 instance_id text NOT NULL,
 roles jsonb NOT NULL CHECK(jsonb_typeof(roles)='array'),
 active boolean NOT NULL,
 version bigint NOT NULL CHECK(version > 0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,product_id,instance_id)
);
CREATE TABLE product_user_audit (
 id bigserial PRIMARY KEY,
 actor_id text NOT NULL REFERENCES users(id),
 target_id text NOT NULL REFERENCES users(id),
 product_id text NOT NULL DEFAULT '',
 instance_id text NOT NULL DEFAULT '',
 action text NOT NULL CHECK(action IN ('access','status')),
 version bigint NOT NULL,
 snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX product_user_audit_target_idx ON product_user_audit(target_id,id);
