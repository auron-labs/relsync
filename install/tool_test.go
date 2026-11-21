package install

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
)

// stageTool stages a release in a folder of its own, away from the tools folder.
func stageTool(t *testing.T, _ string, f files) *Staged {
	t.Helper()
	st, err := Stage(t.Context(), download(t, "tool.zip", zipOf(t, f)), "tool.zip", selection.ArtifactArchive, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Discard() })
	return st
}

func chosen(t *testing.T, st *Staged, recorded []string) []string {
	t.Helper()
	members, err := ToolMembers(st, windowsX64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ChooseCommands(members, recorded, "tool", "tool")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAToolFromAnArchiveInstallsItsCommandOnThePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	fake := &platform.Fake{}
	st := stageTool(t, dir, files{
		"tool-v1.2.0-windows-amd64/tool.exe": consoleExe("one"), "tool-v1.2.0-windows-amd64/README.md": "docs",
		"tool-v1.2.0-windows-amd64/LICENSE": "mit", "tool-v1.2.0-windows-amd64/completions/tool.cmd": "@echo off",
		"tool-v1.2.0-windows-amd64/tool.dll": consoleExe("library"),
	})
	members := chosen(t, st, nil)
	if !slices.Equal(members, []string{"tool.exe"}) {
		t.Fatalf("chosen = %v", members)
	}

	out, err := ApplyTool(t.Context(), st, ToolRequest{Dir: dir, Asset: "tool.zip", Members: members, Platform: fake})
	if err != nil {
		t.Fatal(err)
	}
	if got := onDisk(t, dir); !slices.Equal(got, []string{"tool.exe"}) {
		t.Errorf("tools folder = %v", got)
	}
	rec := out.Record
	if rec.Method != MethodTool || rec.Folder != dir || rec.SHA256 == "" || len(rec.Files) != 1 ||
		rec.Files[0].Command != "tool" || rec.Files[0].Path != "tool.exe" || rec.Files[0].SHA256 != rec.SHA256 {
		t.Errorf("record = %+v", rec)
	}
	if !out.PathAdded || !slices.Equal(fake.UserPath(), []string{dir}) {
		t.Errorf("PathAdded = %v, PATH = %v", out.PathAdded, fake.UserPath())
	}
	again, err := ApplyTool(t.Context(), st, ToolRequest{Dir: dir, Asset: "tool.zip", Members: members, Previous: &rec, Platform: fake})
	if err != nil || again.PathAdded {
		t.Errorf("second install: PathAdded = %v, %v; the PATH is told once", again.PathAdded, err)
	}
}

func TestAStandaloneToolIsNamedWithoutItsVersionAndTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	st, err := Stage(t.Context(), download(t, "x", []byte(consoleExe(""))), "rg-14.1.0-x86_64-pc-windows-msvc.exe", selection.ArtifactStandaloneExe, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	members, err := ToolMembers(st, windowsX64)
	if err != nil {
		t.Fatal(err)
	}
	picked, err := ChooseCommands(members, nil, "ripgrep", "rg")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ApplyTool(t.Context(), st, ToolRequest{Dir: dir, Members: picked, Platform: &platform.Fake{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Record.Files[0]; got.Path != "rg.exe" || got.Command != "rg" {
		t.Errorf("file = %+v", got)
	}
}

func TestAnArchiveCanProvideSeveralCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	st := stageTool(t, dir, files{"README": "r", "bin/tool.exe": consoleExe("1"), "bin/tool-extra.exe": consoleExe("2"), "bin/run.cmd": "@echo hi"})
	members, err := ToolMembers(st, windowsX64)
	if err != nil {
		t.Fatal(err)
	}
	var choice *ChoiceError
	picked, err := ChooseCommands(members, nil, "tool", "tool")
	if err != nil || !slices.Equal(picked, []string{"bin/tool.exe"}) {
		t.Fatalf("the member named for the repository is the default: %v, %v", picked, err)
	}
	picked, err = ChooseCommands(members, nil, "other", "other")
	if !errors.As(err, &choice) || !slices.Equal(choice.Candidates, []string{"bin/run.cmd", "bin/tool-extra.exe", "bin/tool.exe"}) {
		t.Fatalf("with no clear default the user is asked: %v, %v", picked, err)
	}

	out, err := ApplyTool(t.Context(), st, ToolRequest{Dir: dir, Members: []string{"bin/tool.exe", "bin/run.cmd"}, Platform: &platform.Fake{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := onDisk(t, dir); !slices.Equal(got, []string{"run.cmd", "tool.exe"}) {
		t.Errorf("tools folder = %v", got)
	}
	if len(out.Record.Files) != 2 {
		t.Errorf("record = %+v", out.Record)
	}
}

func TestMembersAreChosenByTheirBytesNotTheirNames(t *testing.T) {
	st := stageTool(t, t.TempDir(), files{"tool.exe": "not a program", "other.exe": consoleExe(""), "docs/guide.exe": consoleExe("g")})
	members, err := ToolMembers(st, windowsX64)
	if err != nil {
		t.Fatal(err)
	}
	want := []selection.Member{{Identity: "other.exe", Eligible: true}, {Identity: "tool.exe", Eligible: false}}
	slices.SortFunc(members, func(a, b selection.Member) int { return strings.Compare(a.Identity, b.Identity) })
	if !slices.Equal(members, want) {
		t.Errorf("members = %+v, want %+v", members, want)
	}
	if _, err := ChooseCommands([]selection.Member{{Identity: "tool.exe"}}, nil, "tool", "tool"); !errors.Is(err, ErrNoProgram) {
		t.Errorf("no eligible member: %v", err)
	}
}

func TestMembersRecordedByAnEarlierReleaseAreFoundAgainWhateverTheWrapperFolder(t *testing.T) {
	st := stageTool(t, t.TempDir(), files{"README": "r", "tool-2.0.0/bin/tool.exe": consoleExe(""), "tool-2.0.0/bin/other.exe": consoleExe("o")})
	got := chosen(t, st, []string{"tool-1.0.0/bin/tool.exe"})
	if !slices.Equal(got, []string{"tool-2.0.0/bin/tool.exe"}) {
		t.Errorf("chosen = %v", got)
	}
}

func TestMembersMissingFromALaterReleaseNeedAChoice(t *testing.T) {
	st := stageTool(t, t.TempDir(), files{"README": "r", "bin/tool.exe": consoleExe(""), "bin/new.exe": consoleExe("n")})
	members, err := ToolMembers(st, windowsX64)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ChooseCommands(members, []string{"bin/tool.exe", "bin/removed.exe"}, "tool", "tool")
	var choice *ChoiceError
	if !errors.As(err, &choice) || !slices.Equal(choice.Missing, []string{"bin/removed.exe"}) || !slices.Equal(choice.Kept, []string{"bin/tool.exe"}) ||
		!slices.Equal(choice.Candidates, []string{"bin/new.exe", "bin/tool.exe"}) {
		t.Fatalf("ChooseCommands = %v", err)
	}
}

func TestACommandAnotherItemOwnsBlocksTheInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	st := stageTool(t, dir, files{"tool.exe": consoleExe("")})
	fake := &platform.Fake{}
	_, err := ApplyTool(t.Context(), st, ToolRequest{Dir: dir, Members: []string{"tool.exe"}, Owners: map[string]string{"tool": "acme/other-tool"}, Platform: fake})
	var taken *CommandTakenError
	if !errors.As(err, &taken) || taken.Owner != "acme/other-tool" || taken.Command != "tool" {
		t.Fatalf("ApplyTool = %v", err)
	}
	if exists(dir) || len(fake.UserPath()) != 0 {
		t.Error("a blocked install changed the machine")
	}
}

func TestAFileThatIsNotOursNeedsPermissionToBeReplaced(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	files{"tool.exe": "somebody else's"}.write(t, dir)
	st := stageTool(t, dir, files{"tool.exe": consoleExe("mine")})
	req := ToolRequest{Dir: dir, Members: []string{"tool.exe"}, Platform: &platform.Fake{}}

	_, err := ApplyTool(t.Context(), st, req)
	var foreign *ForeignFileError
	if !errors.As(err, &foreign) || foreign.Command != "tool" {
		t.Fatalf("ApplyTool = %v", err)
	}
	if got := read(t, filepath.Join(dir, "tool.exe")); got != "somebody else's" {
		t.Fatalf("the file was changed before the user agreed: %q", got)
	}

	req.Replace = map[string]bool{"tool": true}
	if _, err := ApplyTool(t.Context(), st, req); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "tool.exe")); got != consoleExe("mine") {
		t.Errorf("the file was not replaced")
	}
}

func TestUpdatingAToolWhileItRunsLeavesTheOldFileToBeRemovedLater(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	fake := &platform.Fake{}
	v1 := stageTool(t, dir, files{"tool.exe": consoleExe("v1")})
	first, err := ApplyTool(t.Context(), v1, ToolRequest{Dir: dir, Members: []string{"tool.exe"}, Platform: fake})
	if err != nil {
		t.Fatal(err)
	}

	// A program that runs cannot be deleted on Windows, only renamed.
	original := removeFile
	removeFile = func(path string) error {
		if strings.HasSuffix(path, asideSuffix) {
			return errors.New("the file is in use")
		}
		return original(path)
	}
	t.Cleanup(func() { removeFile = original })

	v2 := stageTool(t, dir, files{"tool.exe": consoleExe("v2")})
	second, err := ApplyTool(t.Context(), v2, ToolRequest{Dir: dir, Members: []string{"tool.exe"}, Previous: &first.Record, Platform: fake})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "tool.exe")); got != consoleExe("v2") {
		t.Fatalf("tool.exe is not the new version")
	}
	if second.Record.SHA256 == first.Record.SHA256 {
		t.Error("the installed file's hash was not updated")
	}
	leftovers := slices.DeleteFunc(onDisk(t, dir), func(p string) bool { return !strings.HasSuffix(p, asideSuffix) })
	if len(leftovers) != 1 {
		t.Fatalf("expected the old file renamed aside, found %v", leftovers)
	}

	removeFile = original
	CleanLeftovers(dir)
	if got := onDisk(t, dir); !slices.Equal(got, []string{"tool.exe"}) {
		t.Errorf("after cleaning up: %v", got)
	}
}

func TestAnUpdateThatDropsACommandRemovesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	fake := &platform.Fake{}
	v1 := stageTool(t, dir, files{"tool.exe": consoleExe("1"), "helper.exe": consoleExe("2")})
	first, err := ApplyTool(t.Context(), v1, ToolRequest{Dir: dir, Members: []string{"tool.exe", "helper.exe"}, Platform: fake})
	if err != nil {
		t.Fatal(err)
	}
	v2 := stageTool(t, dir, files{"tool.exe": consoleExe("3")})
	if _, err := ApplyTool(t.Context(), v2, ToolRequest{Dir: dir, Members: []string{"tool.exe"}, Previous: &first.Record, Platform: fake}); err != nil {
		t.Fatal(err)
	}
	if got := onDisk(t, dir); !slices.Equal(got, []string{"tool.exe"}) {
		t.Errorf("tools folder = %v", got)
	}
}

func TestAFailedToolUpdatePutsThePreviousCommandsBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	fake := &platform.Fake{}
	v1 := stageTool(t, dir, files{"a.exe": consoleExe("a1"), "b.exe": consoleExe("b1")})
	first, err := ApplyTool(t.Context(), v1, ToolRequest{Dir: dir, Members: []string{"a.exe", "b.exe"}, Platform: fake})
	if err != nil {
		t.Fatal(err)
	}
	v2 := stageTool(t, dir, files{"a.exe": consoleExe("a2"), "b.exe": consoleExe("b2")})
	swapRename(t, func(oldPath, newPath string) error {
		if strings.HasSuffix(oldPath, newSuffix) && strings.Contains(oldPath, "b.exe") {
			return errors.New("locked")
		}
		return os.Rename(oldPath, newPath)
	})

	_, err = ApplyTool(t.Context(), v2, ToolRequest{Dir: dir, Members: []string{"a.exe", "b.exe"}, Previous: &first.Record, Platform: fake})
	var replace *ReplaceError
	if !errors.As(err, &replace) || !replace.Restored {
		t.Fatalf("ApplyTool = %v", err)
	}
	if got := onDisk(t, dir); !slices.Equal(got, []string{"a.exe", "b.exe"}) {
		t.Errorf("tools folder = %v: new or temporary files were left", got)
	}
	if read(t, filepath.Join(dir, "a.exe")) != consoleExe("a1") || read(t, filepath.Join(dir, "b.exe")) != consoleExe("b1") {
		t.Error("the previous commands were not restored")
	}
}

func TestTwoProgramsWithOneCommandNameAreRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	st := stageTool(t, dir, files{"x64/tool.exe": consoleExe("1"), "x86/tool.exe": consoleExe("2")})
	_, err := ApplyTool(t.Context(), st, ToolRequest{Dir: dir, Members: []string{"x64/tool.exe", "x86/tool.exe"}, Platform: &platform.Fake{}})
	if !errors.Is(err, ErrDuplicateCommand) {
		t.Errorf("ApplyTool = %v", err)
	}
}

func TestRemovingAToolDeletesItsCommandsAndLeavesThePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	fake := &platform.Fake{}
	st := stageTool(t, dir, files{"tool.exe": consoleExe("1")})
	out, err := ApplyTool(t.Context(), st, ToolRequest{Dir: dir, Members: []string{"tool.exe"}, Platform: fake})
	if err != nil {
		t.Fatal(err)
	}
	files{"other.exe": "someone else's"}.write(t, dir)

	res, err := RemoveTool(out.Record)
	if err != nil || res.Removed != 1 {
		t.Fatalf("RemoveTool = %+v, %v", res, err)
	}
	if got := onDisk(t, dir); !slices.Equal(got, []string{"other.exe"}) {
		t.Errorf("tools folder = %v", got)
	}
	if !slices.Equal(fake.UserPath(), []string{dir}) {
		t.Errorf("PATH = %v", fake.UserPath())
	}
}

func TestACanceledToolInstallLeavesNothingBehind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	st := stageTool(t, dir, files{"tool.exe": consoleExe("")})
	ctx, cancel := contextCanceled(t)
	defer cancel()
	if _, err := ApplyTool(ctx, st, ToolRequest{Dir: dir, Members: []string{"tool.exe"}, Platform: &platform.Fake{}}); !errors.Is(err, ctx.Err()) {
		t.Fatalf("ApplyTool = %v", err)
	}
	if exists(dir) && len(onDisk(t, dir)) != 0 {
		t.Errorf("left behind: %v", onDisk(t, dir))
	}
}

func TestAToolCommandThatAnotherProgramEarlierOnThePathWinsIsReported(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tools")
	elsewhere := filepath.Join(t.TempDir(), "bin")
	st := stageTool(t, dir, files{"tool.exe": consoleExe("one"), "other.exe": consoleExe("two")})
	lookPath := func(file string) (string, error) {
		switch file {
		case "tool.exe":
			return filepath.Join(elsewhere, file), nil
		case "other.exe":
			return filepath.Join(dir, file), nil // This application's own copy wins.
		}
		return "", exec.ErrNotFound
	}
	out, err := ApplyTool(t.Context(), st, ToolRequest{
		Dir: dir, Asset: "tool.zip", Members: []string{"other.exe", "tool.exe"}, Platform: &platform.Fake{}, LookPath: lookPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Shadowed) != 1 || out.Shadowed[0] != (Shadow{Command: "tool", Folder: elsewhere}) {
		t.Errorf("shadowed = %+v", out.Shadowed)
	}

	// A command that is not on the PATH at all is nothing to report.
	st = stageTool(t, dir, files{"tool.exe": consoleExe("one")})
	out, err = ApplyTool(t.Context(), st, ToolRequest{
		Dir: dir, Asset: "tool.zip", Members: []string{"tool.exe"}, Platform: &platform.Fake{}, Previous: &out.Record,
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	})
	if err != nil || len(out.Shadowed) != 0 {
		t.Errorf("shadowed = %+v, %v", out.Shadowed, err)
	}
}
