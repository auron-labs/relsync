package install

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/platform"
)

// release stages a release made of these files and returns it with the
// folder the app is to live in, under a fresh apps folder.
func stageFiles(t *testing.T, apps string, f files) *Staged {
	t.Helper()
	st, err := Stage(t.Context(), download(t, "app.zip", zipOf(t, f)), "app.zip", "archive", apps)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func portableRequest(folder string, fake *platform.Fake, previous *Record) PortableRequest {
	return PortableRequest{
		Repo: "app", Asset: "app.zip", Folder: folder, Dedicated: true, Previous: previous,
		ShortcutPath: filepath.Join(filepath.Dir(folder), "menu", "App.lnk"), ShortcutText: "App", Platform: fake,
	}
}

func paths(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	slices.Sort(out)
	return out
}

func TestAFreshPortableInstallMovesTheReleaseIntoItsFolder(t *testing.T) {
	apps := t.TempDir()
	folder := filepath.Join(apps, "app")
	fake := &platform.Fake{}
	st := stageFiles(t, apps, files{"app-1.0/app.exe": guiExe(""), "app-1.0/data/a.dat": "a", "app-1.0/uninstall.exe": guiExe("u")})

	out, err := ApplyPortable(t.Context(), st, portableRequest(folder, fake, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Discard(); err != nil {
		t.Fatal(err)
	}
	rec := out.Record
	if rec.Method != MethodPortable || rec.Folder != folder || rec.MainExe != "app.exe" || !rec.Dedicated || rec.SHA256 == "" {
		t.Errorf("record = %+v", rec)
	}
	if got := paths(rec.Files); !slices.Equal(got, []string{"app.exe", "data/a.dat", "uninstall.exe"}) {
		t.Errorf("recorded files = %v", got)
	}
	if got := onDisk(t, folder); !slices.Equal(got, []string{"app.exe", "data/a.dat", "uninstall.exe"}) {
		t.Errorf("on disk = %v", got)
	}
	shortcut := fake.Shortcuts()[rec.Shortcut]
	if rec.Shortcut == "" || shortcut.Target != filepath.Join(folder, "app.exe") {
		t.Errorf("shortcut = %q %+v", rec.Shortcut, shortcut)
	}
	if left := onDisk(t, apps); len(left) != 3 {
		t.Errorf("staging left files behind: %v", left)
	}
}

func TestAPortableAppWithoutAProgramInstallsWithoutALaunchTarget(t *testing.T) {
	apps := t.TempDir()
	fake := &platform.Fake{}
	st := stageFiles(t, apps, files{"readme.txt": "hi", "uninstall.exe": guiExe("")})
	out, err := ApplyPortable(t.Context(), st, portableRequest(filepath.Join(apps, "app"), fake, nil))
	if err != nil {
		t.Fatal(err)
	}
	if out.Record.MainExe != "" || out.Record.Shortcut != "" || len(fake.Shortcuts()) != 0 {
		t.Errorf("record = %+v, shortcuts = %v", out.Record, fake.Shortcuts())
	}
}

func TestAShortcutThatCannotBeMadeDoesNotFailTheInstall(t *testing.T) {
	apps := t.TempDir()
	fake := &platform.Fake{ShortcutErr: errors.New("no Start menu")}
	st := stageFiles(t, apps, files{"app.exe": guiExe("")})
	out, err := ApplyPortable(t.Context(), st, portableRequest(filepath.Join(apps, "app"), fake, nil))
	if err != nil || out.Warning == nil || out.Record.Shortcut != "" {
		t.Fatalf("ApplyPortable = %+v, %v", out, err)
	}
}

func TestAnUpdateKeepsUnknownFilesReplacesShippedOnesAndRemovesDroppedOnes(t *testing.T) {
	apps := t.TempDir()
	folder := filepath.Join(apps, "app")
	fake := &platform.Fake{}
	v1 := stageFiles(t, apps, files{"app.exe": guiExe("v1"), "lib/old.dll": "old", "lib/keep.dll": "keep-v1", "plugins/gone.txt": "gone"})
	first, err := ApplyPortable(t.Context(), v1, portableRequest(folder, fake, nil))
	if err != nil {
		t.Fatal(err)
	}
	// What the program saved in its own folder.
	files{"settings.json": "{}", "saves/slot1.sav": "save"}.write(t, folder)

	v2 := stageFiles(t, apps, files{"app.exe": guiExe("v2"), "lib/keep.dll": "keep-v2", "lib/new.dll": "new"})
	second, err := ApplyPortable(t.Context(), v2, portableRequest(folder, fake, &first.Record))
	if err != nil {
		t.Fatal(err)
	}
	if err := v2.Discard(); err != nil {
		t.Fatal(err)
	}

	want := []string{"app.exe", "lib/keep.dll", "lib/new.dll", "saves/slot1.sav", "settings.json"}
	if got := onDisk(t, folder); !slices.Equal(got, want) {
		t.Fatalf("on disk = %v, want %v", got, want)
	}
	if got := read(t, filepath.Join(folder, "lib", "keep.dll")); got != "keep-v2" {
		t.Errorf("keep.dll = %q", got)
	}
	if exists(filepath.Join(folder, "plugins")) {
		t.Error("the folder only the old version used is still there")
	}
	if got := paths(second.Record.Files); !slices.Equal(got, []string{"app.exe", "lib/keep.dll", "lib/new.dll"}) {
		t.Errorf("recorded files = %v: the program's own files must not be recorded", got)
	}
	if entries, _ := os.ReadDir(apps); len(entries) != 1 {
		t.Errorf("the apps folder holds %v, want only the app", entries)
	}
}

func TestAFailedReplacementPutsThePreviousVersionBack(t *testing.T) {
	apps := t.TempDir()
	folder := filepath.Join(apps, "app")
	fake := &platform.Fake{}
	v1 := stageFiles(t, apps, files{"app.exe": guiExe("v1"), "lib/a.dll": "a-v1", "old-only.txt": "old"})
	first, err := ApplyPortable(t.Context(), v1, portableRequest(folder, fake, nil))
	if err != nil {
		t.Fatal(err)
	}
	files{"settings.json": "{}"}.write(t, folder)
	before := onDisk(t, folder)

	v2 := stageFiles(t, apps, files{"app.exe": guiExe("v2"), "lib/a.dll": "a-v2", "lib/b.dll": "b-v2"})
	placed := 0
	failing := func(oldPath, newPath string) error {
		// Fail while putting the third new file in place, after the first two were.
		if strings.Contains(oldPath, ".stage-") {
			if placed++; placed == 3 {
				return errors.New("the file is in use")
			}
		}
		return os.Rename(oldPath, newPath)
	}
	swapRename(t, failing)

	_, err = ApplyPortable(t.Context(), v2, portableRequest(folder, fake, &first.Record))
	var replace *ReplaceError
	if !errors.As(err, &replace) || !replace.Restored {
		t.Fatalf("ApplyPortable = %v, want a restored ReplaceError", err)
	}
	if got := onDisk(t, folder); !slices.Equal(got, before) {
		t.Fatalf("on disk = %v, want the previous %v", got, before)
	}
	if got := read(t, filepath.Join(folder, "lib", "a.dll")); got != "a-v1" {
		t.Errorf("a.dll = %q, want the previous version", got)
	}
	if err := v2.Discard(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(apps); len(entries) != 1 {
		t.Errorf("the apps folder holds %v: the moved-aside files were not cleaned up", entries)
	}
}

func TestAReplacementThatCannotBePutBackSaysSo(t *testing.T) {
	apps := t.TempDir()
	folder := filepath.Join(apps, "app")
	fake := &platform.Fake{}
	v1 := stageFiles(t, apps, files{"app.exe": guiExe("v1")})
	first, err := ApplyPortable(t.Context(), v1, portableRequest(folder, fake, nil))
	if err != nil {
		t.Fatal(err)
	}
	v2 := stageFiles(t, apps, files{"app.exe": guiExe("v2")})
	swapRename(t, func(oldPath, newPath string) error {
		if strings.Contains(oldPath, ".stage-") || strings.Contains(oldPath, ".replaced-") {
			return errors.New("locked")
		}
		return os.Rename(oldPath, newPath)
	})
	_, err = ApplyPortable(t.Context(), v2, portableRequest(folder, fake, &first.Record))
	var replace *ReplaceError
	if !errors.As(err, &replace) || replace.Restored {
		t.Fatalf("ApplyPortable = %v, want a ReplaceError that was not restored", err)
	}
	if !strings.Contains(replace.Error(), "could not be put back") {
		t.Errorf("error = %q", replace.Error())
	}
}

// swapRename replaces how files are moved for the rest of the test.
func swapRename(t *testing.T, fn func(oldPath, newPath string) error) {
	t.Helper()
	original := renameFile
	renameFile = fn
	t.Cleanup(func() { renameFile = original })
}

func TestACanceledInstallLeavesTheMachineUntouched(t *testing.T) {
	apps := t.TempDir()
	folder := filepath.Join(apps, "app")
	st := stageFiles(t, apps, files{"app.exe": guiExe("")})
	ctx, cancel := contextCanceled(t)
	defer cancel()
	if _, err := ApplyPortable(ctx, st, portableRequest(folder, &platform.Fake{}, nil)); !errors.Is(err, ctx.Err()) {
		t.Fatalf("ApplyPortable = %v", err)
	}
	if exists(folder) {
		t.Error("the app folder was made")
	}
}

func TestRemovingAnAppInItsDedicatedFolderDeletesTheFolderAndTheShortcut(t *testing.T) {
	apps := t.TempDir()
	folder := filepath.Join(apps, "app")
	fake := &platform.Fake{}
	st := stageFiles(t, apps, files{"app.exe": guiExe("")})
	out, err := ApplyPortable(t.Context(), st, portableRequest(folder, fake, nil))
	if err != nil {
		t.Fatal(err)
	}
	files{"saves/slot1.sav": "save"}.write(t, folder)

	res, err := RemovePortable(out.Record, nil, fake)
	if err != nil || res.Warning != nil {
		t.Fatalf("RemovePortable = %+v, %v", res, err)
	}
	if exists(folder) || len(fake.Shortcuts()) != 0 {
		t.Errorf("folder exists: %v, shortcuts: %v", exists(folder), fake.Shortcuts())
	}
}

func TestRemovingAnAppInACustomFolderDeletesOnlyItsFiles(t *testing.T) {
	apps := t.TempDir()
	custom := filepath.Join(t.TempDir(), "mine")
	fake := &platform.Fake{}
	st := stageFiles(t, apps, files{"app.exe": guiExe(""), "lib/a.dll": "a"})
	req := portableRequest(custom, fake, nil)
	req.Dedicated = false
	out, err := ApplyPortable(t.Context(), st, req)
	if err != nil {
		t.Fatal(err)
	}
	files{"notes.txt": "mine", "saves/slot1.sav": "save", "lib/b.dll": "b"}.write(t, custom)

	res, err := RemovePortable(out.Record, nil, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got := onDisk(t, custom); !slices.Equal(got, []string{"lib/b.dll", "notes.txt", "saves/slot1.sav"}) {
		t.Errorf("on disk = %v", got)
	}
	if res.Removed != 2 || res.Left != 3 || len(fake.Shortcuts()) != 0 {
		t.Errorf("result = %+v, shortcuts = %v", res, fake.Shortcuts())
	}
}

func TestRemovingRefusesAFolderThatIsTooBroad(t *testing.T) {
	dir := t.TempDir()
	files{"keep.txt": "mine"}.write(t, dir)
	_, err := RemovePortable(Record{Method: MethodPortable, Folder: dir, Dedicated: true}, []string{dir}, &platform.Fake{})
	if !errors.Is(err, ErrTooBroad) || !exists(filepath.Join(dir, "keep.txt")) {
		t.Fatalf("RemovePortable = %v", err)
	}
}

func TestRemovingIgnoresRecordedPathsThatLeaveTheFolder(t *testing.T) {
	parent := t.TempDir()
	folder := filepath.Join(parent, "app")
	files{"app.exe": "x"}.write(t, folder)
	files{"outside.txt": "precious"}.write(t, parent)
	rec := Record{Method: MethodPortable, Folder: folder, Files: []File{{Path: "app.exe"}, {Path: "../outside.txt"}, {Path: "/abs"}}}
	if _, err := RemovePortable(rec, nil, &platform.Fake{}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(parent, "outside.txt")) || exists(filepath.Join(folder, "app.exe")) {
		t.Error("a recorded path outside the folder was followed, or the app file was kept")
	}
}

func TestFolderNamesAreDisambiguatedByOwner(t *testing.T) {
	apps := "apps"
	taken := func(dir string) bool { return dir == filepath.Join(apps, "tool") }
	if got := DefaultFolder(apps, "acme", "tool", nil); got != filepath.Join(apps, "tool") {
		t.Errorf("free name = %q", got)
	}
	if got := DefaultFolder(apps, "acme", "tool", taken); got != filepath.Join(apps, "tool-acme") {
		t.Errorf("taken name = %q", got)
	}
	if got := DefaultFolder(apps, "acme", "a:b?", nil); got != filepath.Join(apps, "a_b_") {
		t.Errorf("unsafe name = %q", got)
	}
}

func TestACustomFolderMustNotBeTooBroadAndNonEmptyOnesGetASubfolder(t *testing.T) {
	root := t.TempDir()
	protected := []string{filepath.Join(root, "Documents")}
	if _, err := CustomFolder(filepath.Join(root, "Documents"), "tool", protected); !errors.Is(err, ErrTooBroad) {
		t.Errorf("a protected folder: %v", err)
	}
	if _, err := CustomFolder(root, "tool", protected); !errors.Is(err, ErrTooBroad) {
		t.Errorf("a folder that holds a protected one: %v", err)
	}
	if _, err := CustomFolder(filepath.VolumeName(root)+string(filepath.Separator), "tool", nil); !errors.Is(err, ErrTooBroad) {
		t.Errorf("a drive root: %v", err)
	}
	if _, err := CustomFolder("relative/path", "tool", nil); !errors.Is(err, ErrTooBroad) {
		t.Errorf("a relative path: %v", err)
	}
	empty := filepath.Join(root, "empty")
	if got, err := CustomFolder(empty, "tool", protected); err != nil || got != empty {
		t.Errorf("a missing folder = %q, %v", got, err)
	}
	full := filepath.Join(root, "full")
	files{"other.txt": "x"}.write(t, full)
	if got, err := CustomFolder(full, "tool", protected); err != nil || got != filepath.Join(full, "tool") {
		t.Errorf("a folder with files = %q, %v", got, err)
	}
}

func TestMainExecutablePreference(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files files
		want  string
		ok    bool
	}{
		{"a graphical program beats a console one", files{"cli.exe": consoleExe("c"), "gui.exe": guiExe("g")}, "gui.exe", true},
		{"then the repository's name", files{"other.exe": guiExe("o"), "myapp.exe": guiExe("m")}, "myapp.exe", true},
		{"then the shallowest", files{"bin/x/a.exe": guiExe("1"), "z.exe": guiExe("2")}, "z.exe", true},
		{"then the largest", files{"a.exe": guiExe("short"), "b.exe": guiExe("a much longer tail")}, "b.exe", true},
		{"helpers and uninstallers are ignored", files{
			"unins000.exe": guiExe("1"), "crashpad.exe": guiExe("2"), "updater.exe": guiExe("3"),
			"vc_redist.x64.exe": guiExe("4"), "setup.exe": guiExe("5"), "myapp-helper.exe": guiExe("6"),
		}, "", false},
		{"no programs", files{"readme.txt": "x"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.files.write(t, dir)
			got, ok := MainExecutable(dir, tc.files.names(), "myapp")
			if got != tc.want || ok != tc.ok {
				t.Errorf("MainExecutable = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestExecutableKindReadsTheSubsystem(t *testing.T) {
	dir := t.TempDir()
	files{"console.exe": consoleExe(""), "gui.exe": guiExe(""), "text.txt": "hello", "other.exe": "MZ not really a program"}.write(t, dir)
	for name, want := range map[string]string{"console.exe": "tool", "gui.exe": "app", "text.txt": "", "other.exe": "", "missing.exe": ""} {
		if got := ExecutableKind(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("ExecutableKind(%s) = %q, want %q", name, got, want)
		}
	}
}
