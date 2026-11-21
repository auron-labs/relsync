package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/scan"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
)

// fakeSearcher answers searches by name from a table and counts them.
type fakeSearcher struct {
	mu      sync.Mutex
	results map[string][]source.Repository
	queries []string
	// limitAfter makes every search after that many fail as rate limited; 0 never does.
	limitAfter int
}

func (f *fakeSearcher) SearchRepositories(_ context.Context, name string) ([]source.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, name)
	if f.limitAfter > 0 && len(f.queries) > f.limitAfter {
		return nil, &source.Error{Kind: source.KindRateLimited, Status: 403, Resource: "search"}
	}
	return f.results[strings.ToLower(name)], nil
}

func (f *fakeSearcher) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

// scanFixture is a machine with programs on it and a Library to bring them into.
type scanFixture struct {
	*env
	fake   *platform.Fake
	search *fakeSearcher
	tt     *ui.Tester
	root   string
	// requestsBefore is the number of release requests at a point the test chose.
	requestsBefore int
}

func newScanFixture(t *testing.T) *scanFixture {
	t.Helper()
	e := newEnv(t, defaultPreferences())
	f := &scanFixture{env: e, fake: &platform.Fake{}, search: &fakeSearcher{results: map[string][]source.Repository{}}, root: t.TempDir()}
	e.screen.platform = f.fake
	e.screen.searcher = f.search
	e.screen.scanLocations = scan.Locations{}
	e.screen.searchPace = time.Millisecond
	f.tt = e.open()
	f.tt.SetSize(windowWidth, 2400)
	click(t, f.tt, "Scan PC")
	f.tt.Frame()
	return f
}

// program lists a program as installed, with its program file on disk.
func (f *scanFixture) program(key, name, publisher, version string, links ...string) platform.Program {
	f.t.Helper()
	dir := filepath.Join(f.root, "Programs", key)
	exe := filepath.Join(dir, key+".exe")
	write(f.t, exe, "program")
	p := platform.Program{Key: key, Name: name, Publisher: publisher, Version: version, InstallLocation: dir, DisplayIcon: exe, UninstallCommand: "unins"}
	if len(links) > 0 {
		p.AboutURL = links[0]
	}
	f.fake.AddProgram(p)
	return p
}

// scan presses "Scan now" and waits for it to finish.
func (f *scanFixture) scan() {
	f.t.Helper()
	click(f.t, f.tt, "Scan now")
	f.settle()
	f.tt.Frame()
}

func (f *scanFixture) rows() []scanRow { return f.screen.scanPage.results.Get().rows }

func (f *scanFixture) row(name string) scanRow {
	f.t.Helper()
	for _, r := range f.rows() {
		if r.Name == name {
			return r
		}
	}
	f.t.Fatalf("no row for %q among %d", name, len(f.rows()))
	return scanRow{}
}

func twoReleases() []source.Release {
	return []source.Release{release("v2.0.0", 5, ""), release("v1.0.0", 1, "")}
}

func TestScanListsProgramsLinkedToGitHubWithTheirStatusAndTheRestApart(t *testing.T) {
	f := newScanFixture(t)
	f.source.publishOwned("acme", "behind", "Has an update", twoReleases()...)
	f.source.publishOwned("acme", "current", "Is up to date", twoReleases()...)
	f.source.publishOwned("acme", "unreleased", "Nothing yet")
	f.source.publishOwned("acme", "held", "Already tracked", twoReleases()...)
	f.program("behind", "Behind App", "Acme", "1.0.0", "https://github.com/acme/behind")
	f.program("current", "Current App", "Acme", "2.0.0", "https://github.com/acme/current")
	f.program("unreleased", "Unreleased App", "", "0.1", "https://github.com/acme/unreleased")
	f.program("gone", "Gone App", "", "1.0", "https://github.com/acme/gone")
	f.program("sponsor", "Sponsor App", "", "1.0", "https://github.com/sponsors/acme")
	f.program("plain", "Plain App", "", "")
	f.program("vc", "Microsoft Visual C++ 2022 Redistributable (x64)", "Microsoft Corporation", "14.38")
	heldProgram := f.program("held", "Held App", "", "1.0.0", "https://github.com/acme/held")
	// The Library holds one of them already.
	f.trackOwned("acme", "held", catalog.AddOptions{AlreadyHave: true, Install: &install.Record{Method: install.MethodInstaller, UninstallKey: heldProgram.Key}})

	f.scan()
	requireText(t, f.tt,
		"Done. 3 programs linked to GitHub, 4 others.",
		"Linked to GitHub · 3", "Linked by the program",
		"Behind App", "acme/behind", "Update available: 1.0.0 → v2.0.0", "1,234 stars",
		"Current App", "Up to date (v2.0.0)",
		"Unreleased App", "No releases published",
		// A link that cannot be read, and one that is not a repository, are listed with the rest.
		"Other programs · 3", "Gone App", "The project page it points to couldn't be read", "Sponsor App", "Plain App",
		"Add 3 apps to library",
	)
	// Components are hidden until asked for; the Library's program is not offered again.
	requireNoText(t, f.tt, "Held App", "Redistributable")
	for _, name := range []string{"Behind App", "Current App", "Unreleased App"} {
		if r := f.row(name); !r.ticked() || r.Link.Kind != scan.LinkProgram || !r.Link.Kind.Verified() {
			t.Errorf("%s: ticked %v, link %+v", name, r.ticked(), r.Link)
		}
	}
	if got := f.screen.activity.Entries().Get(); len(got) != 1 || got[0].Message != "Scanned this PC: 3 programs linked to GitHub, 4 others" {
		t.Errorf("activity = %+v", got)
	}

	// Switching the filter off lists the components too.
	f.screen.scanPage.showSystem = true
	requireText(t, f.tt, "Other programs · 4", "Microsoft Visual C++ 2022 Redistributable (x64)")
}

func TestAddingTheTickedProgramsTracksThemAtTheVersionsTheyHaveAndOpensUpdates(t *testing.T) {
	f := newScanFixture(t)
	f.source.publishOwned("acme", "behind", "Has an update", twoReleases()...)
	f.source.publishOwned("acme", "ahead", "Newer than any release", twoReleases()...)
	f.source.publishOwned("acme", "unticked", "Left out", twoReleases()...)
	f.source.publishOwned("acme", "unknown", "No version", twoReleases()...)
	behind := f.program("behind", "Behind App", "", "1.0.0", "https://github.com/acme/behind")
	f.program("ahead", "Ahead App", "", "3.0.0", "https://github.com/acme/ahead")
	f.program("unticked", "Unticked App", "", "1.0.0", "https://github.com/acme/unticked")
	f.program("unknown", "Unknown App", "", "", "https://github.com/acme/unknown")
	f.scan()

	// The person unticks one and the page says how many will be added.
	click(t, f.tt, "Select Unticked App")
	f.tt.Frame()
	requireText(t, f.tt, "Add 3 apps to library",
		"Adding links each program to its GitHub page. Nothing gets reinstalled")

	f.requestsBefore = f.source.releaseRequests()
	click(t, f.tt, "Add 3 apps to library")
	f.settle()
	requireText(t, f.tt, "Added 3 apps: 1 has an update waiting")
	// Adding asks for nothing more: what the scan read is what is added.
	if got := f.source.releaseRequests(); got != f.requestsBefore {
		t.Errorf("adding made %d more release requests", got-f.requestsBefore)
	}
	if f.screen.page != "updates" {
		t.Errorf("page = %q, want the Updates page", f.screen.page)
	}

	for name, tc := range map[string]struct{ repo, installed string }{
		"behind": {"acme/behind", "v1.0.0"}, "ahead": {"acme/ahead", "v2.0.0"}, "unknown": {"acme/unknown", "v2.0.0"},
	} {
		e, ok := f.screen.catalog.Find(source.RepoRef{Owner: "acme", Repo: name})
		if !ok || e.FullName() != tc.repo || e.InstalledVersion() != tc.installed || e.Item.LinkGuessed {
			t.Errorf("%s: %+v installed %q, ok %v", name, e.Item, e.InstalledVersion(), ok)
		}
	}
	if _, ok := f.screen.catalog.Find(source.RepoRef{Owner: "acme", Repo: "unticked"}); ok {
		t.Error("an unticked program was added")
	}
	// A program of the installed list is bound to its entry, so it can be updated and removed.
	e, _ := f.screen.catalog.Find(source.RepoRef{Owner: "acme", Repo: "behind"})
	in, managed := e.Installed()
	if !managed || in.Method != install.MethodInstaller || in.UninstallKey != behind.Key || in.MainExe != "behind.exe" ||
		in.Folder != behind.InstallLocation || in.EntryVersion != "1.0.0" || !e.HasUpdate() {
		t.Errorf("install = %+v, %v", in, managed)
	}
	if !f.row("Behind App").tracked || f.row("Behind App").status() != "Already in your library" {
		t.Errorf("row = %+v", f.row("Behind App"))
	}
	got := f.screen.activity.Entries().Get()
	if len(got) < 4 || got[0].Message != "Added acme/behind from the PC scan" && !strings.HasPrefix(got[0].Message, "Added acme/") {
		t.Errorf("activity = %+v", got)
	}
}

func repoOf(owner, name string, stars int) source.Repository {
	return source.Repository{Owner: owner, Name: name, Stars: stars, Description: name + " by " + owner}
}

func TestFindOnGitHubLinksAGuessAndTicksOnlyALikelyMatch(t *testing.T) {
	f := newScanFixture(t)
	f.program("likely", "Likely App", "Acme Software", "1.0.0")
	f.program("possible", "Possible App", "Somebody Else", "1.0.0")
	f.program("nothing", "Nothing Matches", "", "1.0.0")
	f.program("tiny", "Ab", "", "1.0.0")
	f.source.publishOwned("acme", "likelyapp", "A likely match", twoReleases()...)
	f.source.publishOwned("stranger", "possibleapp", "A possible match", twoReleases()...)
	f.search.results["likely app"] = []source.Repository{repoOf("acme", "likelyapp", 80), repoOf("noise", "other", 9000)}
	f.search.results["possible app"] = []source.Repository{repoOf("stranger", "possibleapp", 80)}
	f.scan()
	requireText(t, f.tt, "None of your installed programs link straight to GitHub", "Other programs · 4")

	// Rows are in name order, so the first button belongs to "Ab".
	click(t, f.tt, "Find on GitHub")
	f.settle()
	requireText(t, f.tt, "Name too short to search for")
	f.startFind("program:likely")
	f.startFind("program:possible")
	f.startFind("program:nothing")
	f.settle()
	f.tt.Frame()

	likely, possible := f.row("Likely App"), f.row("Possible App")
	if likely.Link.Kind != scan.LinkLikely || !likely.ticked() || likely.Link.Ref.String() != "acme/likelyapp" || likely.Link.Kind.Verified() {
		t.Errorf("likely = %+v, ticked %v", likely.Link, likely.ticked())
	}
	if possible.Link.Kind != scan.LinkPossible || possible.ticked() || !possible.canSelect() {
		t.Errorf("possible = %+v, ticked %v", possible.Link, possible.ticked())
	}
	requireText(t, f.tt, "Linked to GitHub · 2", "Likely match", "Possible match", "Update available: 1.0.0 → v2.0.0",
		"No match found on GitHub", "Add 1 app to library")

	// Adding a guess marks the Link guessed, so an update asks first.
	f.selectAll(true)
	f.tt.Frame()
	click(t, f.tt, "Add 2 apps to library")
	f.settle()
	for _, name := range []string{"likelyapp", "possibleapp"} {
		e, ok := f.screen.catalog.Find(source.RepoRef{Owner: map[string]string{"likelyapp": "acme", "possibleapp": "stranger"}[name], Repo: name})
		if !ok || !e.Item.LinkGuessed {
			t.Errorf("%s: tracked %v, guessed %v", name, ok, e.Item.LinkGuessed)
		}
	}
}

func (f *scanFixture) selectAll(on bool) { f.screen.selectAllScanned(on) }

func (f *scanFixture) startFind(key string) { f.screen.startFind(key) }

func TestFindAllSearchesInPaceAndStopsWhenTheSearchAllowanceRunsOut(t *testing.T) {
	f := newScanFixture(t)
	for _, n := range []string{"alpha", "bravo", "charlie", "delta"} {
		f.program(n, strings.ToUpper(n[:1])+n[1:]+" Tool", "", "1.0")
		f.source.publishOwned("owner", n+"tool", "", twoReleases()...)
		f.search.results[n+" tool"] = []source.Repository{repoOf("owner", n+"tool", 100)}
	}
	f.search.limitAfter = 2
	f.scan()
	requireText(t, f.tt, "Find all on GitHub (~1 s)")

	click(t, f.tt, "Find all on GitHub (~1 s)")
	f.settle()
	f.tt.Frame()
	requireText(t, f.tt, "Stopped after 3 lookups: found 2 on GitHub.", "Linked to GitHub · 2", "Other programs · 2")
	if got := f.search.asked(); len(got) != 3 {
		t.Errorf("searches = %v", got)
	}
	if r := f.row("Charlie Tool"); r.Linked() || !strings.Contains(r.note, "search limit") {
		t.Errorf("the search that was refused: %+v", r)
	}
	if r := f.row("Delta Tool"); r.note != "" {
		t.Errorf("a program that was never searched for has the note %q", r.note)
	}
}

func TestFindAllLabelCountsTheTimeTheSearchesTake(t *testing.T) {
	for _, tc := range []struct {
		count int
		gap   time.Duration
		want  string
	}{
		{4, 2300 * time.Millisecond, "Find all on GitHub (~10 s)"},
		{20, 2300 * time.Millisecond, "Find all on GitHub (~46 s)"},
		{40, 2300 * time.Millisecond, "Find all on GitHub (~2 min)"},
		{40, 6600 * time.Millisecond, "Find all on GitHub (~4 min)"},
	} {
		if got := findAllLabel(tc.count, tc.gap); got != tc.want {
			t.Errorf("findAllLabel(%d, %v) = %q, want %q", tc.count, tc.gap, got, tc.want)
		}
	}
}

func TestLinkingAProgramByHandIsVerified(t *testing.T) {
	f := newScanFixture(t)
	f.program("mine", "My Tool", "", "1.5.0")
	f.source.publishOwned("me", "my-tool", "Mine", twoReleases()...)
	f.scan()

	click(t, f.tt, "Link…")
	typeInto(t, f.tt, "Repository address", "not a repository")
	click(t, f.tt, "Link it")
	f.tt.Frame()
	requireText(t, f.tt, "Link My Tool to its project page", "That doesn't look like a repository address.")

	typeInto(t, f.tt, "Repository address", "")
	f.screen.scanPage.link.text = "https://github.com/me/my-tool"
	click(t, f.tt, "Link it")
	f.settle()
	requireText(t, f.tt, "Linked by you", "me/my-tool", "Update available: 1.5.0 → v2.0.0", "Linked to GitHub · 1")
	if r := f.row("My Tool"); !r.Link.Kind.Verified() || !r.ticked() {
		t.Errorf("row = %+v", r)
	}
}

func TestALinkTheSiteDoesNotKnowIsReportedInTheRow(t *testing.T) {
	f := newScanFixture(t)
	f.program("mine", "My Tool", "", "1.5.0")
	f.scan()
	f.screen.startLink("program:mine", source.RepoRef{Owner: "me", Repo: "missing"})
	f.settle()
	f.tt.Frame()
	if r := f.row("My Tool"); r.Linked() || r.note == "" {
		t.Errorf("row = %+v", r)
	}
}

func TestTheScanChecksLinkedRepositoriesWithinTheRequestAllowance(t *testing.T) {
	f := newScanFixture(t)
	for _, n := range []string{"one", "two", "three"} {
		f.source.publishOwned("acme", n, "", twoReleases()...)
		f.program(n, n+" app", "", "1.0.0", "https://github.com/acme/"+n)
	}
	f.source.allowance = source.Allowance{Known: true, Limit: 60, Remaining: 1}
	f.scan()
	requireText(t, f.tt, "GitHub's request limit was reached, so some programs were not checked.", "Releases were not checked")
	if got := f.source.releaseRequests(); got != 0 {
		t.Errorf("%d requests were made with an allowance of one", got)
	}
	// The programs can still be added; the Library asks for what it needs then.
	if r := f.row("one app"); !r.canSelect() || r.ticked() {
		t.Errorf("row = %+v", r)
	}
}

func TestAProgramFoundInAFolderIsAddedAsAPortableAppOverItsFolder(t *testing.T) {
	f := newScanFixture(t)
	folder := filepath.Join(f.root, "Portable")
	write(t, filepath.Join(folder, "cool-tool-1.0.0", "cooltool.exe"), "program")
	f.fake.Info = map[string]platform.VersionInfo{filepath.Join(folder, "cool-tool-1.0.0", "cooltool.exe"): {Comments: "https://github.com/acme/cooltool"}}
	f.source.publishOwned("acme", "cooltool", "", twoReleases()...)
	f.screen.prefs.Update(func(p *preferences) { p.ScanFolders = []string{folder} })
	requireText(t, f.tt, folder)
	// A folder is taken off the list with its button, and put back.
	click(t, f.tt, "Stop looking in "+folder)
	f.tt.Frame()
	if len(f.screen.prefs.Get().ScanFolders) != 0 || f.tt.HasText(folder) {
		t.Errorf("folders = %v", f.screen.prefs.Get().ScanFolders)
	}
	f.screen.prefs.Update(func(p *preferences) { p.ScanFolders = []string{folder} })

	f.scan()
	requireText(t, f.tt, "cool tool", "Update available: 1.0.0 → v2.0.0")
	click(t, f.tt, "Add 1 app to library")
	f.settle()
	e, ok := f.screen.catalog.Find(source.RepoRef{Owner: "acme", Repo: "cooltool"})
	in, managed := e.Installed()
	if !ok || !managed || in.Method != install.MethodPortable || in.Folder != filepath.Join(folder, "cool-tool-1.0.0") ||
		in.MainExe != "cooltool.exe" || in.Dedicated || e.InstalledVersion() != "v1.0.0" {
		t.Errorf("install = %+v, managed %v, ok %v", in, managed, ok)
	}
	if files, err := f.screen.catalog.InstallFiles(t.Context(), e.Item.ID); err != nil || len(files) != 0 {
		t.Errorf("a folder find lists files: %v, %v", files, err)
	}
}

func TestToolsFromOtherManagersAreTrackedAtTheirVersionWithNoInstallRecord(t *testing.T) {
	f := newScanFixture(t)
	bin := filepath.Join(f.root, "bin", "config.json")
	write(t, bin, `{"bins":{"C:/bin/jq.exe":{"path":"C:/bin/jq.exe","remote_name":"jq","version":"v1.0.0","url":"https://github.com/acme/jq"}}}`)
	f.screen.scanLocations = scan.Locations{BinConfig: bin}
	f.source.publishOwned("acme", "jq", "", twoReleases()...)
	f.scan()
	requireText(t, f.tt, "jq", "Update available: v1.0.0 → v2.0.0")
	click(t, f.tt, "Add 1 app to library")
	f.settle()
	e, _ := f.screen.catalog.Find(source.RepoRef{Owner: "acme", Repo: "jq"})
	if _, managed := e.Installed(); managed || e.InstalledVersion() != "v1.0.0" || !e.HasUpdate() {
		t.Errorf("entry = %+v, install %+v", e.Item, e.Install)
	}
}

// wingetTable prints rows the way `winget list` does: padded to the columns of the header.
func wingetTable(rows ...[3]string) string {
	line := func(a, b, c string) string { return fmt.Sprintf("%-20s%-20s%-12s%s\n", a, b, c, "winget") }
	var out strings.Builder
	out.WriteString(line("Name", "Id", "Version") + strings.Repeat("-", 60) + "\n")
	for _, r := range rows {
		out.WriteString(line(r[0], r[1], r[2]))
	}
	return out.String()
}

func TestWingetPackagesAreReadOnRequestAndJoinTheLinkedPrograms(t *testing.T) {
	f := newScanFixture(t)
	const args = " --exact --source winget --accept-source-agreements --disable-interactivity"
	f.fake.WingetOutputs = map[string]string{
		"list --source winget --accept-source-agreements --disable-interactivity": wingetTable(
			[3]string{"Zip Tool", "acme.ziptool", "1.0.0"}, [3]string{"Plain Tool", "other.plain", "2.0"}, [3]string{"Held", "acme.held", "1.0"}),
		"show --id acme.ziptool" + args: "Homepage: https://github.com/acme/ziptool\n",
		"show --id other.plain" + args:  "Homepage: https://example.com\n",
		"show --id acme.held" + args:    "Homepage: https://github.com/acme/held\n",
	}
	f.source.publishOwned("acme", "ziptool", "", twoReleases()...)
	f.source.publishOwned("acme", "held", "", twoReleases()...)
	f.trackOwned("acme", "held", catalog.AddOptions{})
	f.program("plain", "Plain App", "", "")
	f.scan()
	requireNoText(t, f.tt, "Zip Tool")

	click(t, f.tt, "Also read winget packages")
	f.settle()
	requireText(t, f.tt, "winget: 1 more program linked to a project page.", "Zip Tool", "Linked to GitHub · 1", "Update available: 1.0.0 → v2.0.0")
	requireNoText(t, f.tt, "Plain Tool")

	f.fake.WingetErr = platform.ErrNotSupported
	click(t, f.tt, "Also read winget packages")
	f.settle()
	requireText(t, f.tt, "winget isn't available on this PC.")
}

func TestAScanThatCannotListTheInstalledProgramsSaysSo(t *testing.T) {
	f := newScanFixture(t)
	f.fake.ProgramsErr = platform.ErrNotSupported
	f.scan()
	requireText(t, f.tt, "Scanning for installed programs is not available on this system.")
	f.fake.ProgramsErr = errors.New("registry is locked")
	f.scan()
	requireText(t, f.tt, "The scan could not read the installed programs: list the installed programs: registry is locked")
	if got := f.screen.activity.Entries().Get(); len(got) != 1 || !strings.HasPrefix(got[0].Message, "Scan failed: ") {
		t.Errorf("activity = %+v", got)
	}
}

func TestAScanCanBeCanceledAndNeedsNoSecondScanWhileRunning(t *testing.T) {
	f := newScanFixture(t)
	f.source.publishOwned("acme", "slow", "", twoReleases()...)
	f.program("slow", "Slow App", "", "1.0", "https://github.com/acme/slow")
	f.source.block = make(chan struct{})
	click(t, f.tt, "Scan now")
	f.tt.Frame()
	requireText(t, f.tt, "Checking 1 linked program on GitHub…")
	if !hasLabel(f.tt, "Cancel") {
		t.Fatal("no Cancel while scanning")
	}
	f.screen.startScan() // A second request while one runs is ignored.
	click(t, f.tt, "Cancel")
	f.settle()
	requireText(t, f.tt, "Scan canceled.", "Releases were not checked")
	requireNoText(t, f.tt, "Checking releases…")
	close(f.source.block)
}

func TestSelectAllAndNoneTickOnlyWhatCanBeAdded(t *testing.T) {
	f := newScanFixture(t)
	f.source.publishOwned("acme", "one", "", twoReleases()...)
	f.source.publishOwned("acme", "two", "", twoReleases()...)
	f.source.publishOwned("acme", "held", "", twoReleases()...)
	f.program("one", "One App", "", "1.0", "https://github.com/acme/one")
	f.program("two", "Two App", "", "1.0", "https://github.com/acme/two")
	f.program("held", "Held App", "", "1.0", "https://github.com/acme/held")
	f.scan()
	requireText(t, f.tt, "Add 3 apps to library")

	click(t, f.tt, "Select none")
	f.tt.Frame()
	requireText(t, f.tt, "Add 0 apps to library")
	if f.tt.HasText("Add 1 app to library") {
		t.Fatal("none are ticked")
	}
	// A program the Library gains meanwhile can no longer be ticked.
	f.trackOwned("acme", "held", catalog.AddOptions{})
	f.screen.startScan()
	f.settle()
	click(t, f.tt, "Select all")
	f.tt.Frame()
	requireText(t, f.tt, "Add 2 apps to library", "Already in your library")
	if r := f.row("Held App"); r.ticked() || r.canSelect() {
		t.Errorf("a program already in the Library is selectable: %+v", r)
	}
}
