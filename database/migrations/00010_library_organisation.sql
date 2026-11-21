-- +goose Up
-- Groups the user makes. An Item refers to its group by id, so renaming one
-- touches a single row.
CREATE TABLE groups (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE
);

-- The Item's own group; 0 leaves it in the group its category gives it.
ALTER TABLE items ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0;

-- What kind of software the repository is, worked out when its metadata is
-- fetched; empty until then.
ALTER TABLE items ADD COLUMN category TEXT NOT NULL DEFAULT '';

ALTER TABLE items ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN note TEXT NOT NULL DEFAULT '';

-- The user's tags, as a JSON array.
ALTER TABLE items ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE items DROP COLUMN tags;
ALTER TABLE items DROP COLUMN note;
ALTER TABLE items DROP COLUMN hidden;
ALTER TABLE items DROP COLUMN favorite;
ALTER TABLE items DROP COLUMN category;
ALTER TABLE items DROP COLUMN group_id;
DROP TABLE groups;
