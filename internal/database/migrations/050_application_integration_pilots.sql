-- Managed browser artifacts pin the pilot that granted their use.
ALTER TABLE product_browser_handoffs ADD COLUMN application_pilot_at timestamptz;
ALTER TABLE product_browser_codes ADD COLUMN application_pilot_at timestamptz;
ALTER TABLE product_identity_links ADD COLUMN application_pilot_at timestamptz;
