-- A native expiry belongs to a tagged handoff. Older tagged handoffs without
-- an expiry cannot be redeemed, so they must start again with a fresh approval.
ALTER TABLE product_browser_handoffs
    ADD COLUMN native_session_expires_at timestamptz,
    ADD CONSTRAINT browser_handoff_native_expiry_requires_session CHECK (
        native_session_expires_at IS NULL OR native_session_id IS NOT NULL
    );
