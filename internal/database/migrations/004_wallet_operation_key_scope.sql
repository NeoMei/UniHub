-- Earlier development schemas applied migration 002 with one shared namespace
-- for public transfer keys and generated receipt keys. Preserve those operations
-- while moving uniqueness to the operation kind. Fresh schemas already have the
-- scoped constraint; this migration is safe for either migration-002 variant.
ALTER TABLE wallet_operations
    DROP CONSTRAINT IF EXISTS wallet_operations_account_id_request_key_key;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'wallet_operations'::regclass
          AND conname = 'wallet_operations_account_id_kind_request_key_key'
    ) THEN
        ALTER TABLE wallet_operations
            ADD CONSTRAINT wallet_operations_account_id_kind_request_key_key
            UNIQUE (account_id, kind, request_key);
    END IF;
END
$$;
