-- Customer fee waiver evidence is independent of unresolved supplier liability.
CREATE TABLE billing_platform_fault_waivers (
 case_id text PRIMARY KEY,
 call_id text NOT NULL UNIQUE REFERENCES billing_calls(id),
 account_id text NOT NULL REFERENCES accounts(id),
 operator_id text NOT NULL,
 approval_sha256 text NOT NULL CHECK(length(approval_sha256)=64),
 approval_raw bytea NOT NULL,
 original_central_row jsonb NOT NULL,
 original_commands jsonb NOT NULL,
 release_command_id text NOT NULL UNIQUE REFERENCES billing_commands(id),
 provider_state text NOT NULL CHECK(provider_state='PROVIDER_UNRESOLVED'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION reject_platform_fault_waiver_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'platform fault waiver decisions are append-only' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER billing_platform_fault_waivers_immutable BEFORE UPDATE OR DELETE ON billing_platform_fault_waivers FOR EACH ROW EXECUTE FUNCTION reject_platform_fault_waiver_rewrite();
