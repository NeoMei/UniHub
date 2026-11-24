CREATE TABLE team_groups (
    team_id text NOT NULL REFERENCES accounts(id),
    id text NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80 AND name = btrim(name)),
    name_key text NOT NULL CHECK (name_key <> ''),
    PRIMARY KEY (team_id, id),
    UNIQUE (team_id, name_key)
);

CREATE TABLE team_group_members (
    team_id text NOT NULL,
    group_id text NOT NULL,
    user_id text NOT NULL,
    PRIMARY KEY (team_id, group_id, user_id),
    FOREIGN KEY (team_id, group_id) REFERENCES team_groups(team_id, id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, user_id) REFERENCES account_members(account_id, user_id) ON DELETE CASCADE
);

CREATE INDEX team_group_members_user_idx ON team_group_members(team_id, user_id, group_id);
