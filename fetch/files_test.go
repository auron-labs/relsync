package fetch_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/fetch"
)

// downloadedFolder makes a release folder as the downloader leaves it, by
// really downloading one file into it, and adds the other files.
func downloadedFolder(t *testing.T, root, owner, tag string, files ...string) string {
	t.Helper()
	srv := newFileServer(t, []byte("x"))
	dest := fetch.DestPath(root, owner, "repo", tag, "first.bin")
	if _, err := newClient().Download(t.Context(), fetch.Request{URL: srv.URL, Dest: dest, Size: 1}, nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(dest)
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func age(t *testing.T, path string, by time.Duration) {
	t.Helper()
	old := time.Now().Add(-by)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestCleanupRemovesStaleUnfinishedDownloadsAndKeepsRecentOnes(t *testing.T) {
	root := t.TempDir()
	dir := downloadedFolder(t, root, "acme", "v1", "old.bin.part", "old.bin.part.validator", "new.bin.part", "new.bin.part.validator", "orphan.bin.part.validator")
	age(t, filepath.Join(dir, "old.bin.part"), fetch.StalePartAge+time.Hour)
	// The validator is written once, at the start, so only its .part file's age counts.
	age(t, filepath.Join(dir, "new.bin.part.validator"), fetch.StalePartAge+time.Hour)
	if err := fetch.Cleanup(root, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"old.bin.part": false, "old.bin.part.validator": false,
		"new.bin.part": true, "new.bin.part.validator": true,
		"orphan.bin.part.validator": false, "first.bin": true,
	} {
		if got := exists(filepath.Join(dir, name)); got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
}

func TestCleanupRemovesFinishedDownloadsUnlessTheyAreKept(t *testing.T) {
	root := t.TempDir()
	dir := downloadedFolder(t, root, "acme", "v1")
	if err := fetch.Cleanup(root, true, time.Now()); err != nil || !exists(filepath.Join(dir, "first.bin")) {
		t.Fatalf("with keep on: err %v, file exists %v", err, exists(filepath.Join(dir, "first.bin")))
	}
	if err := fetch.Cleanup(root, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if exists(dir) || exists(filepath.Dir(dir)) {
		t.Errorf("the emptied folders were left behind")
	}
}

func TestCleanupLeavesWhatTheApplicationDidNotDownload(t *testing.T) {
	root := t.TempDir()
	// Looks like the layout, but the application never wrote there.
	theirs := filepath.Join(root, "Documents", "2026", "report.pdf")
	if err := os.MkdirAll(filepath.Dir(theirs), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(theirs, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose.zip"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	downloadedFolder(t, root, "acme", "v1")
	if err := fetch.Cleanup(root, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !exists(theirs) || !exists(filepath.Join(root, "loose.zip")) {
		t.Fatal("cleanup removed a file the application did not download")
	}
}

func TestCleanupOfAMissingFolderIsFine(t *testing.T) {
	if err := fetch.Cleanup(filepath.Join(t.TempDir(), "none"), false, time.Now()); err != nil {
		t.Fatal(err)
	}
}
