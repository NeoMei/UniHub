-- Native lock belongs to the authenticator binding, independent of central grants.
-- The existing deleted flag remains the irreversible tombstone.
ALTER TABLE product_auth_subjects ADD COLUMN native_locked boolean NOT NULL DEFAULT false;
