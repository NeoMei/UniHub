CREATE TABLE users (
    id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE identities (
    issuer text NOT NULL CHECK (issuer <> ''),
    subject text NOT NULL CHECK (subject <> ''),
    user_id text NOT NULL REFERENCES users(id),
    PRIMARY KEY (issuer, subject)
);
CREATE TABLE accounts (
    id text PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('personal', 'team')),
    name text NOT NULL,
    personal_user_id text UNIQUE REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'personal') = (personal_user_id IS NOT NULL))
);
CREATE TABLE account_members (
    account_id text NOT NULL REFERENCES accounts(id),
    user_id text NOT NULL REFERENCES users(id),
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'billing_admin', 'member')),
    PRIMARY KEY (account_id, user_id)
);
CREATE INDEX account_members_user_idx ON account_members(user_id);
CREATE TABLE account_invitations (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id),
    recipient_id text NOT NULL REFERENCES users(id),
    invited_by text NOT NULL REFERENCES users(id),
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'billing_admin', 'member')),
    created_at timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz
);
CREATE UNIQUE INDEX account_invitations_pending_idx ON account_invitations(account_id, recipient_id) WHERE accepted_at IS NULL;
