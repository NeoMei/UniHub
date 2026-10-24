ALTER TABLE account_invitations ADD COLUMN revoked_at timestamptz;
ALTER TABLE account_invitations ADD CONSTRAINT account_invitations_terminal_state
    CHECK (accepted_at IS NULL OR revoked_at IS NULL);
DROP INDEX account_invitations_pending_idx;
CREATE UNIQUE INDEX account_invitations_pending_idx
    ON account_invitations(account_id, recipient_id)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
