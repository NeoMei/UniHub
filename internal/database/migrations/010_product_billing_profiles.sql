-- Append-only product commercial policy. Version zero is the implicit legacy
-- profile; operators and deployments can only publish positive versions.
CREATE TABLE product_billing_profiles (
    product_id text NOT NULL REFERENCES products(id),
    version bigint NOT NULL CHECK (version > 0),
    actor_id text NOT NULL CHECK (actor_id <> ''),
    snapshot jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (product_id,version),
    CHECK (snapshot->>'product_id' = product_id),
    CHECK ((snapshot->>'version')::bigint = version)
);

CREATE FUNCTION reject_billing_profile_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'product billing profiles are append-only' USING ERRCODE = '23514';
END;
$$;
CREATE TRIGGER product_billing_profiles_immutable
    BEFORE UPDATE OR DELETE ON product_billing_profiles
    FOR EACH ROW EXECUTE FUNCTION reject_billing_profile_rewrite();
