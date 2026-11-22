-- A back-channel native logout removes the browser session before its cookie
-- can be cleared. Keep the hash long enough for one fresh handoff to replace it.
CREATE TABLE product_browser_native_logout_cookies (
    session_id_hash text PRIMARY KEY,
    expires_at timestamptz NOT NULL
);

CREATE INDEX product_browser_native_logout_cookies_expiry
    ON product_browser_native_logout_cookies(expires_at);
