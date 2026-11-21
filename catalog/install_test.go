package catalog_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

func (h *harness) trackTool(name string) catalog.Entry {
	h.t.Helper()
	h.source.publish("acme", name, page(`"e1"`, rel("v1.0.0", 1, func(r *source.Release) {
		r.Assets = []source.Asset{{Name: name + "-windows-x64.zip", Size: 9}}
	})))
	return h.add(name, catalog.AddOptions{})
}

func TestAnInstallRecordAndItsFilesSurviveARestartAndAreReplacedByTheNextInstall(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	members := &selection.Intent{Product: "tool", Kind: selection.ArtifactArchive, ArchiveMembers: []string{"bin/tool.exe", "bin/other.exe"}}

	rec := install.Record{
		Method: install.MethodTool, Asset: "tool-windows-x64.zip", Folder: `C:\tools`, SHA256: "abc",
		Files: []install.File{{Path: "tool.exe", SHA256: "abc", Command: "tool"}, {Path: "other.exe", SHA256: "def", Command: "other"}},
	}
	if err := h.catalog.SetInstallError(t.Context(), e.Item.ID, "earlier failure"); err != nil {
		t.Fatal(err)
	}
	err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: rec, Kind: selection.ItemTool, Intent: members})
	if err != nil {
		t.Fatal(err)
	}

	check := func(when string) {
		t.Helper()
		got := h.get(e.Item.ID)
		in, ok := got.Installed()
		if !ok || in.Tag != "v1.0.0" || in.Method != install.MethodTool || in.Folder != `C:\tools` || in.Asset != "tool-windows-x64.zip" ||
			in.SHA256 != "abc" || !slices.Equal(in.Commands, []string{"other", "tool"}) {
			t.Errorf("%s: install = %+v, %v", when, in, ok)
		}
		if got.Item.Kind != selection.ItemTool || got.Item.InstallError != "" {
			t.Errorf("%s: kind %q, install error %q", when, got.Item.Kind, got.Item.InstallError)
		}
		if got.Intent == nil || !reflect.DeepEqual(got.Intent.ArchiveMembers, members.ArchiveMembers) {
			t.Errorf("%s: intent = %+v", when, got.Intent)
		}
		files, err := h.catalog.InstallFiles(t.Context(), e.Item.ID)
		if err != nil || len(files) != 2 || files[0].Path != "other.exe" || files[1].Command != "tool" {
			t.Errorf("%s: files = %+v, %v", when, files, err)
		}
	}
	check("recorded")
	h.reopen()
	check("after a restart")

	next := install.Record{Method: install.MethodTool, Folder: `C:\tools`, Files: []install.File{{Path: "tool.exe", Command: "tool"}}}
	if err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.1.0", Record: next}); err != nil {
		t.Fatal(err)
	}
	got := h.get(e.Item.ID)
	files, _ := h.catalog.InstallFiles(t.Context(), e.Item.ID)
	if in, _ := got.Installed(); in.Tag != "v1.1.0" || !slices.Equal(in.Commands, []string{"tool"}) || len(files) != 1 {
		t.Errorf("replaced install = %+v, files %v", in, files)
	}
	if got.Item.Kind != selection.ItemTool || got.Intent == nil {
		t.Errorf("an install that gives no kind or intent must keep them: %q, %+v", got.Item.Kind, got.Intent)
	}
}

func TestClearingAnInstallForgetsItsFilesButKeepsTheItem(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	rec := install.Record{Method: install.MethodPortable, Folder: `C:\apps\tool`, Dedicated: true, MainExe: "tool.exe", Files: []install.File{{Path: "tool.exe"}}}
	if err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: rec}); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.ClearInstall(t.Context(), e.Item.ID); err != nil {
		t.Fatal(err)
	}
	got := h.get(e.Item.ID)
	files, err := h.catalog.InstallFiles(t.Context(), e.Item.ID)
	if got.Install != nil || len(files) != 0 || err != nil || got.Status(false) != catalog.StatusNotInstalled {
		t.Errorf("install %+v, files %v, %v, status %v", got.Install, files, err, got.Status(false))
	}
}

func TestStoppingTrackingAnInstalledItemRemovesItsRecordAndFiles(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	rec := install.Record{Method: install.MethodPortable, Folder: `C:\apps\tool`, Files: []install.File{{Path: "tool.exe"}}}
	if err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: rec}); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.Remove(t.Context(), e.Item.ID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := h.db.SQL.QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM installs) + (SELECT COUNT(*) FROM install_files)`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("rows left = %d, %v", rows, err)
	}
}

func TestAMarkedInstallIsNotManaged(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page(`"e1"`, rel("v1.0.0", 1)))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true})
	if _, ok := h.get(e.Item.ID).Installed(); ok {
		t.Error("a version the user only says they have is a managed install")
	}
}

func TestInstallErrorAndFolderArePerItemSettings(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page(`"e1"`, rel("v1.0.0", 1)))
	e := h.add("tool", catalog.AddOptions{InstallFolder: `D:\mine`})
	if e.Item.InstallFolder != `D:\mine` {
		t.Errorf("folder from the add options = %q", e.Item.InstallFolder)
	}
	if err := h.catalog.SetInstallFolder(t.Context(), e.Item.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.SetInstallError(t.Context(), e.Item.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if got := h.get(e.Item.ID); got.Item.InstallFolder != "" || got.Item.InstallError != "boom" {
		t.Errorf("item = %+v", got.Item)
	}
}

func TestClaimsListWhatOtherItemsHaveTaken(t *testing.T) {
	h := openHarness(t)
	a, b := h.trackTool("alpha"), h.trackTool("beta")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(h.catalog.RecordInstall(t.Context(), a.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: install.Record{
		Method: install.MethodPortable, Folder: `C:\Apps\alpha`, Files: []install.File{{Path: "alpha.exe"}},
	}}))
	must(h.catalog.RecordInstall(t.Context(), b.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: install.Record{
		Method: install.MethodTool, Folder: `C:\tools`, Files: []install.File{{Path: "Beta.exe", Command: "Beta"}},
	}}))

	claims := catalog.ClaimsExcept(h.catalog.Entries().Get(), b.Item.ID)
	if claims.Folders[`c:\apps\alpha`] != "acme/alpha" || len(claims.Commands) != 0 {
		t.Errorf("claims except beta = %+v", claims)
	}
	claims = catalog.ClaimsExcept(h.catalog.Entries().Get(), a.Item.ID)
	if claims.Commands["beta"] != "acme/beta" || len(claims.Folders) != 0 {
		t.Errorf("claims except alpha = %+v", claims)
	}
}

func TestRelationComparesAReleaseWithTheInstalledVersion(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page(`"e1"`, rel("v2.0.0", 5), rel("v1.0.0", 1)))
	fresh := h.add("tool", catalog.AddOptions{})
	if got := fresh.Relation("v1.0.0"); got != catalog.RelationFresh {
		t.Errorf("not installed: %v", got)
	}
	if err := h.catalog.MarkInstalled(t.Context(), fresh.Item.ID, "v1.5.0"); err != nil {
		t.Fatal(err)
	}
	e := h.get(fresh.Item.ID)
	for tag, want := range map[string]catalog.Relation{
		"v2.0.0": catalog.RelationNewer, "v1.5.0": catalog.RelationSame, "v1.0.0": catalog.RelationOlder,
	} {
		if got := e.Relation(tag); got != want {
			t.Errorf("Relation(%s) = %v, want %v", tag, got, want)
		}
	}
	if r, ok := e.Release("v1.0.0"); !ok || r.Tag != "v1.0.0" {
		t.Errorf("Release = %+v, %v", r, ok)
	}
	if _, ok := e.Release("v9.9.9"); ok {
		t.Error("a release that is not cached was found")
	}
}

func TestAnInstallersRecordSurvivesARestart(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	rec := install.Record{
		Method: install.MethodInstaller, Asset: "tool.msi", Folder: `C:\Program Files\Tool`, MainExe: "tool.exe",
		UninstallKey: "LocalMachine|64|{ABC}", Scope: platform.ScopeMachine, EntryVersion: "1.0.0.0",
	}
	if err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: rec}); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	in, ok := h.get(e.Item.ID).Installed()
	if !ok || in.Method != install.MethodInstaller || in.UninstallKey != rec.UninstallKey || in.Scope != platform.ScopeMachine ||
		in.EntryVersion != "1.0.0.0" || !in.Removable() {
		t.Errorf("install = %+v, %v", in, ok)
	}
}

func TestAnInstallerProgramTheSystemDoesNotListCannotBeRemoved(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	unlisted := install.Record{Method: install.MethodInstaller, Asset: "tool-setup.exe"}
	if err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: unlisted}); err != nil {
		t.Fatal(err)
	}
	in, _ := h.get(e.Item.ID).Installed()
	if in.Removable() {
		t.Fatalf("an unlisted installer install is removable: %+v", in)
	}

	if err := h.catalog.LocateProgram(t.Context(), e.Item.ID, `D:\Tool`, "tool.exe"); err != nil {
		t.Fatal(err)
	}
	in, _ = h.get(e.Item.ID).Installed()
	if in.Folder != `D:\Tool` || in.MainExe != "tool.exe" || in.Tag != "v1.0.0" {
		t.Errorf("located install = %+v", in)
	}
	pkg := install.Record{Method: install.MethodInstaller, Package: "Acme.Tool_1.0.0.0_x64__abc"}
	if err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: pkg}); err != nil {
		t.Fatal(err)
	}
	if in, _ = h.get(e.Item.ID).Installed(); !in.Removable() {
		t.Errorf("an MSIX package is removable by its name: %+v", in)
	}
}

func TestLocatingAProgramNeedsAnInstall(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	if err := h.catalog.LocateProgram(t.Context(), e.Item.ID, `D:\Tool`, "tool.exe"); !errors.Is(err, catalog.ErrNotInstalled) {
		t.Errorf("LocateProgram = %v, want ErrNotInstalled", err)
	}
	if err := h.catalog.MoveInstalled(t.Context(), e.Item.ID, "v1.0.0", "1.0"); !errors.Is(err, catalog.ErrNotInstalled) {
		t.Errorf("MoveInstalled = %v, want ErrNotInstalled", err)
	}
}

func TestInstallerOptionsArePerItemSettings(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	if err := h.catalog.SetInstallerArgs(t.Context(), e.Item.ID, "  /S /LANG=en  "); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.SetInstallForEveryone(t.Context(), e.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if got := h.get(e.Item.ID).Item; got.InstallerArgs != "/S /LANG=en" || !got.InstallForEveryone {
		t.Errorf("item = %+v", got)
	}
}

func TestMovingTheInstalledVersionKeepsTheRestOfTheRecord(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	rec := install.Record{Method: install.MethodInstaller, UninstallKey: "k", EntryVersion: "1.0.0"}
	if err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: rec}); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.MoveInstalled(t.Context(), e.Item.ID, "v1.2.0", "1.2.0"); err != nil {
		t.Fatal(err)
	}
	in, _ := h.get(e.Item.ID).Installed()
	if in.Tag != "v1.2.0" || in.EntryVersion != "1.2.0" || in.UninstallKey != "k" {
		t.Errorf("install = %+v", in)
	}
}

func TestReleaseForVersionFindsANewerTrackedRelease(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page(`"e1"`, rel("v1.2.0", 3), rel("v1.1.0", 2), rel("v1.0.0", 1)))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	for _, tc := range []struct{ seen, want string }{
		{"1.2.0.0", "v1.2.0"}, {"1.1.0", "v1.1.0"}, {"1.0.0", ""}, {"9.9.9", ""}, {"", ""}, {"1", ""},
	} {
		got, ok := h.get(e.Item.ID).ReleaseForVersion(tc.seen)
		if got.Tag != tc.want || ok != (tc.want != "") {
			t.Errorf("ReleaseForVersion(%q) = %q, %v; want %q", tc.seen, got.Tag, ok, tc.want)
		}
	}
}

func TestMovingAPortableInstallKeepsItsVersionAndFilesAndOnlyChangesWhereItIs(t *testing.T) {
	h := openHarness(t)
	e := h.trackTool("tool")
	rec := install.Record{
		Method: install.MethodPortable, Asset: "tool-windows-x64.zip", Folder: `C:\Apps\tool`, Dedicated: true, MainExe: "tool.exe",
		Files: []install.File{{Path: "tool.exe"}, {Path: "lib/a.dll"}},
	}
	if err := h.catalog.RecordInstall(t.Context(), e.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: rec}); err != nil {
		t.Fatal(err)
	}
	before, _ := h.get(e.Item.ID).Installed()

	if err := h.catalog.MoveInstall(t.Context(), e.Item.ID, `D:\Elsewhere`, false); err != nil {
		t.Fatal(err)
	}
	got, _ := h.get(e.Item.ID).Installed()
	if got.Folder != `D:\Elsewhere` || got.Dedicated || got.Tag != "v1.0.0" || got.MainExe != "tool.exe" || !got.At.Equal(before.At) {
		t.Errorf("install = %+v", got)
	}
	if files, err := h.catalog.InstallFiles(t.Context(), e.Item.ID); err != nil || len(files) != 2 {
		t.Errorf("files = %+v, %v", files, err)
	}

	tool := h.trackTool("other")
	if err := h.catalog.MoveInstall(t.Context(), tool.Item.ID, `D:\Elsewhere`, false); !errors.Is(err, catalog.ErrNotInstalled) {
		t.Errorf("moving an app that is not installed: %v", err)
	}
	installer := install.Record{Method: install.MethodInstaller, UninstallKey: "key"}
	if err := h.catalog.RecordInstall(t.Context(), tool.Item.ID, catalog.Installed{Tag: "v1.0.0", Record: installer}); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.MoveInstall(t.Context(), tool.Item.ID, `D:\Elsewhere`, false); !errors.Is(err, catalog.ErrNotInstalled) {
		t.Errorf("moving an installer-based app: %v", err)
	}
}
