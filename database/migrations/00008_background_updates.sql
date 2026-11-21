-- +goose Up
-- The user's choice to install this Item's updates without being asked.
ALTER TABLE items ADD COLUMN auto_update INTEGER NOT NULL DEFAULT 0;

-- The release tag the user was last told about, so a version is announced once.
ALTER TABLE items ADD COLUMN notified_version TEXT NOT NULL DEFAULT '';

-- Set when the repository was matched to the program by name rather than by the
-- program itself or the user, so an update could come from the wrong project.
ALTER TABLE items ADD COLUMN link_guessed INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE items DROP COLUMN link_guessed;
ALTER TABLE items DROP COLUMN notified_version;
ALTER TABLE items DROP COLUMN auto_update;
