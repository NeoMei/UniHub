-- Existing browser source proofs stay in the generic token namespace. Their
-- destination epoch is unknown, so they cannot verify as identity proofs.
ALTER TABLE product_browser_source_proofs
    ADD COLUMN destination_binding_id text REFERENCES product_auth_subjects(binding_id) ON DELETE CASCADE,
    ADD COLUMN destination_assignment_version bigint CHECK (destination_assignment_version > 0),
    ADD COLUMN destination_status_version bigint CHECK (destination_status_version > 0),
    ADD COLUMN destination_auth_version bigint CHECK (destination_auth_version > 0),
    ADD CONSTRAINT browser_source_proof_destination_epoch_complete CHECK (
        num_nonnulls(destination_binding_id, destination_assignment_version,
            destination_status_version, destination_auth_version) IN (0, 4)
    );
