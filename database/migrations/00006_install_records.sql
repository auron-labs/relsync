-- +goose Up
-- A Tool can add several programs from one archive, so the chosen archive
-- members become a list.
ALTER TABLE selection_intents RENAME COLUMN archive_member TO archive_members;
UPDATE selection_intents
SET archive_members = CASE WHEN archive_members = '' THEN '[]' ELSE json_array(archive_members) END;

-- Where the user asked an Item to be installed, empty for the default place,
-- and why the last install, update or removal failed.
ALTER TABLE items ADD COLUMN install_folder TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN install_error TEXT NOT NULL DEFAULT '';

-- What this application put on the machine. A row with no method is a version
-- the user says they have.
ALTER TABLE installs ADD COLUMN method TEXT NOT NULL DEFAULT '';
ALTER TABLE installs ADD COLUMN asset_name TEXT NOT NULL DEFAULT '';
ALTER TABLE installs ADD COLUMN folder TEXT NOT NULL DEFAULT '';
ALTER TABLE installs ADD COLUMN main_exe TEXT NOT NULL DEFAULT '';
ALTER TABLE installs ADD COLUMN dedicated INTEGER NOT NULL DEFAULT 0;
ALTER TABLE installs ADD COLUMN shortcut TEXT NOT NULL DEFAULT '';
ALTER TABLE installs ADD COLUMN sha256 TEXT NOT NULL DEFAULT '';

-- The files an install put in its folder, relative to it: removing the install
-- deletes only these. A file that is a command of a Tool names the command.
CREATE TABLE install_files (
    item_id INTEGER NOT NULL REFERENCES installs (item_id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    command TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (item_id, path)
);

-- +goose Down
DROP TABLE install_files;
ALTER TABLE installs DROP COLUMN sha256;
ALTER TABLE installs DROP COLUMN shortcut;
ALTER TABLE installs DROP COLUMN dedicated;
ALTER TABLE installs DROP COLUMN main_exe;
ALTER TABLE installs DROP COLUMN folder;
ALTER TABLE installs DROP COLUMN asset_name;
ALTER TABLE installs DROP COLUMN method;
ALTER TABLE items DROP COLUMN install_error;
ALTER TABLE items DROP COLUMN install_folder;
UPDATE selection_intents SET archive_members = COALESCE(json_extract(archive_members, '$[0]'), '');
ALTER TABLE selection_intents RENAME COLUMN archive_members TO archive_member;
