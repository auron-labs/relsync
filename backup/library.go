package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/auron-labs/mygo-framework/database"

	"github.com/aaronflorey/release-sync/install"
)

const (
	libraryFormat  = "release-sync-library-backup"
	libraryVersion = 1

	manifestName = "manifest.json"
	databaseName = "library/app.db"
	appsFolder   = "apps/"
)

// Include is what a Library backup holds.
type Include string

// The choices of what to include.
const (
	// LibraryOnly is the Library and the settings.
	LibraryOnly Include = "library"
	// WithApps is those and the files of the Portable apps.
	WithApps Include = "full"
)

// UnreachableError is a folder backups cannot be saved in, such as a drive or
// network location that is not there. It is worth trying again later.
type UnreachableError struct {
	Folder string
	Err    error
}

func (e *UnreachableError) Error() string { return "cannot reach " + e.Folder + ": " + e.Err.Error() }
func (e *UnreachableError) Unwrap() error { return e.Err }

// Manifest says what a Library backup is and holds.
type Manifest struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// Created is when the backup was made, and Machine and AppVersion where
	// and by which version of the application.
	Created    time.Time     `json:"created"`
	Machine    string        `json:"machine"`
	AppVersion string        `json:"appVersion"`
	Includes   Include       `json:"includes"`
	Apps       []ManifestApp `json:"apps,omitempty"`
	// Skipped names the apps left out, as too large.
	Skipped []string `json:"skipped,omitempty"`
}

// ManifestApp is a Portable app's files in a Library backup.
type ManifestApp struct {
	Name string `json:"name"`
	// Entry is the archive inside the backup that holds the folder.
	Entry string `json:"entry"`
	// Folder is where the files go when the backup is restored.
	Folder    string `json:"folder"`
	Dedicated bool   `json:"dedicated"`
	Files     int    `json:"files"`
}

// AppFiles is a Portable app to include in a Library backup.
type AppFiles struct {
	Name   string
	Record install.Record
	Tag    string
}

// LibraryRequest is a Library backup to make.
type LibraryRequest struct {
	// Database is the live database, which is backed up with the database's
	// own backup, never copied as a file.
	Database string
	// Folder is where backups are saved, and Prefix starts their names.
	Folder, Prefix string
	Include        Include
	// Apps are the Portable apps whose files go in when Include is WithApps.
	Apps []AppFiles
	// Keep is how many backups to keep, all of them when zero or less.
	Keep       int
	AppVersion string
	// MaxAppBytes is the size limit of one app's files, MaxAppBytes when zero.
	MaxAppBytes int64
	Now         time.Time
}

// LibraryResult is a Library backup that was made.
type LibraryResult struct {
	Path     string
	Size     int64
	Manifest Manifest
}

// nameFormat stamps the names of Library backups, which sort oldest first.
const nameFormat = "20060102-150405"

// LibraryName is the file name of a backup made at a time.
func LibraryName(prefix string, include Include, at time.Time) string {
	return prefix + "-" + string(include) + "-" + at.Format(nameFormat) + ".zip"
}

// CreateLibrary writes one archive holding a manifest, a consistent copy of
// the database and, when asked, the Portable apps' files, then deletes the
// oldest archives beyond the number to keep. Nothing else of the application's
// data is read, so no token or other secret can be in it: those live in the
// system's credential store.
func CreateLibrary(ctx context.Context, req LibraryRequest) (LibraryResult, error) {
	if err := os.MkdirAll(req.Folder, 0o750); err != nil {
		return LibraryResult{}, &UnreachableError{Folder: req.Folder, Err: err}
	}
	machine, _ := os.Hostname()
	manifest := Manifest{
		Format: libraryFormat, Version: libraryVersion, Created: req.Now.UTC(), Machine: machine, AppVersion: req.AppVersion, Includes: req.Include,
	}
	final := filepath.Join(req.Folder, LibraryName(req.Prefix, req.Include, req.Now))
	err := writeArchive(final, func(w io.Writer) error {
		zw := zip.NewWriter(w)
		if err := addDatabase(ctx, zw, req.Database); err != nil {
			return err
		}
		if req.Include == WithApps {
			if err := addApps(ctx, zw, req, &manifest); err != nil {
				return err
			}
		}
		if err := addJSON(zw, manifestName, manifest); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return fmt.Errorf("finish the archive: %w", err)
		}
		return nil
	})
	if err != nil {
		return LibraryResult{}, fmt.Errorf("back up the library: %w", err)
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(req.Prefix) + `-(library|full)-\d{8}-\d{6}\.zip$`)
	prune(req.Folder, req.Keep, pattern.MatchString)
	info, err := os.Stat(final)
	if err != nil {
		return LibraryResult{}, fmt.Errorf("read %s: %w", final, err)
	}
	return LibraryResult{Path: final, Size: info.Size(), Manifest: manifest}, nil
}

// addDatabase adds a consistent copy of the live database, made by the
// database's own backup into a temporary folder.
func addDatabase(ctx context.Context, zw *zip.Writer, path string) error {
	dir, err := os.MkdirTemp("", "release-sync-backup-")
	if err != nil {
		return fmt.Errorf("make room for the database copy: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	copyPath := filepath.Join(dir, "app.db")
	if err := database.Backup(ctx, path, copyPath); err != nil {
		return fmt.Errorf("copy the database: %w", err)
	}
	f, err := os.Open(copyPath) // #nosec G304 -- the file was just made in a folder of ours.
	if err != nil {
		return fmt.Errorf("open the database copy: %w", err)
	}
	defer func() { _ = f.Close() }()
	entry, err := zw.Create(databaseName)
	if err != nil {
		return fmt.Errorf("add the database: %w", err)
	}
	if _, err := io.Copy(entry, ctxReader{ctx: ctx, r: f}); err != nil {
		return fmt.Errorf("add the database: %w", err)
	}
	return nil
}

// addApps adds each Portable app's folder as an archive of its own, stored
// as it is, so that restoring one is restoring an app backup. An app over the
// size limit is skipped and named in the manifest.
func addApps(ctx context.Context, zw *zip.Writer, req LibraryRequest, m *Manifest) error {
	for i, app := range req.Apps {
		files, size, err := install.OwnedFiles(app.Record)
		if err != nil {
			continue // A folder that is gone has nothing to back up.
		}
		if size > orDefault(req.MaxAppBytes, MaxAppBytes) {
			m.Skipped = append(m.Skipped, app.Name)
			continue
		}
		meta := appMeta{Format: appFormat, Tag: app.Tag, Asset: app.Record.Asset, MainExe: app.Record.MainExe, SHA256: app.Record.SHA256, Created: req.Now.UTC()}
		for _, f := range app.Record.Files {
			meta.Files = append(meta.Files, fileMeta{Path: f.Path, SHA256: f.SHA256})
		}
		entryName := appsFolder + strconv.Itoa(i+1) + ".zip"
		entry, err := zw.CreateHeader(&zip.FileHeader{Name: entryName, Method: zip.Store})
		if err != nil {
			return fmt.Errorf("add %s: %w", app.Name, err)
		}
		written, err := zipFolder(ctx, entry, app.Record.Folder, files, meta)
		if err != nil {
			return fmt.Errorf("add %s: %w", app.Name, err)
		}
		m.Apps = append(m.Apps, ManifestApp{Name: app.Name, Entry: entryName, Folder: app.Record.Folder, Dedicated: app.Record.Dedicated, Files: written})
	}
	return nil
}

func addJSON(zw *zip.Writer, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	entry, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if _, err := entry.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// ErrNewerBackup is a backup made by a newer version of the application, which
// this version cannot read.
var ErrNewerBackup = errors.New("the backup was made by a newer version of the application")

// ReadManifest reads the manifest of a Library backup. A file that is not one
// is ErrNotABackup, and one of a newer format ErrNewerBackup.
func ReadManifest(archive string) (Manifest, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %s: %w", ErrNotABackup, filepath.Base(archive), err)
	}
	defer func() { _ = zr.Close() }()
	var m Manifest
	if err := readJSON(&zr.Reader, manifestName, &m); err != nil || m.Format != libraryFormat {
		return Manifest{}, fmt.Errorf("%w: %s", ErrNotABackup, filepath.Base(archive))
	}
	if m.Version > libraryVersion {
		return Manifest{}, fmt.Errorf("%w: %s", ErrNewerBackup, filepath.Base(archive))
	}
	return m, nil
}
