package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronflorey/release-sync/platform"
)

// movable is a Portable app's folder, with the record the application keeps of it.
type movable struct {
	root, folder string
	record       Record
}

func newMovable(t *testing.T, dedicated bool, installed files, others files) movable {
	t.Helper()
	root := t.TempDir()
	folder := filepath.Join(root, "apps", "tool")
	installed.write(t, folder)
	others.write(t, folder)
	rec := Record{Method: MethodPortable, Folder: folder, Dedicated: dedicated, MainExe: "tool.exe", Shortcut: "menu/Tool.lnk"}
	for _, name := range installed.names() {
		rec.Files = append(rec.Files, File{Path: name})
	}
	return movable{root: root, folder: folder, record: rec}
}

func (m movable) request(dest string, p platform.Platform) MoveRequest {
	if p == nil {
		p = &platform.Fake{}
	}
	return MoveRequest{Record: m.record, Destination: dest, Platform: p}
}

func (m movable) dest() string { return filepath.Join(m.root, "moved") }

// content reads every file below dir, as a map of slash paths to contents.
func content(t *testing.T, dir string) files {
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

var program = files{"tool.exe": "program", "lib/a.dll": "library"}

func TestAFolderTheApplicationMadeMovesWholeWithWhatTheAppSaved(t *testing.T) {
	m := newMovable(t, true, program, files{"settings/save.dat": "my save"})
	if err := os.MkdirAll(filepath.Join(m.folder, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	fake := &platform.Fake{}
	if err := fake.CreateShortcut(platform.Shortcut{Path: "menu/Tool.lnk", Target: filepath.Join(m.folder, "tool.exe")}); err != nil {
		t.Fatal(err)
	}
	res, err := MovePortable(t.Context(), m.request(m.dest(), fake))
	if err != nil {
		t.Fatal(err)
	}
	if want := (files{"tool.exe": "program", "lib/a.dll": "library", "settings/save.dat": "my save"}); !sameFiles(content(t, m.dest()), want) {
		t.Errorf("moved files = %v", content(t, m.dest()))
	}
	if _, err := os.Stat(filepath.Join(m.dest(), "empty")); err != nil {
		t.Errorf("an empty folder was not moved: %v", err)
	}
	if _, err := os.Stat(m.folder); !os.IsNotExist(err) {
		t.Errorf("the old folder is still there: %v", err)
	}
	if res.Record.Folder != m.dest() || res.Record.Dedicated || res.Record.MainExe != "tool.exe" || res.Left != 0 {
		t.Errorf("result = %+v", res)
	}
	if res.Shortcuts != 1 || fake.Shortcuts()["menu/Tool.lnk"].Target != filepath.Join(m.dest(), "tool.exe") {
		t.Errorf("shortcuts = %d, %+v", res.Shortcuts, fake.Shortcuts())
	}
}

func TestOnlyTheInstalledFilesMoveOutOfAFolderThePersonChose(t *testing.T) {
	m := newMovable(t, false, program, files{"my-notes.txt": "mine", "my-project/code.c": "mine too"})
	res, err := MovePortable(t.Context(), m.request(m.dest(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFiles(content(t, m.dest()), program) {
		t.Errorf("moved files = %v", content(t, m.dest()))
	}
	if want := (files{"my-notes.txt": "mine", "my-project/code.c": "mine too"}); !sameFiles(content(t, m.folder), want) {
		t.Errorf("left behind = %v", content(t, m.folder))
	}
	if _, err := os.Stat(filepath.Join(m.folder, "lib")); !os.IsNotExist(err) {
		t.Errorf("the folder the move emptied is still there: %v", err)
	}
	if res.Record.Folder != m.dest() {
		t.Errorf("record = %+v", res.Record)
	}
}

func TestAMoveIsRefusedWhenItCannotBeSafe(t *testing.T) {
	m := newMovable(t, true, program, nil)
	occupied := filepath.Join(m.root, "occupied")
	files{"readme.txt": "x"}.write(t, occupied)
	empty := filepath.Join(m.root, "empty")
	if err := os.MkdirAll(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	notPortable := m
	notPortable.record.Method = MethodInstaller
	missing := m
	missing.record.Folder = filepath.Join(m.root, "gone")
	tight := &platform.Fake{SpaceFunc: func(string) (uint64, error) { return 3, nil }}
	for _, tc := range []struct {
		name string
		req  MoveRequest
		want error
	}{
		{"an installer's app", notPortable.request(m.dest(), nil), ErrNotMovable},
		{"a folder that is gone", missing.request(m.dest(), nil), ErrMoveMissing},
		{"the same folder", m.request(m.folder, nil), ErrMoveSameFolder},
		{"the same folder in another case", m.request(filepath.Join(m.root, "APPS", "Tool"), nil), ErrMoveSameFolder},
		{"a folder inside its own", m.request(filepath.Join(m.folder, "lib", "inner"), nil), ErrMoveInsideSource},
		{"a folder that is not empty", m.request(occupied, nil), ErrMoveNotEmpty},
		{"the folder that holds it", m.request(filepath.Join(m.root, "apps"), nil), ErrMoveNotEmpty},
		{"a drive without room", m.request(m.dest(), tight), ErrMoveNoSpace},
		{"a folder too broad", MoveRequest{Record: m.record, Destination: m.dest(), Protected: []string{filepath.Join(m.root, "moved", "inside")}, Platform: &platform.Fake{}}, ErrTooBroad},
	} {
		if err := CheckMove(tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: CheckMove = %v, want %v", tc.name, err, tc.want)
		}
		if _, err := MovePortable(t.Context(), tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: MovePortable = %v, want %v", tc.name, err, tc.want)
		}
	}
	if !sameFiles(content(t, m.folder), program) {
		t.Errorf("a refused move changed the app: %v", content(t, m.folder))
	}
	// An empty folder that exists is a fine place.
	if err := CheckMove(m.request(empty, nil)); err != nil {
		t.Errorf("an empty folder was refused: %v", err)
	}
}

func TestAMoveThatCannotMeasureSpaceGoesAhead(t *testing.T) {
	m := newMovable(t, true, program, nil)
	unknown := &platform.Fake{SpaceFunc: func(string) (uint64, error) { return 0, platform.ErrNotSupported }}
	if err := CheckMove(m.request(m.dest(), unknown)); err != nil {
		t.Fatal(err)
	}
}

// failOnCall makes the nth copy of a move fail.
func failOnCall(t *testing.T, n int, copied func(src, dst string) (string, error)) {
	t.Helper()
	calls := 0
	original := copyForMove
	copyForMove = func(src, dst string, mode os.FileMode) (string, error) {
		calls++
		if calls == n {
			return copied(src, dst)
		}
		return original(src, dst, mode)
	}
	t.Cleanup(func() { copyForMove = original })
}

func TestAMoveThatFailsMidwayLeavesTheSourceWholeAndNothingBehind(t *testing.T) {
	for name, tc := range map[string]struct {
		dedicated bool
		existing  bool
	}{
		"a whole folder to a new one":         {dedicated: true},
		"a whole folder to an empty one":      {dedicated: true, existing: true},
		"the installed files of a chosen one": {existing: true},
	} {
		m := newMovable(t, tc.dedicated, files{"a.exe": "a", "b/b.dll": "b", "c/c.dll": "c"}, files{"keep.txt": "mine"})
		m.record.MainExe = "a.exe"
		if tc.existing {
			if err := os.MkdirAll(m.dest(), 0o750); err != nil {
				t.Fatal(err)
			}
		}
		before := content(t, m.folder)
		failOnCall(t, 2, func(string, string) (string, error) { return "", errors.New("the disk is full") })
		if _, err := MovePortable(t.Context(), m.request(m.dest(), nil)); err == nil {
			t.Fatalf("%s: a failed copy went through", name)
		}
		if after := content(t, m.folder); !sameFiles(before, after) {
			t.Errorf("%s: source changed from %v to %v", name, before, after)
		}
		if left := content(t, m.dest()); len(left) != 0 {
			t.Errorf("%s: copies left behind: %v", name, left)
		}
		entries, err := os.ReadDir(m.dest())
		switch {
		case tc.existing && (err != nil || len(entries) != 0):
			t.Errorf("%s: the empty folder was not left empty: %v %v", name, entries, err)
		case !tc.existing && err == nil:
			t.Errorf("%s: the folder made for the move is still there", name)
		}
	}
}

func TestACopyThatDoesNotMatchItsOriginalIsNotKept(t *testing.T) {
	m := newMovable(t, true, program, nil)
	failOnCall(t, 1, func(src, dst string) (string, error) {
		if _, err := copyFile(src, dst, 0o600); err != nil {
			return "", err
		}
		return "digest-of-something-else", nil
	})
	if _, err := MovePortable(t.Context(), m.request(m.dest(), nil)); err == nil {
		t.Fatal("a copy that differs was accepted")
	}
	if !sameFiles(content(t, m.folder), program) {
		t.Errorf("source = %v", content(t, m.folder))
	}
	if _, err := os.Stat(m.dest()); !os.IsNotExist(err) {
		t.Errorf("the copy was kept: %v", err)
	}
}

func TestACanceledMoveLeavesTheSourceWhole(t *testing.T) {
	m := newMovable(t, true, program, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := MovePortable(ctx, m.request(m.dest(), nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !sameFiles(content(t, m.folder), program) {
		t.Errorf("source = %v", content(t, m.folder))
	}
	if _, err := os.Stat(m.dest()); !os.IsNotExist(err) {
		t.Errorf("the copy was kept: %v", err)
	}
}

func TestOriginalsThatCannotBeDeletedAreCountedNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a folder cannot be made read-only to root")
	}
	m := newMovable(t, false, program, nil)
	locked := filepath.Join(m.folder, "lib")
	if err := os.Chmod(locked, 0o500); err != nil { // #nosec G302 -- a folder has to be searchable.
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // #nosec G302 -- a folder has to be searchable.
	res, err := MovePortable(t.Context(), m.request(m.dest(), nil))
	if err != nil || res.Left != 1 || !sameFiles(content(t, m.dest()), program) {
		t.Fatalf("result = %+v, %v; moved %v", res, err, content(t, m.dest()))
	}
}

func TestAShortcutThatCannotBeRepointedIsAWarning(t *testing.T) {
	m := newMovable(t, true, program, nil)
	boom := errors.New("access denied")
	res, err := MovePortable(t.Context(), m.request(m.dest(), &platform.Fake{ShortcutErr: boom}))
	if err != nil || !errors.Is(res.Warning, boom) || res.Record.Folder != m.dest() {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

func TestStageFolderKeepsALoneTopLevelFolderAndRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "app.zip")
	if err := os.WriteFile(archive, zipOf(t, files{"only/inner.txt": "x"}), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := StageFolder(t.Context(), archive, filepath.Join(dir, "parent"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Discard() }()
	if got, err := st.Files(); err != nil || len(got) != 1 || got[0] != "only/inner.txt" {
		t.Fatalf("files = %v, %v", got, err)
	}

	evil := filepath.Join(dir, "evil.zip")
	if err := os.WriteFile(evil, zipOf(t, files{"../outside.txt": "x"}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StageFolder(t.Context(), evil, filepath.Join(dir, "parent")); !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.txt")); !os.IsNotExist(err) {
		t.Errorf("an entry escaped the folder: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "parent")); len(entries) != 1 {
		t.Errorf("a failed staging left %d folders behind", len(entries)-1)
	}
}
