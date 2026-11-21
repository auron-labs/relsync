package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/database"

	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
)

var clock = time.Date(2026, 11, 20, 9, 30, 0, 0, time.Local)

// files maps paths, written with "/", to contents.
type files map[string]string

func (f files) write(t *testing.T, dir string) {
	t.Helper()
	for name, text := range f {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (f files) names() []string {
	names := make([]string, 0, len(f))
	for n := range f {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// read lists every file below dir.
func read(t *testing.T, dir string) files {
	t.Helper()
	got := files{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path) // #nosec G122 G304 -- a path inside the test's temporary directory.
		got[filepath.ToSlash(rel)] = string(data)
		return err //nolint:wrapcheck // The walk reports it with the path.
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return got
}

func sameFiles(a, b files) bool {
	if len(a) != len(b) {
		return false
	}
	for name, text := range a {
		if other, ok := b[name]; !ok || other != text {
			return false
		}
	}
	return true
}

// app is an installed Portable app.
type app struct {
	folder string
	record install.Record
}

func newApp(t *testing.T, dedicated bool, installed, others files) app {
	t.Helper()
	folder := filepath.Join(t.TempDir(), "apps", "tool")
	installed.write(t, folder)
	others.write(t, folder)
	rec := install.Record{Method: install.MethodPortable, Folder: folder, Dedicated: dedicated, Asset: "tool.zip", MainExe: "tool.exe", SHA256: "abc"}
	for _, name := range installed.names() {
		rec.Files = append(rec.Files, install.File{Path: name})
	}
	return app{folder: folder, record: rec}
}

func (a app) request(dir string, at time.Time) AppRequest {
	return AppRequest{Dir: dir, Record: a.record, Tag: "v1.0.0", Reason: BeforeUpdate, Now: at}
}

var v1 = files{"tool.exe": "program v1", "lib/a.dll": "library v1", "data.cfg": "config v1"}

func TestBackingUpAnAppListsItsVersionAndReason(t *testing.T) {
	a := newApp(t, true, v1, nil)
	dir := t.TempDir()
	info, err := CreateApp(t.Context(), a.request(dir, clock))
	if err != nil {
		t.Fatal(err)
	}
	list := ListApp(dir)
	if len(list) != 1 || list[0].Path != info.Path || list[0].Tag != "v1.0.0" || list[0].Reason != BeforeUpdate || list[0].Size == 0 || !list[0].Created.Equal(clock) {
		t.Fatalf("list = %+v, info = %+v", list, info)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the folder holds %d files, want only the archive", len(entries))
	}
}

func TestOnlyTheNewestThreeBackupsOfAnAppAreKept(t *testing.T) {
	a := newApp(t, true, v1, nil)
	dir := t.TempDir()
	for i := range 5 {
		if _, err := CreateApp(t.Context(), a.request(dir, clock.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	list := ListApp(dir)
	if len(list) != KeepPerApp {
		t.Fatalf("kept %d backups", len(list))
	}
	for i, want := range []int{4, 3, 2} {
		if !list[i].Created.Equal(clock.Add(time.Duration(want) * time.Hour)) {
			t.Errorf("backup %d is from %v, want hour %d", i, list[i].Created, want)
		}
	}
}

func TestAFolderOverTheLimitIsSkipped(t *testing.T) {
	a := newApp(t, true, files{"tool.exe": strings.Repeat("x", 1000)}, nil)
	dir := t.TempDir()
	req := a.request(dir, clock)
	req.MaxBytes = 999
	if _, err := CreateApp(t.Context(), req); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if len(ListApp(dir)) != 0 {
		t.Error("an archive was made of a folder over the limit")
	}
	req.MaxBytes = 1000
	if _, err := CreateApp(t.Context(), req); err != nil {
		t.Fatalf("a folder exactly at the limit: %v", err)
	}
	// The limit is 2 GB.
	if MaxAppBytes != 2*1024*1024*1024 {
		t.Errorf("MaxAppBytes = %d", MaxAppBytes)
	}
}

func TestFilesThatCannotBeReadAreLeftOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	a := newApp(t, true, v1, nil)
	if err := os.Chmod(filepath.Join(a.folder, "data.cfg"), 0); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	info, err := CreateApp(t.Context(), a.request(dir, clock))
	if err != nil {
		t.Fatal(err)
	}
	if got := zipNames(t, info.Path); !slices.Equal(got, []string{".release-sync-backup.json", "lib/a.dll", "tool.exe"}) {
		t.Errorf("archive holds %v", got)
	}
	// Nothing readable is nothing to keep.
	for name := range v1 {
		_ = os.Chmod(filepath.Join(a.folder, filepath.FromSlash(name)), 0)
	}
	if _, err := CreateApp(t.Context(), a.request(dir, clock.Add(time.Hour))); !errors.Is(err, ErrNothingToBackUp) {
		t.Fatalf("err = %v", err)
	}
	if got := ListApp(dir); len(got) != 1 {
		t.Errorf("a backup of nothing was kept: %d archives", len(got))
	}
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func TestAFolderTheApplicationMadeIsBackedUpWholeAndAChosenOneByItsInstalledFiles(t *testing.T) {
	mine := files{"saves/slot1.dat": "my save"}
	whole := newApp(t, true, v1, mine)
	info, err := CreateApp(t.Context(), whole.request(t.TempDir(), clock))
	if err != nil {
		t.Fatal(err)
	}
	if got := zipNames(t, info.Path); !slices.Contains(got, "saves/slot1.dat") {
		t.Errorf("a whole folder's backup holds %v", got)
	}
	chosen := newApp(t, false, v1, mine)
	info, err = CreateApp(t.Context(), chosen.request(t.TempDir(), clock))
	if err != nil {
		t.Fatal(err)
	}
	if got := zipNames(t, info.Path); slices.Contains(got, "saves/slot1.dat") {
		t.Errorf("the person's own files were taken: %v", got)
	}
}

func TestRestoringAnAppPutsItsFolderBackAsItWas(t *testing.T) {
	a := newApp(t, false, v1, files{"my-notes.txt": "mine"})
	dir := t.TempDir()
	info, err := CreateApp(t.Context(), a.request(dir, clock))
	if err != nil {
		t.Fatal(err)
	}
	// An update: files change, one goes, one is new.
	if err := os.Remove(filepath.Join(a.folder, "lib", "a.dll")); err != nil {
		t.Fatal(err)
	}
	files{"tool.exe": "program v2", "new.dll": "only in v2"}.write(t, a.folder)
	current := a.record
	current.Files = []install.File{{Path: "tool.exe"}, {Path: "new.dll"}, {Path: "data.cfg"}}

	rec, err := RestoreApp(t.Context(), RestoreRequest{Backup: ListApp(dir)[0], Current: current, Platform: &platform.Fake{}})
	if err != nil {
		t.Fatal(err)
	}
	want := files{"tool.exe": "program v1", "lib/a.dll": "library v1", "data.cfg": "config v1", "my-notes.txt": "mine"}
	if got := read(t, a.folder); !sameFiles(got, want) {
		t.Errorf("folder = %v, want %v", got, want)
	}
	if rec.Folder != a.folder || rec.MainExe != "tool.exe" || rec.SHA256 != "abc" || rec.Asset != "tool.zip" || len(rec.Files) != len(v1) {
		t.Errorf("record = %+v", rec)
	}
	if info.Tag != "v1.0.0" {
		t.Errorf("tag = %q", info.Tag)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(a.folder), ".*")); len(left) != 0 {
		t.Errorf("a restore left %v beside the app", left)
	}
}

// evilBackup is a backup whose archive holds an entry that would be written outside the app's folder.
func evilBackup(t *testing.T, entry string) AppInfo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evil.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, text := range map[string]string{
		"tool.exe": "program", entry: "payload",
		metaName: `{"format":"` + appFormat + `","tag":"v0","files":[{"path":"tool.exe"}]}`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(text))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := readAppMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	return AppInfo{Path: path, meta: meta}
}

func TestARestoreEntryThatWouldEscapeTheFolderIsRefused(t *testing.T) {
	for _, entry := range []string{"../outside.txt", "lib/../../outside.txt", "/etc/outside.txt", `..\outside.txt`, "C:/outside.txt"} {
		a := newApp(t, true, v1, nil)
		outside := filepath.Join(filepath.Dir(a.folder), "outside.txt")
		_, err := RestoreApp(t.Context(), RestoreRequest{Backup: evilBackup(t, entry), Current: a.record, Platform: &platform.Fake{}})
		if !errors.Is(err, install.ErrUnsafeEntry) {
			t.Errorf("%q: err = %v", entry, err)
		}
		if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
			t.Errorf("%q: a file was written outside the folder", entry)
		}
		if got := read(t, a.folder); !sameFiles(got, v1) {
			t.Errorf("%q: a refused restore changed the app: %v", entry, got)
		}
	}
}

func TestAnAppInAFolderThatIsTooBroadIsNotRestored(t *testing.T) {
	a := newApp(t, true, v1, nil)
	dir := t.TempDir()
	info, err := CreateApp(t.Context(), a.request(dir, clock))
	if err != nil {
		t.Fatal(err)
	}
	_, err = RestoreApp(t.Context(), RestoreRequest{Backup: info, Current: a.record, Protected: []string{filepath.Join(a.folder, "lib")}, Platform: &platform.Fake{}})
	if !errors.Is(err, install.ErrTooBroad) {
		t.Fatalf("err = %v", err)
	}
}

func TestListAppIgnoresWhatIsNotABackup(t *testing.T) {
	dir := t.TempDir()
	files{"notes.txt": "x", "broken.zip": "not a zip"}.write(t, dir)
	other := filepath.Join(dir, "other.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	_, _ = zw.Create("readme.txt")
	_ = zw.Close()
	if err := os.WriteFile(other, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ListApp(dir); len(got) != 0 {
		t.Errorf("listed %+v", got)
	}
	if got := ListApp(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("a missing folder lists %+v", got)
	}
}

// libraryDB is a migrated database holding a marker, as the application's would.
func libraryDB(t *testing.T, dir, marker string) string {
	t.Helper()
	path := filepath.Join(dir, "app.db")
	db, err := database.Open(t.Context(), path, migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(t.Context(), `CREATE TABLE IF NOT EXISTS marker (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(t.Context(), `DELETE FROM marker`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(t.Context(), `INSERT INTO marker (v) VALUES (?)`, marker); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// marker reads the marker of a database.
func marker(t *testing.T, path string) string {
	t.Helper()
	db, err := database.Open(t.Context(), path, migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var v string
	if err := db.SQL.QueryRowContext(t.Context(), `SELECT v FROM marker`).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return v
}

func libraryRequest(db, folder string, at time.Time) LibraryRequest {
	return LibraryRequest{Database: db, Folder: folder, Prefix: "release-sync", Include: LibraryOnly, AppVersion: "1.2.3", Now: at}
}

func TestALibraryBackupHoldsAManifestAndAWholeCopyOfTheDatabase(t *testing.T) {
	config := t.TempDir()
	db := libraryDB(t, config, "my library")
	// Anything else in the data folder, such as a token, never goes in.
	files{"token.txt": "not-for-the-archive", "backups/old.db": "not-for-the-archive"}.write(t, config)
	res, err := CreateLibrary(t.Context(), libraryRequest(db, t.TempDir(), clock))
	if err != nil {
		t.Fatal(err)
	}
	if got := zipNames(t, res.Path); !slices.Equal(got, []string{"library/app.db", "manifest.json"}) {
		t.Fatalf("archive holds %v", got)
	}
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("not-for-the-archive")) {
		t.Error("a secret is in the archive")
	}
	m, err := ReadManifest(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != libraryFormat || m.Version != 1 || m.AppVersion != "1.2.3" || m.Includes != LibraryOnly || !m.Created.Equal(clock) || m.Machine == "" {
		t.Errorf("manifest = %+v", m)
	}
	if filepath.Base(res.Path) != "release-sync-library-20261120-093000.zip" {
		t.Errorf("name = %s", filepath.Base(res.Path))
	}
	// The copy is a database that opens and holds what the live one did.
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := extractEntry(res.Path, databaseName, copyPath); err != nil {
		t.Fatal(err)
	}
	if got := marker(t, copyPath); got != "my library" {
		t.Errorf("marker = %q", got)
	}
}

func TestALibraryBackupOfALiveDatabaseDoesNotNeedItClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	live, err := database.Open(t.Context(), path, migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	if _, err := live.SQL.ExecContext(t.Context(), `CREATE TABLE marker (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := live.SQL.ExecContext(t.Context(), `INSERT INTO marker (v) VALUES ('uncheckpointed')`); err != nil {
		t.Fatal(err)
	}
	res, err := CreateLibrary(t.Context(), libraryRequest(path, t.TempDir(), clock))
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := extractEntry(res.Path, databaseName, copyPath); err != nil {
		t.Fatal(err)
	}
	if got := marker(t, copyPath); got != "uncheckpointed" {
		t.Errorf("marker = %q", got)
	}
}

func TestOnlyTheNewestLibraryBackupsAreKept(t *testing.T) {
	db := libraryDB(t, t.TempDir(), "x")
	folder := t.TempDir()
	files{"notes.txt": "mine", "other-library-20200101-000000.zip": "another program's"}.write(t, folder)
	for i := range 5 {
		if _, err := CreateLibrary(t.Context(), func() LibraryRequest {
			r := libraryRequest(db, folder, clock.Add(time.Duration(i)*24*time.Hour))
			r.Keep = 2
			return r
		}()); err != nil {
			t.Fatal(err)
		}
	}
	got := read(t, folder)
	names := make([]string, 0, len(got))
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	want := []string{"notes.txt", "other-library-20200101-000000.zip", "release-sync-library-20261123-093000.zip", "release-sync-library-20261124-093000.zip"}
	if !slices.Equal(names, want) {
		t.Fatalf("folder holds %v, want %v", names, want)
	}
}

func TestKeepingAllBackupsDeletesNone(t *testing.T) {
	db := libraryDB(t, t.TempDir(), "x")
	folder := t.TempDir()
	for i := range 4 {
		if _, err := CreateLibrary(t.Context(), libraryRequest(db, folder, clock.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	if entries, _ := os.ReadDir(folder); len(entries) != 4 {
		t.Fatalf("kept %d backups", len(entries))
	}
}

func TestAFolderThatCannotBeReachedIsReportedAsSuch(t *testing.T) {
	db := libraryDB(t, t.TempDir(), "x")
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CreateLibrary(t.Context(), libraryRequest(db, filepath.Join(blocker, "backups"), clock))
	var unreachable *UnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("err = %v", err)
	}
}

func TestAFailedBackupLeavesNoArchive(t *testing.T) {
	folder := t.TempDir()
	_, err := CreateLibrary(t.Context(), libraryRequest(filepath.Join(t.TempDir(), "missing.db"), folder, clock))
	if err == nil {
		t.Fatal("a backup of a missing database succeeded")
	}
	if entries, _ := os.ReadDir(folder); len(entries) != 0 {
		t.Errorf("a failed backup left %d files", len(entries))
	}
}

func TestAFullBackupHoldsEachAppsFilesAndNamesThoseSkipped(t *testing.T) {
	db := libraryDB(t, t.TempDir(), "x")
	small := newApp(t, true, v1, files{"saves/slot1.dat": "my save"})
	big := newApp(t, true, files{"big.exe": strings.Repeat("x", 500)}, nil)
	req := libraryRequest(db, t.TempDir(), clock)
	req.Include, req.MaxAppBytes = WithApps, 400
	req.Apps = []AppFiles{{Name: "Small", Record: small.record, Tag: "v1"}, {Name: "Big", Record: big.record, Tag: "v1"}}
	res, err := CreateLibrary(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Includes != WithApps || len(m.Apps) != 1 || m.Apps[0].Name != "Small" || m.Apps[0].Folder != small.folder || m.Apps[0].Files != 4 || !slices.Equal(m.Skipped, []string{"Big"}) {
		t.Fatalf("manifest = %+v", m)
	}
	if got := zipNames(t, res.Path); !slices.Equal(got, []string{"apps/1.zip", "library/app.db", "manifest.json"}) {
		t.Errorf("archive holds %v", got)
	}
}

func TestRestoringAFullBackupPutsTheAppsFilesBack(t *testing.T) {
	db := libraryDB(t, t.TempDir(), "x")
	a := newApp(t, true, v1, nil)
	req := libraryRequest(db, t.TempDir(), clock)
	req.Include, req.Apps = WithApps, []AppFiles{{Name: "Tool", Record: a.record, Tag: "v1"}}
	res, err := CreateLibrary(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	files{"tool.exe": "damaged"}.write(t, a.folder)
	if err := RestoreApps(t.Context(), res.Path, res.Manifest, nil, &platform.Fake{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, a.folder); !sameFiles(got, v1) {
		t.Errorf("folder = %v", got)
	}
}

func TestRestoringAFullBackupRefusesAFolderThatIsTooBroad(t *testing.T) {
	db := libraryDB(t, t.TempDir(), "x")
	a := newApp(t, true, v1, nil)
	req := libraryRequest(db, t.TempDir(), clock)
	req.Include, req.Apps = WithApps, []AppFiles{{Name: "Tool", Record: a.record, Tag: "v1"}}
	res, err := CreateLibrary(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	// A backup from elsewhere can name any folder.
	res.Manifest.Apps[0].Folder = filepath.Dir(a.folder)
	err = RestoreApps(t.Context(), res.Path, res.Manifest, []string{a.folder}, &platform.Fake{})
	if !errors.Is(err, install.ErrTooBroad) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoringAFullBackupRefusesAnEntryThatWouldEscape(t *testing.T) {
	evil := evilBackup(t, "../outside.txt")
	folder := filepath.Join(t.TempDir(), "apps", "tool")
	archive := filepath.Join(t.TempDir(), "library.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "apps/1.zip", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := os.ReadFile(evil.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(inner)
	_ = zw.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Apps: []ManifestApp{{Name: "Tool", Entry: "apps/1.zip", Folder: folder}}}
	if err := RestoreApps(t.Context(), archive, m, nil, &platform.Fake{}); !errors.Is(err, install.ErrUnsafeEntry) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(folder), "outside.txt")); !os.IsNotExist(err) {
		t.Error("a file was written outside the folder")
	}
}

func TestRestoringReplacesTheLibraryAtTheNextStartAndKeepsASafetyCopy(t *testing.T) {
	config := t.TempDir()
	live := libraryDB(t, config, "the backed up library")
	res, err := CreateLibrary(t.Context(), libraryRequest(live, t.TempDir(), clock))
	if err != nil {
		t.Fatal(err)
	}
	// The library changes after the backup.
	libraryDB(t, config, "the library as it is now")
	pending := filepath.Join(config, "restore-pending")
	m, err := StageRestore(t.Context(), res.Path, pending)
	if err != nil {
		t.Fatal(err)
	}
	if m.AppVersion != "1.2.3" {
		t.Errorf("manifest = %+v", m)
	}
	if got := marker(t, live); got != "the library as it is now" {
		t.Fatalf("staging changed the live library: %q", got)
	}

	applied, err := ApplyPendingRestore(t.Context(), pending, live)
	if err != nil || !applied {
		t.Fatalf("applied = %v, %v", applied, err)
	}
	if got := marker(t, live); got != "the backed up library" {
		t.Errorf("marker = %q", got)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("the staged restore was left behind: %v", err)
	}
	safety, _ := filepath.Glob(filepath.Join(config, "backups", "app.db.*.pre-restore.db"))
	if len(safety) != 1 {
		t.Fatalf("safety copies = %v", safety)
	}
	if got := marker(t, safety[0]); got != "the library as it is now" {
		t.Errorf("the safety copy holds %q", got)
	}
	// Nothing pending the next time.
	if applied, err := ApplyPendingRestore(t.Context(), pending, live); applied || err != nil {
		t.Errorf("a second start: applied = %v, %v", applied, err)
	}
}

func TestAFileThatIsNotABackupIsRefusedBeforeAnythingIsStaged(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.zip")
	if err := os.WriteFile(junk, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("manifest.json")
	_, _ = w.Write([]byte(`{"format":"something-else","version":1}`))
	_ = zw.Close()
	if err := os.WriteFile(other, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, archive := range []string{junk, other, filepath.Join(dir, "missing.zip")} {
		pending := filepath.Join(dir, "pending")
		if _, err := StageRestore(t.Context(), archive, pending); !errors.Is(err, ErrNotABackup) {
			t.Errorf("%s: err = %v", filepath.Base(archive), err)
		}
		if _, err := os.Stat(pending); !os.IsNotExist(err) {
			t.Errorf("%s: something was staged", filepath.Base(archive))
		}
	}
}

// libraryWith is a Library backup whose database is the given bytes and whose manifest has the given version.
func libraryWith(t *testing.T, version int, db []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "library.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(manifestName)
	_, _ = fmt.Fprintf(w, `{"format":%q,"version":%d}`, libraryFormat, version)
	d, _ := zw.Create(databaseName)
	_, _ = d.Write(db)
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestABackupWhoseDatabaseIsDamagedIsRefused(t *testing.T) {
	archive := libraryWith(t, 1, []byte("this is not a database"))
	pending := filepath.Join(t.TempDir(), "pending")
	if _, err := StageRestore(t.Context(), archive, pending); !errors.Is(err, ErrNotABackup) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Error("a damaged database was staged")
	}
}

func TestABackupOfANewerFormatOrSchemaIsRefused(t *testing.T) {
	if _, err := StageRestore(t.Context(), libraryWith(t, 9, nil), filepath.Join(t.TempDir(), "pending")); !errors.Is(err, ErrNewerBackup) {
		t.Errorf("a newer format: err = %v", err)
	}
	// A database a newer version of the application migrated.
	src := libraryDB(t, t.TempDir(), "x")
	db, err := database.Open(t.Context(), src, migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL.ExecContext(t.Context(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	data, err := os.ReadFile(src) // #nosec G122 G304 -- a path inside the test's temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StageRestore(t.Context(), libraryWith(t, 1, data), filepath.Join(t.TempDir(), "pending")); !errors.Is(err, ErrNewerBackup) {
		t.Errorf("a newer schema: err = %v", err)
	}
}

func TestAStagedDatabaseThatFailsLeavesTheLibraryAsItIs(t *testing.T) {
	config := t.TempDir()
	live := libraryDB(t, config, "current")
	pending := filepath.Join(config, "restore-pending")
	files{"app.db": "junk"}.write(t, pending)
	applied, err := ApplyPendingRestore(context.Background(), pending, live)
	if err == nil || applied {
		t.Fatalf("applied = %v, %v", applied, err)
	}
	if got := marker(t, live); got != "current" {
		t.Errorf("marker = %q", got)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("the failed restore was left to be tried again at every start: %v", err)
	}
}
