-- +goose Up
-- A downloaded Discover list, as JSON, by the tab and category it was built
-- for, so a visit can show it at once and refresh it later.
CREATE TABLE discover_lists (
    list_key TEXT PRIMARY KEY,
    fetched_at INTEGER NOT NULL,
    repositories TEXT NOT NULL
);

-- Whether a repository has a download for a kind of machine, as of one
-- moment. Only an answer the site gave is kept.
CREATE TABLE download_checks (
    source TEXT NOT NULL,
    owner TEXT NOT NULL COLLATE NOCASE,
    repo TEXT NOT NULL COLLATE NOCASE,
    machine TEXT NOT NULL,
    has_download INTEGER NOT NULL,
    checked_at INTEGER NOT NULL,
    PRIMARY KEY (source, owner, repo, machine)
);

-- Where the top of a list stood when it was shown: a JSON object from the
-- lower-cased owner/repo to its rank and stars.
CREATE TABLE rank_snapshots (
    list_key TEXT NOT NULL,
    taken_at INTEGER NOT NULL,
    ranks TEXT NOT NULL,
    PRIMARY KEY (list_key, taken_at)
);

-- Repositories saved to look at later, with what was known about them when
-- they were saved.
CREATE TABLE try_later (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL,
    owner TEXT NOT NULL,
    repo TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    language TEXT NOT NULL DEFAULT '',
    stars INTEGER NOT NULL DEFAULT 0,
    avatar_url TEXT NOT NULL DEFAULT '',
    topics TEXT NOT NULL DEFAULT '[]',
    note TEXT NOT NULL DEFAULT '',
    saved_at INTEGER NOT NULL,
    UNIQUE (source, owner COLLATE NOCASE, repo COLLATE NOCASE)
);

-- +goose Down
DROP TABLE try_later;
DROP TABLE rank_snapshots;
DROP TABLE download_checks;
DROP TABLE discover_lists;
