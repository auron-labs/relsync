package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
	"github.com/aaronflorey/release-sync/version"
)

// Installed is what an operation records when it has put an Item on this machine.
type Installed struct {
	// Tag is the release that was installed.
	Tag    string
	Record install.Record
	// Kind is what the installed program turned out to be; empty leaves the
	// Item's kind as it is. It is only given for an Item whose kind was guessed.
	Kind selection.ItemKind
	// Intent is the choice of file to keep, with the programs a Tool was given;
	// nil leaves it as it is.
	Intent *selection.Intent
}

// RecordInstall records that the application installed an Item, replacing
// the record and file list of an earlier install, and clears the Item's last
// install error.
func (c *Catalog) RecordInstall(ctx context.Context, id int64, in Installed) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Get(id)
	if !ok {
		return ErrNotFound
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record install of %s: %w", entry.FullName(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeInstall(ctx, tx, id, in.Tag, c.now(), in.Record); err != nil {
		return fmt.Errorf("record install of %s: %w", entry.FullName(), err)
	}
	if in.Kind != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE items SET kind = ? WHERE id = ?`, string(in.Kind), id); err != nil {
			return fmt.Errorf("record install of %s: %w", entry.FullName(), err)
		}
	}
	if in.Intent != nil {
		if err := writeIntent(ctx, tx, id, in.Intent); err != nil {
			return fmt.Errorf("record install of %s: %w", entry.FullName(), err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE items SET install_error = '' WHERE id = ?`, id); err != nil {
		return fmt.Errorf("record install of %s: %w", entry.FullName(), err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record install of %s: %w", entry.FullName(), err)
	}
	_, err = c.publish(ctx, id)
	return err
}

// writeInstall stores an Item's install record and its file list in place of
// an earlier one.
func writeInstall(ctx context.Context, db execer, id int64, tag string, at time.Time, rec install.Record) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO installs
		(item_id, tag, installed_at, method, asset_name, folder, main_exe, dedicated, shortcut, sha256,
		uninstall_key, package_name, scope, entry_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (item_id) DO UPDATE SET tag = excluded.tag, installed_at = excluded.installed_at,
		method = excluded.method, asset_name = excluded.asset_name, folder = excluded.folder,
		main_exe = excluded.main_exe, dedicated = excluded.dedicated, shortcut = excluded.shortcut,
		sha256 = excluded.sha256, uninstall_key = excluded.uninstall_key, package_name = excluded.package_name,
		scope = excluded.scope, entry_version = excluded.entry_version`,
		id, tag, unix(at), string(rec.Method), rec.Asset, rec.Folder, rec.MainExe, rec.Dedicated,
		rec.Shortcut, rec.SHA256, rec.UninstallKey, rec.Package, string(rec.Scope), rec.EntryVersion); err != nil {
		return fmt.Errorf("write install: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM install_files WHERE item_id = ?`, id); err != nil {
		return fmt.Errorf("write install: %w", err)
	}
	for _, f := range rec.Files {
		if _, err := db.ExecContext(ctx, `INSERT INTO install_files (item_id, path, sha256, command) VALUES (?, ?, ?, ?)`,
			id, f.Path, f.SHA256, f.Command); err != nil {
			return fmt.Errorf("write install: %w", err)
		}
	}
	return nil
}

// ClearInstall forgets that the Item is installed, and its file list, once the
// application has removed it from the machine.
func (c *Catalog) ClearInstall(ctx context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.Get(id); !ok {
		return ErrNotFound
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM installs WHERE item_id = ?`, id); err != nil {
		return fmt.Errorf("forget install of item %d: %w", id, err)
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE items SET install_error = '' WHERE id = ?`, id); err != nil {
		return fmt.Errorf("forget install of item %d: %w", id, err)
	}
	_, err := c.publish(ctx, id)
	return err
}

// InstallFiles lists the files the application put on the machine for an
// Item. They are read when needed, as an app may have thousands.
func (c *Catalog) InstallFiles(ctx context.Context, id int64) ([]install.File, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT path, sha256, command FROM install_files WHERE item_id = ? ORDER BY path`, id)
	if err != nil {
		return nil, fmt.Errorf("load files of item %d: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	var files []install.File
	for rows.Next() {
		var f install.File
		if err := rows.Scan(&f.Path, &f.SHA256, &f.Command); err != nil {
			return nil, fmt.Errorf("load files of item %d: %w", id, err)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load files of item %d: %w", id, err)
	}
	return files, nil
}

// SetInstallError records why the last install, update or removal failed, or
// clears it with "".
func (c *Catalog) SetInstallError(ctx context.Context, id int64, text string) error {
	return c.updateItem(ctx, id, `UPDATE items SET install_error = ? WHERE id = ?`, text)
}

// SetInstallFolder sets where a Portable app is to be installed; "" is the
// default place. It applies to the next fresh install.
func (c *Catalog) SetInstallFolder(ctx context.Context, id int64, folder string) error {
	return c.updateItem(ctx, id, `UPDATE items SET install_folder = ? WHERE id = ?`, folder)
}

// SetInstallerArgs sets the user's own arguments for the Item's setup program;
// "" goes back to the silent switches.
func (c *Catalog) SetInstallerArgs(ctx context.Context, id int64, args string) error {
	return c.updateSynced(ctx, id, `UPDATE items SET installer_args = ? WHERE id = ?`, strings.TrimSpace(args))
}

// SetInstallForEveryone makes the Item install for every user of the PC, or not.
func (c *Catalog) SetInstallForEveryone(ctx context.Context, id int64, everyone bool) error {
	return c.updateItem(ctx, id, `UPDATE items SET install_for_everyone = ? WHERE id = ?`, everyone)
}

// LocateProgram records the program of an installed Item that the system does
// not list: the folder it is in and the program, relative to it.
func (c *Catalog) LocateProgram(ctx context.Context, id int64, folder, mainExe string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Get(id)
	if !ok {
		return ErrNotFound
	}
	if _, managed := entry.Installed(); !managed {
		return fmt.Errorf("locate the program of %s: %w", entry.FullName(), ErrNotInstalled)
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE installs SET folder = ?, main_exe = ? WHERE item_id = ?`, folder, mainExe, id); err != nil {
		return fmt.Errorf("locate the program of %s: %w", entry.FullName(), err)
	}
	_, err := c.publish(ctx, id)
	return err
}

// MoveInstall records that a Portable app's folder is now folder, after the
// application moved its files there. dedicated says whether the application
// made that folder for the app. The installed version, its files and when it
// was installed stay as they were.
func (c *Catalog) MoveInstall(ctx context.Context, id int64, folder string, dedicated bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Get(id)
	if !ok {
		return ErrNotFound
	}
	in, managed := entry.Installed()
	if !managed || in.Method != install.MethodPortable {
		return fmt.Errorf("move %s: %w", entry.FullName(), ErrNotInstalled)
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE installs SET folder = ?, dedicated = ? WHERE item_id = ?`, folder, dedicated, id); err != nil {
		return fmt.Errorf("move %s: %w", entry.FullName(), err)
	}
	_, err := c.publish(ctx, id)
	return err
}

// MoveInstalled moves an Item's installed version to tag, and keeps the version
// its entry reported, after the program was updated outside this application.
func (c *Catalog) MoveInstalled(ctx context.Context, id int64, tag, entryVersion string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Get(id)
	if !ok {
		return ErrNotFound
	}
	if _, managed := entry.Installed(); !managed {
		return fmt.Errorf("move the installed version of %s: %w", entry.FullName(), ErrNotInstalled)
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE installs SET tag = ?, entry_version = ? WHERE item_id = ?`, tag, entryVersion, id); err != nil {
		return fmt.Errorf("move the installed version of %s: %w", entry.FullName(), err)
	}
	_, err := c.publish(ctx, id)
	return err
}

// Claims are what the other Items have taken on this machine.
type Claims struct {
	// Folders maps the lower-case folder of every managed Portable install to
	// the name of the Item that has it.
	Folders map[string]string
	// Commands maps the lower-case command names of every Tool to the name of
	// the Item that has them.
	Commands map[string]string
}

// ClaimsExcept lists the folders and commands every Item but id has.
func ClaimsExcept(entries []Entry, id int64) Claims {
	claims := Claims{Folders: map[string]string{}, Commands: map[string]string{}}
	for _, e := range entries {
		in, ok := e.Installed()
		if !ok || e.Item.ID == id {
			continue
		}
		if in.Method == install.MethodPortable {
			claims.Folders[strings.ToLower(in.Folder)] = e.FullName()
		}
		for _, name := range in.Commands {
			claims.Commands[strings.ToLower(name)] = e.FullName()
		}
	}
	return claims
}

// Relation is how a release compares with what is installed.
type Relation int

// The relations.
const (
	// RelationFresh is a release of an Item that is not installed.
	RelationFresh Relation = iota
	// RelationSame is the installed version itself.
	RelationSame
	// RelationNewer is a release newer than the installed one.
	RelationNewer
	// RelationOlder is a release older than the installed one.
	RelationOlder
)

// Relation compares the release with tag to the installed version.
func (e Entry) Relation(tag string) Relation {
	installed := e.InstalledVersion()
	switch {
	case installed == "":
		return RelationFresh
	case installed == tag:
		return RelationSame
	case e.newer(tag, installed):
		return RelationNewer
	}
	return RelationOlder
}

// ReleaseForVersion finds the tracked release that is newer than the installed
// version and has the version seen on the machine, or reports false.
func (e Entry) ReleaseForVersion(seen string) (source.Release, bool) {
	candidates := e.Candidates()
	tags := make([]string, len(candidates))
	for i, r := range candidates {
		tags[i] = r.Tag
	}
	tag := version.MatchTag(seen, tags)
	if tag == "" || !e.newer(tag, e.InstalledVersion()) {
		return source.Release{}, false
	}
	return e.Release(tag)
}

// Release finds a cached release by its tag.
func (e Entry) Release(tag string) (source.Release, bool) {
	for _, r := range e.Releases {
		if r.Tag == tag {
			return r, true
		}
	}
	return source.Release{}, false
}
