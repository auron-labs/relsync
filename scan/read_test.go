package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/platform"
)

// touch makes a file of the given size, and its folders.
func touch(t *testing.T, size int, parts ...string) string {
	t.Helper()
	path := filepath.Join(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func write(t *testing.T, content string, parts ...string) string {
	t.Helper()
	path := filepath.Join(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func byName(t *testing.T, candidates []Candidate, name string) Candidate {
	t.Helper()
	for _, c := range candidates {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no candidate named %q among %v", name, names(candidates))
	return Candidate{}
}

func names(candidates []Candidate) []string {
	out := make([]string, len(candidates))
	for i, c := range candidates {
		out[i] = c.Name
	}
	return out
}

func TestFromProgramsMergesByNameAndLeavesOutWhatTheLibraryHolds(t *testing.T) {
	got := FromPrograms([]platform.Program{
		{Key: "m|7zip", Name: "7-Zip", Version: "24.0", Publisher: "Igor Pavlov"},
		{Key: "u|7zip", Name: "7-ZIP", Version: "23.0"},
		{Key: "u|held", Name: "Held Tool"},
		{Key: "u|b", Name: "bee", AboutURL: "https://github.com/acme/bee", Comments: "c", HelpLink: "h", UpdateURL: "u"},
	}, map[string]bool{"u|held": true})
	if !slices.Equal(names(got), []string{"7-Zip", "bee"}) || got[0].Program.Key != "m|7zip" || got[0].Version != "24.0" {
		t.Fatalf("candidates = %+v", got)
	}
	// The link fields are read most trusted first: update, about, help, comments.
	if !slices.Equal(got[1].Texts, []string{"u", "https://github.com/acme/bee", "h", "c"}) {
		t.Errorf("texts = %q", got[1].Texts)
	}
}

// scanFixture is a machine with programs registered, shortcuts, and folders.
type scanFixture struct {
	fake *platform.Fake
	root string
}

func newScanFixture(t *testing.T) *scanFixture {
	t.Helper()
	return &scanFixture{fake: &platform.Fake{Known: platform.Folders{}}, root: t.TempDir()}
}

func (f *scanFixture) sources(folders ...string) Sources {
	return Sources{Platform: f.fake, Folders: folders}
}

func TestAProgramIsLinkedByWhicheverLinkFieldNamesARepository(t *testing.T) {
	f := newScanFixture(t)
	for _, p := range []platform.Program{
		{Key: "k1", Name: "By Update", UpdateURL: "https://github.com/acme/by-update/releases"},
		{Key: "k2", Name: "By About", AboutURL: "https://github.com/acme/by-about"},
		{Key: "k3", Name: "By Help", HelpLink: "https://github.com/acme/by-help/issues"},
		{Key: "k4", Name: "By Comments", Comments: "Open source: github.com/acme/by-comments"},
		{Key: "k5", Name: "No Owner", AboutURL: "https://github.com/sponsors/acme", HelpLink: "https://example.com"},
		{Key: "k6", Name: "Nothing"},
		{Key: "k7", Name: "First Wins", UpdateURL: "https://github.com/acme/first", AboutURL: "https://github.com/acme/second"},
	} {
		f.fake.AddProgram(p)
	}
	res, err := Read(t.Context(), f.sources(), func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"By Update": "acme/by-update", "By About": "acme/by-about", "By Help": "acme/by-help", "By Comments": "acme/by-comments",
		"No Owner": "", "Nothing": "", "First Wins": "acme/first",
	} {
		c := byName(t, res.Candidates, name)
		if want == "" {
			if c.Linked() {
				t.Errorf("%s was linked to %v", name, c.Link.Ref)
			}
			continue
		}
		if c.Link.Ref.String() != want || c.Link.Kind != LinkProgram || !c.Link.Kind.Verified() {
			t.Errorf("%s: link = %+v, want %s", name, c.Link, want)
		}
	}
}

func TestAProgramIsLinkedByTheVersionInformationOfItsFile(t *testing.T) {
	f := newScanFixture(t)
	f.fake.Info = map[string]platform.VersionInfo{}
	for name, info := range map[string]platform.VersionInfo{
		"Comment":   {Comments: "https://github.com/acme/comment"},
		"Copyright": {LegalCopyright: "(c) Acme github.com/acme/copyright"},
		"Trademark": {LegalTrademarks: "github.com/acme/trademark"},
		"Product":   {ProductName: "Product https://github.com/acme/product"},
		"Company":   {CompanyName: "github.com/acme/company"},
		"Silent":    {Comments: "nothing to see"},
	} {
		exe := touch(t, 10, f.root, name, name+".exe")
		f.fake.AddProgram(platform.Program{Key: name, Name: name, InstallLocation: filepath.Join(f.root, name), DisplayIcon: exe + ",0"})
		f.fake.Info[exe] = info
	}
	var progress [][2]int
	res, err := Read(t.Context(), f.sources(), func(done, total int) { progress = append(progress, [2]int{done, total}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Comment", "Copyright", "Trademark", "Product", "Company"} {
		if c := byName(t, res.Candidates, name); c.Link.Ref.String() != "acme/"+strings.ToLower(name) || c.Link.Kind != LinkProgram {
			t.Errorf("%s: link = %+v", name, c.Link)
		}
	}
	if byName(t, res.Candidates, "Silent").Linked() {
		t.Error("a program whose file names no repository was linked")
	}
	if len(progress) == 0 || progress[len(progress)-1] != [2]int{6, 6} {
		t.Errorf("progress = %v", progress)
	}
}

func TestReadDoesNotOfferProgramsTheLibraryHoldsOrTheListCannotBeRead(t *testing.T) {
	f := newScanFixture(t)
	f.fake.AddProgram(platform.Program{Key: "held", Name: "Held"})
	f.fake.AddProgram(platform.Program{Key: "free", Name: "Free"})
	src := f.sources()
	src.Library.Keys = map[string]bool{"held": true}
	res, err := Read(t.Context(), src, func(int, int) {})
	if err != nil || !slices.Equal(names(res.Candidates), []string{"Free"}) {
		t.Fatalf("candidates = %v, %v", names(res.Candidates), err)
	}
	f.fake.ProgramsErr = platform.ErrNotSupported
	if _, err := Read(t.Context(), src, func(int, int) {}); err == nil {
		t.Error("a scan went on without the list of installed programs")
	}
}

func TestReadStopsWhenCanceled(t *testing.T) {
	f := newScanFixture(t)
	f.fake.AddProgram(platform.Program{Key: "a", Name: "A"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Read(ctx, f.sources(), func(int, int) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v", err)
	}
}

func TestTwoProgramsLinkedToOneRepositoryLeaveOneLinked(t *testing.T) {
	f := newScanFixture(t)
	f.fake.AddProgram(platform.Program{Key: "a", Name: "Acme Tool", AboutURL: "https://github.com/acme/tool"})
	f.fake.AddProgram(platform.Program{Key: "b", Name: "Acme Tool Updater", AboutURL: "https://github.com/ACME/tool"})
	res, err := Read(t.Context(), f.sources(), func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if !byName(t, res.Candidates, "Acme Tool").Linked() || byName(t, res.Candidates, "Acme Tool Updater").Linked() {
		t.Errorf("candidates = %+v", res.Candidates)
	}
}

func TestToolRecordsLinkTheirTools(t *testing.T) {
	f := newScanFixture(t)
	bin := write(t, `{"default_path":"C:/bin","bins":{
		"C:/bin/jq.exe":{"path":"C:/bin/jq.exe","remote_name":"jq","version":"v1.7.1","url":"https://github.com/jqlang/jq","provider":"github"},
		"C:/bin/odd.exe":{"path":"C:/bin/odd.exe","version":"1.0","url":"https://example.com/odd","provider":"http"}}}`, f.root, "bin", "config.json")
	scoop := filepath.Join(f.root, "scoop")
	write(t, `{"version":"14.1.0","homepage":"https://github.com/BurntSushi/ripgrep","checkver":"github"}`, scoop, "apps", "ripgrep", "current", "manifest.json")
	write(t, `{"version":"1.2","homepage":"https://example.com","checkver":{"github":"https://github.com/acme/fd-clone"}}`, scoop, "apps", "fdclone", "current", "manifest.json")
	write(t, `{"version":"0.1","homepage":"https://example.com","url":"https://github.com/acme/dl/releases/download/v0.1/dl.zip"}`, scoop, "apps", "dl", "current", "manifest.json")
	write(t, `{"version":"1","homepage":"https://example.com"}`, scoop, "apps", "plain", "current", "manifest.json")
	write(t, `not json`, scoop, "apps", "broken", "current", "manifest.json")
	write(t, `{"version":"0.9"}`, scoop, "apps", "scoop", "current", "manifest.json")
	if err := os.MkdirAll(filepath.Join(scoop, "apps", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}

	src := f.sources()
	src.Locations = Locations{BinConfig: bin, ScoopRoots: []string{scoop, filepath.Join(f.root, "no-such-scoop")}}
	res, err := Read(t.Context(), src, func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"jq": "jqlang/jq", "ripgrep": "BurntSushi/ripgrep", "fdclone": "acme/fd-clone", "dl": "acme/dl", "odd": "", "plain": ""} {
		c := byName(t, res.Candidates, name)
		if (want == "") == c.Linked() || c.Link.Ref.String() != want && want != "" {
			t.Errorf("%s: link = %+v, want %q", name, c.Link, want)
		}
	}
	jq, rg := byName(t, res.Candidates, "jq"), byName(t, res.Candidates, "ripgrep")
	if jq.Version != "v1.7.1" || jq.Origin != OriginBin || jq.MainExe != "jq.exe" || rg.Version != "14.1.0" || rg.Origin != OriginScoop {
		t.Errorf("jq = %+v, ripgrep = %+v", jq, rg)
	}
	if len(res.Candidates) != 6 {
		t.Errorf("candidates = %v", names(res.Candidates))
	}
}

func TestMissingToolRecordsAreNotAProblem(t *testing.T) {
	if got, err := ReadBin(filepath.Join(t.TempDir(), "none.json")); err != nil || got != nil {
		t.Errorf("missing bin configuration: %v, %v", got, err)
	}
	if got, err := ReadBin(""); err != nil || got != nil {
		t.Errorf("no path: %v, %v", got, err)
	}
	if got, err := ReadScoop(t.TempDir()); err != nil || got != nil {
		t.Errorf("missing scoop: %v, %v", got, err)
	}
	broken := write(t, "{", t.TempDir(), "config.json")
	if _, err := ReadBin(broken); err == nil {
		t.Error("a broken bin configuration was read")
	}
}

func TestDefaultLocationsFollowTheEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BIN_CONFIG", filepath.Join(home, "elsewhere.json"))
	t.Setenv("SCOOP", filepath.Join(home, "my-scoop"))
	t.Setenv("SCOOP_GLOBAL", filepath.Join(home, "global-scoop"))
	got := DefaultLocations()
	if got.BinConfig != filepath.Join(home, "elsewhere.json") ||
		!slices.Equal(got.ScoopRoots, []string{filepath.Join(home, "my-scoop"), filepath.Join(home, "global-scoop")}) {
		t.Errorf("locations = %+v", got)
	}
	t.Setenv("BIN_CONFIG", "")
	t.Setenv("SCOOP", "")
	t.Setenv("SCOOP_GLOBAL", "")
	t.Setenv("ProgramData", "")
	got = DefaultLocations()
	if !slices.Equal(got.ScoopRoots, []string{filepath.Join(home, "scoop")}) || !strings.HasSuffix(got.BinConfig, "config.json") {
		t.Errorf("default locations = %+v", got)
	}
}

func TestShortcutsAndFoldersFindPortableProgramsNothingElseAccountsFor(t *testing.T) {
	f := newScanFixture(t)
	programFiles := filepath.Join(f.root, "Program Files")
	f.fake.Known = platform.Folders{System: []string{programFiles}}

	registered := touch(t, 10, programFiles, "Registered", "registered.exe")
	f.fake.AddProgram(platform.Program{Key: "reg", Name: "Registered", InstallLocation: filepath.Dir(registered), DisplayIcon: registered})
	byIcon := touch(t, 10, f.root, "Apps", "iconic", "iconic.exe")
	f.fake.AddProgram(platform.Program{Key: "icon", Name: "Iconic", DisplayIcon: byIcon})

	portable := touch(t, 10, f.root, "Tools", "cool-tool-1.4.2", "cooltool.exe")
	f.fake.Info = map[string]platform.VersionInfo{portable: {CompanyName: "Cool Co", Comments: "Home: github.com/cool/cool-tool"}}
	system := touch(t, 10, programFiles, "Unlisted", "unlisted.exe")
	uninstaller := touch(t, 10, f.root, "Tools", "cool-tool-1.4.2", "unins000.exe")
	missing := filepath.Join(f.root, "gone", "gone.exe")
	for _, target := range []struct{ name, exe string }{
		{"Cool Tool", portable},
		{"Cool Tool", portable},
		{"Registered", registered},
		{"Iconic", byIcon},
		{"Unlisted", system},
		{"Uninstall Cool Tool", uninstaller},
		{"Gone", missing},
		{"Link to a document", filepath.Join(f.root, "notes.txt")},
	} {
		if err := f.fake.CreateShortcut(platform.Shortcut{Path: filepath.Join(f.root, "Desktop", target.name+".lnk"), Target: target.exe}); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Read(t.Context(), f.sources(), func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(res.Candidates), []string{"Cool Tool", "Iconic", "Registered"}) {
		t.Fatalf("candidates = %v", names(res.Candidates))
	}
	cool := byName(t, res.Candidates, "Cool Tool")
	if cool.Origin != OriginFolder || cool.Version != "1.4.2" || cool.Publisher != "Cool Co" || cool.Folder != filepath.Dir(portable) ||
		cool.MainExe != "cooltool.exe" || cool.Link.Ref.String() != "cool/cool-tool" || cool.Link.Kind != LinkProgram {
		t.Errorf("cool tool = %+v", cool)
	}
}

func TestAFolderTheUserAddedOffersTheProgramOfEachSubFolder(t *testing.T) {
	f := newScanFixture(t)
	folder := filepath.Join(f.root, "Portable")
	touch(t, 10, folder, "named-app-2.0", "other.exe")
	touch(t, 99, folder, "named-app-2.0", "named-app.exe")
	touch(t, 50, folder, "biggest", "small.exe")
	touch(t, 500, folder, "biggest", "large.exe")
	touch(t, 10, folder, "helpers-only", "setup.exe")
	touch(t, 10, folder, "helpers-only", "crash_handler.exe")
	touch(t, 10, folder, "nested-1.0", "app", "nested.exe")
	touch(t, 10, folder, "loose-file.exe")
	f.fake.Versions = map[string]string{filepath.Join(folder, "biggest", "large.exe"): "7.1.0.0"}
	f.fake.Info = map[string]platform.VersionInfo{}

	res, err := Read(t.Context(), f.sources(folder, filepath.Join(f.root, "absent")), func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(res.Candidates), []string{"biggest", "named app", "nested"}) {
		t.Fatalf("candidates = %v", names(res.Candidates))
	}
	named, biggest := byName(t, res.Candidates, "named app"), byName(t, res.Candidates, "biggest")
	if named.MainExe != "named-app.exe" || named.Version != "2.0" || biggest.MainExe != "large.exe" || biggest.Version != "7.1.0.0" {
		t.Errorf("named = %+v, biggest = %+v", named, biggest)
	}
}

func TestAFolderOfMoreThan400SubFoldersIsReadOnlyUpToThat(t *testing.T) {
	f := newScanFixture(t)
	folder := filepath.Join(f.root, "Many")
	for i := range foldersLimit + 5 {
		touch(t, 10, folder, "tool"+strings.Repeat("x", 3)+string(rune('a'+i%26))+strings.Repeat("y", i/26), "program.exe")
	}
	res, err := Read(t.Context(), f.sources(folder), func(int, int) {})
	if err != nil || len(res.Candidates) != foldersLimit {
		t.Errorf("%d candidates, %v", len(res.Candidates), err)
	}
}

func TestFolderProgramsHaveNoVersionWhenTheFileSaysNone(t *testing.T) {
	f := newScanFixture(t)
	folder := filepath.Join(f.root, "Portable")
	touch(t, 10, folder, "plain", "plain.exe")
	touch(t, 10, folder, "zero", "zero.exe")
	f.fake.Versions = map[string]string{filepath.Join(folder, "zero", "zero.exe"): "0.0.0.0"}
	res, err := Read(t.Context(), f.sources(folder), func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Candidates {
		if c.Version != "" {
			t.Errorf("%s has version %q", c.Name, c.Version)
		}
	}
}

func TestProgramsOfTheLibraryAreNotOfferedAsPortableAgain(t *testing.T) {
	f := newScanFixture(t)
	folder := filepath.Join(f.root, "Portable")
	held := touch(t, 10, folder, "held", "held.exe")
	touch(t, 10, folder, "free", "free.exe")
	src := f.sources(folder)
	src.Library.Folders = []string{filepath.Dir(held)}
	res, err := Read(t.Context(), src, func(int, int) {})
	if err != nil || !slices.Equal(names(res.Candidates), []string{"free"}) {
		t.Errorf("candidates = %v, %v", names(res.Candidates), err)
	}
}
