package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAFolderKeepsTheFileInASubFolderAndReplacesItWholesale(t *testing.T) {
	root := t.TempDir()
	f := NewFolder(root, "release-sync")
	ctx := context.Background()
	if _, found, err := f.Read(ctx); err != nil || found {
		t.Fatalf("Read before any write = %v, %v, want nothing", found, err)
	}

	if err := f.Write(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(ctx, "second, which is longer"); err != nil {
		t.Fatal(err)
	}
	text, found, err := f.Read(ctx)
	if err != nil || !found || text != "second, which is longer" {
		t.Fatalf("Read = %q, %v, %v", text, found, err)
	}
	if want := filepath.Join(root, "release-sync", "library-sync.json"); f.File() != want {
		t.Errorf("file = %q, want %q", f.File(), want)
	}
	entries, err := os.ReadDir(filepath.Join(root, "release-sync"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the sub-folder holds %v, want only the file: a temporary file was left", names)
	}
}

func TestAFolderThatCannotBeWrittenToFailsAndLeavesNoTemporaryFile(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocked, []byte("a file where the folder should be"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewFolder(blocked, "release-sync").Write(context.Background(), "x"); err == nil {
		t.Fatal("writing below a file succeeded")
	}
	// The sync file being a directory makes the rename fail after the temporary file was written.
	f := NewFolder(root, "release-sync")
	if err := os.MkdirAll(f.File(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(context.Background(), "x"); err == nil {
		t.Fatal("replacing a directory succeeded")
	}
	entries, _ := os.ReadDir(filepath.Dir(f.File()))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("the temporary file %s was left behind", e.Name())
		}
	}
}

func TestAnotherDevicesChangeToTheFileIsNoticedWithoutPolling(t *testing.T) {
	root := t.TempDir()
	mine, theirs := NewFolder(root, "release-sync"), NewFolder(root, "release-sync")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 8)
	if err := mine.Watch(ctx, func() { changed <- struct{}{} }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	for _, text := range []string{"created", "replaced"} {
		if err := theirs.Write(context.Background(), text); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			t.Fatalf("no notice that the file was %s", text)
		}
	}
}

func TestOtherFilesInTheFolderAreNotMistakenForTheSyncFile(t *testing.T) {
	root := t.TempDir()
	f := NewFolder(root, "release-sync")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 8)
	if err := f.Watch(ctx, func() { changed <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.File()), "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Error("a change to another file was reported as a change to the sync file")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestTwoDevicesSyncThroughAFolder(t *testing.T) {
	root := t.TempDir()
	pc := newDevice(t, "pc", "Windows", NewFolder(root, "release-sync"))
	mac := newDevice(t, "mac", "macOS", NewFolder(root, "release-sync"))
	pc.save("dolphin-emu", "dolphin", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	equalStrings(t, "mac saved repositories", mac.laterKeys(), []string{"dolphin-emu/dolphin"})
}
