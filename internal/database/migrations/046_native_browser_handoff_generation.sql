ALTER TABLE product_browser_handoffs
  ADD COLUMN native_handoff_generation text NOT NULL DEFAULT '',
  ADD CONSTRAINT browser_handoff_generation_requires_session CHECK (
    native_handoff_generation = '' OR native_session_id IS NOT NULL
  );
