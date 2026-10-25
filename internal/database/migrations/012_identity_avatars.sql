CREATE TABLE identity_avatars (
    user_id text PRIMARY KEY REFERENCES users(id),
    content_type text NOT NULL CHECK(content_type = 'image/png'),
    data bytea NOT NULL CHECK(octet_length(data) BETWEEN 1 AND 524288),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
