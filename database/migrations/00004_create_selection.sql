-- +goose Up
-- What kind of Item this is and how an App may be installed. Empty means the
-- app works it out.
ALTER TABLE items ADD COLUMN kind TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN install_method TEXT NOT NULL DEFAULT '';

-- The version-independent description of the file chosen for an Item, applied
-- to every later release. No row means the best match is taken each time.
CREATE TABLE selection_intents (
    item_id INTEGER PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    product TEXT NOT NULL DEFAULT '',
    artifact_kind TEXT NOT NULL DEFAULT '',
    architecture TEXT NOT NULL DEFAULT '',
    abi TEXT NOT NULL DEFAULT '',
    cpu_variant TEXT NOT NULL DEFAULT '',
    variant TEXT NOT NULL DEFAULT '',
    archive_member TEXT NOT NULL DEFAULT '',
    explicit INTEGER NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE selection_intents;
ALTER TABLE items DROP COLUMN install_method;
ALTER TABLE items DROP COLUMN kind;
