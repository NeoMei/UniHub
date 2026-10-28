ALTER TABLE account_members ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE accounts ADD COLUMN admin_version bigint NOT NULL DEFAULT 1 CHECK(admin_version>0);
CREATE FUNCTION platform_team_membership_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE accounts SET admin_version=admin_version+1 WHERE id=COALESCE(NEW.account_id,OLD.account_id);
 RETURN COALESCE(NEW,OLD);
END $$;
CREATE TRIGGER platform_team_member_version AFTER INSERT OR UPDATE OR DELETE ON account_members FOR EACH ROW EXECUTE FUNCTION platform_team_membership_version();
-- Account-origin invitations retain their original authorization path and table.
-- Platform invitations cannot be consumed by the account-origin acceptance path.
CREATE TABLE platform_team_invitations (
 id text PRIMARY KEY,
 account_id text NOT NULL REFERENCES accounts(id),
 recipient_id text NOT NULL REFERENCES users(id),
 invited_by text NOT NULL REFERENCES users(id),
 role text NOT NULL CHECK(role IN ('member','admin','billing_admin','owner')),
 authorization_source text NOT NULL DEFAULT 'platform' CHECK(authorization_source='platform'),
 transfer_owner_id text REFERENCES users(id),
 reason text NOT NULL CHECK(length(reason)>0),
 version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days',
 accepted_at timestamptz,
 revoked_at timestamptz,
 CHECK(transfer_owner_id IS NULL OR role='owner')
);
CREATE UNIQUE INDEX platform_team_invitation_pending ON platform_team_invitations(account_id,recipient_id) WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX platform_team_invitation_recipient ON platform_team_invitations(recipient_id,created_at,id);
CREATE TABLE platform_identity_operations (
 id text PRIMARY KEY,
 actor_id text NOT NULL REFERENCES users(id),
 kind text NOT NULL CHECK(kind IN ('create','display_name','recovery','activation','redeem','sessions')),
 issuer text NOT NULL,
 subject text NOT NULL DEFAULT '',
 target_id text REFERENCES users(id),
 request_hash text NOT NULL,
 state text NOT NULL CHECK(state IN ('pending','succeeded','unknown','failed','unsupported')),
 result jsonb NOT NULL DEFAULT '{}',
 reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX platform_identity_operation_target ON platform_identity_operations(target_id,created_at,id);
CREATE TABLE platform_identity_recovery (
 id text PRIMARY KEY REFERENCES platform_identity_operations(id),
 issuer text NOT NULL,
 subject text NOT NULL,
 user_id text NOT NULL REFERENCES users(id),
 purpose text NOT NULL CHECK(purpose IN ('recovery','activation')),
 token_hash text NOT NULL UNIQUE,
 verified_channel_hash text NOT NULL,
 version bigint NOT NULL DEFAULT 1,
 state text NOT NULL CHECK(state IN ('delivering','delivered','unknown','redeeming','consumed','failed')),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE platform_recovery_rate_limits (
 key_hash text NOT NULL,
 window_start timestamptz NOT NULL,
 hits integer NOT NULL,
 PRIMARY KEY(key_hash,window_start)
);
