// Package backup keeps copies of what the application is about to change or
// could lose: a Portable app's folder before it is updated or removed, and
// the Library with its settings, optionally with the Portable apps' files.
// Archives are zip files. Putting files back goes through the install
// package's unpacking, so an archive cannot write outside the folder it is
// restored to.
package backup

import (
	"archive/zip"
	"compress/flate"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
)

const (
	// MaxAppBytes is the size of the biggest folder that is backed up; larger
	// ones, such as games, are skipped.
	MaxAppBytes = 2 << 30
	// KeepPerApp is how many backups of one app are kept.
	KeepPerApp = 3

	// metaName is the entry that says what an app backup holds. A zipped
	// folder could hold a file of the same name only by chance, and it is
	// never put back.
	metaName = ".release-sync-backup.json"
	// stampLayout names backups so that they sort oldest first.
	stampLayout = "20060102-150405.000"
)

var (
	// ErrTooLarge is a folder over the size limit.
	ErrTooLarge = errors.New("the folder is too large to back up")
	// ErrNothingToBackUp is a folder with no file that could be read.
	ErrNothingToBackUp = errors.New("no file could be read")
	// ErrNotABackup is a file that is not an archive of this application's.
	ErrNotABackup = errors.New("not a backup made by this application")
)

// Reason is why an app was backed up.
type Reason string

// The reasons.
const (
	// BeforeUpdate is a backup taken before a newer version replaced the app.
	BeforeUpdate Reason = "update"
	// BeforeRemoval is a backup taken before the app was uninstalled.
	BeforeRemoval Reason = "removal"
)

// fileMeta is a file of the install a backup was taken of.
type fileMeta struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

// appMeta is what a backup records about the install it holds.
type appMeta struct {
	Format  string     `json:"format"`
	Tag     string     `json:"tag"`
	Asset   string     `json:"asset"`
	MainExe string     `json:"mainExe"`
	SHA256  string     `json:"sha256"`
	Reason  Reason     `json:"reason"`
	Created time.Time  `json:"created"`
	Files   []fileMeta `json:"files"`
}

const appFormat = "release-sync-app-backup"

// AppInfo is a backup of an app's folder.
type AppInfo struct {
	// Path is the archive.
	Path string
	// Tag is the version that was backed up.
	Tag     string
	Reason  Reason
	Created time.Time
	Size    int64

	meta appMeta
}

// AppRequest is an app to back up.
type AppRequest struct {
	// Dir is the Item's folder of backups.
	Dir string
	// Record is the install being backed up; its folder is what is zipped.
	Record install.Record
	// Tag is the version installed now.
	Tag    string
	Reason Reason
	// Keep is how many backups to keep, KeepPerApp when zero.
	Keep int
	// MaxBytes is the size limit, MaxAppBytes when zero.
	MaxBytes int64
	Now      time.Time
}

// AppDir is the folder under root that holds the backups of one Item.
func AppDir(root, owner, repo string) string {
	return filepath.Join(root, install.SafeName(owner+"-"+repo))
}

// CreateApp zips the app's folder into the Item's folder of backups and keeps
// the newest few. Files that cannot be read, as those a running program holds,
// are left out rather than failing the backup. A folder over the limit is
// ErrTooLarge and one with nothing readable is ErrNothingToBackUp.
func CreateApp(ctx context.Context, req AppRequest) (AppInfo, error) {
	files, size, err := install.OwnedFiles(req.Record)
	if err != nil {
		return AppInfo{}, fmt.Errorf("list the files of %s: %w", req.Record.Folder, err)
	}
	if size > orDefault(req.MaxBytes, MaxAppBytes) {
		return AppInfo{}, fmt.Errorf("%w: %s", ErrTooLarge, req.Record.Folder)
	}
	if err := os.MkdirAll(req.Dir, 0o750); err != nil {
		return AppInfo{}, fmt.Errorf("create %s: %w", req.Dir, err)
	}
	final := filepath.Join(req.Dir, req.Now.Format(stampLayout)+"-"+string(req.Reason)+".zip")
	meta := appMeta{
		Format: appFormat, Tag: req.Tag, Asset: req.Record.Asset, MainExe: req.Record.MainExe, SHA256: req.Record.SHA256,
		Reason: req.Reason, Created: req.Now.UTC(),
	}
	for _, f := range req.Record.Files {
		meta.Files = append(meta.Files, fileMeta{Path: f.Path, SHA256: f.SHA256})
	}
	if err := writeArchive(final, func(w io.Writer) error {
		written, err := zipFolder(ctx, w, req.Record.Folder, files, meta)
		if err == nil && written == 0 {
			return ErrNothingToBackUp
		}
		return err
	}); err != nil {
		return AppInfo{}, fmt.Errorf("back up %s: %w", req.Record.Folder, err)
	}
	prune(req.Dir, orDefault(req.Keep, KeepPerApp), func(name string) bool { return strings.HasSuffix(name, ".zip") })
	info, err := os.Stat(final)
	if err != nil {
		return AppInfo{}, fmt.Errorf("read %s: %w", final, err)
	}
	return AppInfo{Path: final, Tag: meta.Tag, Reason: meta.Reason, Created: meta.Created, Size: info.Size(), meta: meta}, nil
}

func orDefault[T int | int64](v, fallback T) T {
	if v == 0 {
		return fallback
	}
	return v
}

// writeArchive writes an archive through fill to a ".part" file beside path
// and moves it into place when it is whole, so a failure leaves nothing that
// looks like a backup.
func writeArchive(path string, fill func(io.Writer) error) (err error) {
	part := path + ".part"
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- the path is in the folder the backups are kept in.
	if err != nil {
		return fmt.Errorf("create %s: %w", part, err)
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(part)
		}
	}()
	if err := fill(out); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("write %s: %w", part, err)
	}
	if err := os.Rename(part, path); err != nil {
		return fmt.Errorf("move %s into place: %w", part, err)
	}
	return nil
}

// zipFolder writes the listed files of folder, then the metadata, as a zip.
// It returns how many files were written.
func zipFolder(ctx context.Context, w io.Writer, folder string, files []string, meta appMeta) (int, error) {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) { return flate.NewWriter(out, flate.BestSpeed) })
	written := 0
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("back up: %w", err)
		}
		ok, err := addFile(ctx, zw, filepath.Join(folder, filepath.FromSlash(rel)), rel)
		if err != nil {
			return 0, err
		}
		if ok {
			written++
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return 0, fmt.Errorf("write the backup's record: %w", err)
	}
	entry, err := zw.Create(metaName)
	if err != nil {
		return 0, fmt.Errorf("write the backup's record: %w", err)
	}
	if _, err := entry.Write(data); err != nil {
		return 0, fmt.Errorf("write the backup's record: %w", err)
	}
	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("finish the archive: %w", err)
	}
	return written, nil
}

// addFile adds one file to the archive as rel. A file that cannot be opened is
// in use or unreadable and is left out, which is false. Failing part way
// through one that opened is an error: the archive holds half of it.
func addFile(ctx context.Context, zw *zip.Writer, path, rel string) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- the path is a recorded file of the app's own folder.
	if err != nil {
		// Leaving the file out is the documented outcome.
		return false, nil
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		// Not a file this can keep.
		return false, nil
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return false, fmt.Errorf("describe %s: %w", rel, err)
	}
	header.Name, header.Method = rel, zip.Deflate
	entry, err := zw.CreateHeader(header)
	if err != nil {
		return false, fmt.Errorf("add %s: %w", rel, err)
	}
	if _, err := io.Copy(entry, ctxReader{ctx: ctx, r: f}); err != nil { // #nosec G110 -- the folder is the app's own, and the size was checked.
		return false, fmt.Errorf("add %s: %w", rel, err)
	}
	return true, nil
}

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context //nolint:containedctx // A reader has no other way to learn that the work was cancelled.
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, fmt.Errorf("back up: %w", err)
	}
	return c.r.Read(p) //nolint:wrapcheck // Pass the underlying reader's errors through, io.EOF included.
}

// prune deletes all but the newest keep files of dir that count as backups,
// by name, which sorts oldest first. keep of zero or less keeps everything.
func prune(dir string, keep int, counts func(name string) bool) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && counts(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for len(names) > keep {
		// An old backup that cannot be deleted is only wasted space.
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// ListApp lists the backups of one Item, newest first. A file that is not a
// readable backup is not listed.
func ListApp(dir string) []AppInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var list []AppInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		meta, err := readAppMeta(path)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, AppInfo{Path: path, Tag: meta.Tag, Reason: meta.Reason, Created: meta.Created, Size: info.Size(), meta: meta})
	}
	slices.SortFunc(list, func(a, b AppInfo) int { return strings.Compare(b.Path, a.Path) })
	return list
}

func readAppMeta(path string) (appMeta, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return appMeta{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = zr.Close() }()
	var meta appMeta
	if err := readJSON(&zr.Reader, metaName, &meta); err != nil || meta.Format != appFormat {
		return appMeta{}, fmt.Errorf("%w: %s", ErrNotABackup, path)
	}
	return meta, nil
}

// maxRecord bounds how much of a backup's record is read.
const maxRecord = 8 << 20

// readJSON decodes the entry called name of the archive.
func readJSON(zr *zip.Reader, name string, into any) error {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", name, err)
		}
		defer func() { _ = r.Close() }()
		if err := json.NewDecoder(io.LimitReader(r, maxRecord)).Decode(into); err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("%w: no %s", ErrNotABackup, name)
}

// RestoreRequest is a backup to put back over an installed app.
type RestoreRequest struct {
	Backup AppInfo
	// Current is the install being replaced.
	Current install.Record
	// Protected are the folders too broad to put an app in.
	Protected []string
	Platform  platform.Platform
}

// RestoreApp puts a backup back as the installed app: the backup's files
// replace the current ones, and files only the current version has are
// removed, so the folder is as it was. It unpacks into a staging folder beside
// the app's with the install package's rules, so an entry that would leave the
// folder refuses the whole restore, and it puts the current files back if
// anything fails. It returns the install record to keep.
func RestoreApp(ctx context.Context, req RestoreRequest) (install.Record, error) {
	if err := install.ValidateFolder(req.Current.Folder, req.Protected); err != nil {
		return install.Record{}, err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	rec, err := restoreFolder(ctx, req.Backup.Path, req.Backup.meta, req.Current.Folder, req.Current.Dedicated, &req.Current, req.Platform)
	if err != nil {
		return install.Record{}, err
	}
	return rec, nil
}

// restoreFolder unpacks the archive of a folder over folder. previous is the
// install being replaced, nil when there is none to account for.
func restoreFolder(ctx context.Context, archive string, meta appMeta, folder string, dedicated bool, previous *install.Record, p platform.Platform) (install.Record, error) {
	st, err := install.StageFolder(ctx, archive, filepath.Dir(folder))
	if err != nil {
		return install.Record{}, err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	defer func() { _ = st.Discard() }()
	if err := os.Remove(filepath.Join(st.Root, metaName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return install.Record{}, fmt.Errorf("prepare the restore: %w", err)
	}
	out, err := install.ApplyPortable(ctx, st, install.PortableRequest{
		Asset: meta.Asset, Folder: folder, Dedicated: dedicated, Previous: previous, Platform: p,
	})
	if err != nil {
		return install.Record{}, err //nolint:wrapcheck // The install package's errors are matched by type.
	}
	rec := out.Record
	rec.MainExe, rec.SHA256, rec.Files = meta.MainExe, meta.SHA256, make([]install.File, len(meta.Files))
	for i, f := range meta.Files {
		rec.Files[i] = install.File{Path: f.Path, SHA256: f.SHA256}
	}
	return rec, nil
}
