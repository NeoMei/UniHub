-- Platform grants are explicit central IDs, independent of products and providers.
CREATE TABLE platform_admin_roles (
 user_id text PRIMARY KEY REFERENCES users(id),
 roles jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(roles)='array' AND roles <@ '["super_admin","user_admin","finance_admin","auditor"]'::jsonb),
 legacy_user_admin boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 source text NOT NULL,
 granted_by text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE platform_admin_audit (
 id bigserial PRIMARY KEY,
 actor_id text NOT NULL,
 action text NOT NULL,
 target_id text NOT NULL DEFAULT '',
 reason text NOT NULL CHECK(length(trim(reason))>0),
 detail jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX platform_admin_audit_target ON platform_admin_audit(target_id,id);
CREATE INDEX platform_admin_audit_actor ON platform_admin_audit(actor_id,id);
ALTER TABLE users ADD COLUMN session_not_before timestamptz NOT NULL DEFAULT 'epoch';
ALTER TABLE users ADD COLUMN last_authenticated_at timestamptz;
CREATE INDEX users_directory_order ON users(created_at,id);
CREATE INDEX users_directory_status ON users(active,created_at,id);
CREATE INDEX identities_user ON identities(user_id);
CREATE INDEX browser_sessions_identity ON browser_sessions(issuer,subject);

-- Issuance and status changes share the user row lock. This closes a race where
-- a key passed its initial active check but was inserted after the revoke scan.
CREATE FUNCTION platform_guard_credential_issue() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE enabled boolean;
BEGIN
 SELECT active INTO enabled FROM users WHERE id=NEW.actor_id FOR SHARE;
 IF enabled IS DISTINCT FROM true THEN
  RAISE EXCEPTION 'credential actor disabled' USING ERRCODE='42501';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER platform_guard_credential_issue BEFORE INSERT ON model_credentials
 FOR EACH ROW EXECUTE FUNCTION platform_guard_credential_issue();
