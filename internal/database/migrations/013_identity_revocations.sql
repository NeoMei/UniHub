CREATE TABLE identity_revocations (
    id text PRIMARY KEY,
    issuer text NOT NULL,
    subject text NOT NULL,
    revoked_after timestamptz NOT NULL,
    pending boolean NOT NULL DEFAULT true
);
CREATE INDEX identity_revocations_identity ON identity_revocations(issuer, subject, revoked_after);
