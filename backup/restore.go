package backup

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/auron-labs/mygo-framework/database"

	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
)

// stagedDatabase is the name of the database a restore waits to put in place.
const stagedDatabase = "app.db"

// StageRestore checks a Library backup and puts its database in pendingDir,
// where ApplyPendingRestore finds it at the next start. The running
// application holds its own database open, so it cannot be replaced while it
// runs. The staged database is opened with this version's migrations, which
// brings an older one up to date and refuses one that a newer version made, and
// is then written out whole, so what is staged is complete.
func StageRestore(ctx context.Context, archive, pendingDir string) (_ Manifest, err error) {
	manifest, err := ReadManifest(archive)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.RemoveAll(pendingDir); err != nil {
		return Manifest{}, fmt.Errorf("clear %s: %w", pendingDir, err)
	}
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create %s: %w", pendingDir, err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(pendingDir)
		}
	}()
	raw := filepath.Join(pendingDir, "backup.db")
	if err := extractEntry(archive, databaseName, raw); err != nil {
		return Manifest{}, err
	}
	db, err := database.Open(ctx, raw, migrations.FS())
	if err != nil {
		if errors.Is(err, database.ErrSchemaNewer) {
			return Manifest{}, fmt.Errorf("%w: %w", ErrNewerBackup, err)
		}
		return Manifest{}, fmt.Errorf("%w: the database in it cannot be opened: %w", ErrNotABackup, err)
	}
	if err := db.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close the staged database: %w", err)
	}
	if err := database.Backup(ctx, raw, filepath.Join(pendingDir, stagedDatabase)); err != nil {
		return Manifest{}, fmt.Errorf("write the staged database: %w", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(raw + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Manifest{}, fmt.Errorf("clean up the staged database: %w", err)
		}
	}
	return manifest, nil
}

// ApplyPendingRestore replaces the database at dbPath with the one a restore
// staged in pendingDir, if there is one, and clears pendingDir. The database
// package keeps a safety copy of the database it replaces, beside it. It must
// run before the database is opened. A staged database that turns out to be
// unusable leaves the current one as it is.
func ApplyPendingRestore(ctx context.Context, pendingDir, dbPath string) (applied bool, err error) {
	staged := filepath.Join(pendingDir, stagedDatabase)
	if _, err := os.Stat(staged); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("look for a staged restore: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(pendingDir)) }()
	if err := database.Restore(ctx, staged, dbPath); err != nil {
		return false, fmt.Errorf("restore the library: %w", err)
	}
	return true, nil
}

// RestoreApps puts the Portable apps' files of a Library backup back in their
// folders, with the rules of restoring an app backup: an entry that would
// leave a folder refuses that app. A folder that is too broad to manage, which
// a backup could name, is refused too. Every app is tried, and the failures
// are returned together.
func RestoreApps(ctx context.Context, archive string, m Manifest, protected []string, p platform.Platform) error {
	var errs []error
	for _, app := range m.Apps {
		if err := restoreAppEntry(ctx, archive, app, protected, p); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", app.Name, err))
		}
	}
	return errors.Join(errs...)
}

func restoreAppEntry(ctx context.Context, archive string, app ManifestApp, protected []string, p platform.Platform) error {
	if err := install.ValidateFolder(app.Folder, protected); err != nil {
		return err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	parent := filepath.Dir(app.Folder)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	tmp, err := os.CreateTemp(parent, ".restore-*.zip")
	if err != nil {
		return fmt.Errorf("make room for the files: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("make room for the files: %w", err)
	}
	if err := extractEntry(archive, app.Entry, tmp.Name()); err != nil {
		return err
	}
	meta, err := readAppMeta(tmp.Name())
	if err != nil {
		return err
	}
	_, err = restoreFolder(ctx, tmp.Name(), meta, app.Folder, app.Dedicated, nil, p)
	return err
}

// extractEntry copies the one entry called name of a zip to dest, which is
// named by the caller and never by the archive.
func extractEntry(archive, name, dest string) (err error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrNotABackup, filepath.Base(archive), err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		in, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", name, err)
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- dest is chosen by the caller.
		if err != nil {
			return fmt.Errorf("create %s: %w", dest, err)
		}
		defer func() { err = errors.Join(err, out.Close()) }()
		if _, err := io.Copy(out, in); err != nil { // #nosec G110 -- the backup is the person's own file.
			return fmt.Errorf("unpack %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("%w: no %s", ErrNotABackup, strings.TrimPrefix(name, appsFolder))
}
