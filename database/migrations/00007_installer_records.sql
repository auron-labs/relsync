-- +goose Up
-- What an Item's installer left on the machine. The uninstall key is the entry
-- in the system's list of installed programs, empty when the system does not
-- list the program; the package full name is an MSIX package's. The scope says
-- whom it was installed for, and the entry version is what the entry said,
-- kept to see a change made outside this application.
ALTER TABLE installs ADD COLUMN uninstall_key TEXT NOT NULL DEFAULT '';
ALTER TABLE installs ADD COLUMN package_name TEXT NOT NULL DEFAULT '';
ALTER TABLE installs ADD COLUMN scope TEXT NOT NULL DEFAULT '';
ALTER TABLE installs ADD COLUMN entry_version TEXT NOT NULL DEFAULT '';

-- The user's own arguments for an Item's installer, which replace the silent
-- switches, and whether the Item is installed for everyone on the PC.
ALTER TABLE items ADD COLUMN installer_args TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN install_for_everyone INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE items DROP COLUMN install_for_everyone;
ALTER TABLE items DROP COLUMN installer_args;
ALTER TABLE installs DROP COLUMN entry_version;
ALTER TABLE installs DROP COLUMN scope;
ALTER TABLE installs DROP COLUMN package_name;
ALTER TABLE installs DROP COLUMN uninstall_key;
