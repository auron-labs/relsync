-- +goose Up
-- When the user last changed an Item, in Unix milliseconds, so a sync can keep
-- the later of two edits. 0 for an Item that has not been edited since.
ALTER TABLE items ADD COLUMN modified_at INTEGER NOT NULL DEFAULT 0;

-- The same for a saved repository, and when a group was made or renamed.
ALTER TABLE try_later ADD COLUMN modified_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE groups ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;

-- What the user removed and when, in Unix milliseconds, so a sync can carry a
-- removal to the other devices. The key names the thing removed: an Item
-- (item:), a saved repository (later:) or a group (group:).
CREATE TABLE sync_tombstones (
    key TEXT PRIMARY KEY,
    deleted_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE sync_tombstones;
ALTER TABLE groups DROP COLUMN created_at;
ALTER TABLE try_later DROP COLUMN modified_at;
ALTER TABLE items DROP COLUMN modified_at;
