-- Generic browser codes leave these fields unset. Identity codes use the
-- destination binding and versions that were current at authorization.
ALTER TABLE product_browser_codes
    ADD COLUMN destination_binding_id text REFERENCES product_auth_subjects(binding_id) ON DELETE CASCADE,
    ADD COLUMN destination_assignment_version bigint CHECK (destination_assignment_version > 0),
    ADD COLUMN destination_status_version bigint CHECK (destination_status_version > 0),
    ADD COLUMN destination_auth_version bigint CHECK (destination_auth_version > 0),
    ADD CONSTRAINT browser_code_destination_epoch_complete CHECK (
        num_nonnulls(destination_binding_id, destination_assignment_version,
            destination_status_version, destination_auth_version) IN (0, 4)
    );
