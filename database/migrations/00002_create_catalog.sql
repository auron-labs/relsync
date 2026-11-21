-- +goose Up
CREATE TABLE items (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL,
    owner TEXT NOT NULL,
    repo TEXT NOT NULL,
    lane TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    language TEXT NOT NULL DEFAULT '',
    topics TEXT NOT NULL DEFAULT '[]',
    stars INTEGER NOT NULL DEFAULT 0,
    metadata_fetched INTEGER NOT NULL DEFAULT 0,
    include_prereleases INTEGER NOT NULL DEFAULT 0,
    hold_updates INTEGER NOT NULL DEFAULT 0,
    skipped_version TEXT NOT NULL DEFAULT '',
    repo_missing INTEGER NOT NULL DEFAULT 0,
    etag TEXT NOT NULL DEFAULT '',
    last_checked INTEGER NOT NULL DEFAULT 0,
    last_error_kind TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    added_at INTEGER NOT NULL,
    UNIQUE (source, owner COLLATE NOCASE, repo COLLATE NOCASE)
);

CREATE TABLE releases (
    item_id INTEGER NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    tag TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    published_at INTEGER NOT NULL DEFAULT 0,
    prerelease INTEGER NOT NULL DEFAULT 0,
    url TEXT NOT NULL DEFAULT '',
    assets TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (item_id, position)
);

-- What is on this machine, kept apart from the Item so a Library can move
-- between machines without it. Later phases add the install method and files.
CREATE TABLE installs (
    item_id INTEGER PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    tag TEXT NOT NULL,
    installed_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE installs;
DROP TABLE releases;
DROP TABLE items;
