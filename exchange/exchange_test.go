package exchange_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/database"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/exchange"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

var links = source.NewRegistry(nil, source.RegistryOptions{})

func parse(t *testing.T, text string) exchange.Parsed {
	t.Helper()
	parsed, err := exchange.Parse(text, links)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// refs lists the repositories of a parse as owner/repo, with the site when it is not GitHub.
func refs(p exchange.Parsed) []string {
	out := make([]string, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.Ref.String()
		if e.Ref.Source != "" {
			out[i] = e.Ref.Source + "|" + out[i]
		}
	}
	return out
}

func same(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestParseReadsThisApplicationsExportAndOlderVersionsOfIt(t *testing.T) {
	parsed := parse(t, `{"format":"release-sync-library","version":1,"exported":"2026-11-01T00:00:00Z","items":[
		{"url":"https://github.com/acme/app","name":"App","kind":"app","group":"Work","includePrereleases":true},
		{"url":"https://gitlab.com/acme/team/tool","kind":"tool"},
		{"url":"not a repository"},
		{"url":"https://codeberg.org/acme/other","kind":"surprising"}]}`)
	same(t, refs(parsed), "acme/app", "gitlab:gitlab.com|acme/team/tool", "forgejo:codeberg.org|acme/other")
	app := parsed.Entries[0]
	if app.Group != "Work" || app.Kind != selection.ItemApp || !app.Prereleases {
		t.Errorf("entry = %+v", app)
	}
	if parsed.Entries[1].Kind != selection.ItemTool || parsed.Entries[2].Kind != "" {
		t.Errorf("kinds = %q and %q: an unknown kind is detected, not copied", parsed.Entries[1].Kind, parsed.Entries[2].Kind)
	}
	if parsed.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", parsed.Skipped)
	}
}

func TestParseRefusesAFileFromANewerVersion(t *testing.T) {
	_, err := exchange.Parse(`{"format":"release-sync-library","version":99,"items":[]}`, links)
	if !errors.Is(err, exchange.ErrNewerFormat) {
		t.Errorf("err = %v, want ErrNewerFormat", err)
	}
}

func TestParseReadsTheReferenceAppsExport(t *testing.T) {
	for name, text := range map[string]string{
		"a document": `{"apps":[
			{"url":"https://github.com/a/one","name":"One","group":"Tools","includePrereleases":true},
			{"url":"https://github.com/a/two","categories":["Media","Other"],"additionalSettings":"{\"includePrereleases\":true}"},
			{"url":"https://github.com/a/three","additionalSettings":{"includePrereleases":true}},
			{"name":"no address"},
			"not an object"]}`,
		"a bare array": `[
			{"url":"https://github.com/a/one","name":"One","group":"Tools","includePrereleases":true},
			{"url":"https://github.com/a/two","categories":["Media","Other"],"additionalSettings":"{\"includePrereleases\":true}"},
			{"url":"https://github.com/a/three","additionalSettings":{"includePrereleases":true}},
			{"name":"no address"},
			"not an object",
		]`,
	} {
		t.Run(name, func(t *testing.T) {
			parsed := parse(t, text)
			same(t, refs(parsed), "a/one", "a/two", "a/three")
			if parsed.Skipped != 2 {
				t.Errorf("skipped = %d, want 2", parsed.Skipped)
			}
			if one := parsed.Entries[0]; one.Group != "Tools" || !one.Prereleases {
				t.Errorf("first = %+v", one)
			}
			if two := parsed.Entries[1]; two.Group != "Media" || !two.Prereleases {
				t.Errorf("second = %+v: the first category is the group, and nested settings count", two)
			}
			if !parsed.Entries[2].Prereleases {
				t.Error("settings given as an object are read too")
			}
		})
	}
}

func TestParseReadsBinsExportAsTools(t *testing.T) {
	parsed := parse(t, `[
		{"name":"rg","remote_name":"ripgrep","version":"14.0.0","hash":"abc","url":"https://github.com/BurntSushi/ripgrep","provider":"github","package_path":"rg.exe","pinned":false},
		{"name":"glab","remote_name":"glab","version":"1","hash":"x","url":"https://gitlab.com/gitlab-org/cli","provider":"gitlab","package_path":"glab"}
	]`)
	same(t, refs(parsed), "BurntSushi/ripgrep", "gitlab:gitlab.com|gitlab-org/cli")
	for _, e := range parsed.Entries {
		if e.Kind != selection.ItemTool {
			t.Errorf("%s kind = %q, want a Tool", e.Ref, e.Kind)
		}
	}
}

func TestParseFindsEveryLinkInAnyText(t *testing.T) {
	parsed := parse(t, "# My apps\r\n"+
		"- [Ripgrep](https://github.com/BurntSushi/ripgrep) is fast.\r\n"+
		"* <https://gitlab.com/gitlab-org/cli/-/releases>\n"+
		"1. https://codeberg.org/forgejo/runner.\r"+
		"- [ ] cli/cli\n"+
		"Compare read/write and/or other prose with git@github.com:Owner/Repo.git, please.\n"+
		"https://github.com/burntsushi/RIPGREP/issues/7 is a duplicate\n"+
		"https://git.example.com/acme/tool/src/branch/main\n"+
		"neovim/neovim,Editors,stable\n"+
		"url,name,group\n")
	same(t, refs(parsed),
		"BurntSushi/ripgrep", "gitlab:gitlab.com|gitlab-org/cli", "forgejo:codeberg.org|forgejo/runner", "cli/cli",
		"Owner/Repo", "web:git.example.com|acme/tool", "neovim/neovim")
}

func TestParseTreatsJSONThatIsNotAListAsText(t *testing.T) {
	parsed := parse(t, `{"name":"notes","homepage":"https://github.com/acme/app"}`)
	same(t, refs(parsed), "acme/app")
}

func TestParseOfNothingFindsNothing(t *testing.T) {
	for _, text := range []string{"", "   \n", "just words and/or slashes", "[]", `{"apps":[]}`} {
		if parsed := parse(t, text); len(parsed.Entries) != 0 {
			t.Errorf("Parse(%q) = %v", text, refs(parsed))
		}
	}
}

// fakeSource answers like a site: repositories are published by site, owner and name.
type fakeSource struct {
	mu       sync.Mutex
	pages    map[string]source.ReleasePage
	failures map[string]error
	// repoCalls and releaseCalls count the requests made.
	repoCalls, releaseCalls int
	// calls lists the repositories whose releases were asked for, in order.
	calls []string
}

func newFakeSource() *fakeSource {
	return &fakeSource{pages: map[string]source.ReleasePage{}, failures: map[string]error{}}
}

func key(ref source.RepoRef) string { return strings.ToLower(ref.Source + "|" + ref.String()) }

func (f *fakeSource) publish(site, owner, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages[key(source.RepoRef{Owner: owner, Repo: name, Source: site})] = source.ReleasePage{Releases: []source.Release{{
		Tag: "v2.0.0", Published: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Assets: []source.Asset{{Name: name + "-x64-setup.exe", Size: 1 << 20}},
	}}}
}

func (f *fakeSource) fail(site, owner, name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[key(source.RepoRef{Owner: owner, Repo: name, Source: site})] = err
}

func (f *fakeSource) Repository(_ context.Context, ref source.RepoRef) (source.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoCalls++
	if _, ok := f.pages[key(ref)]; !ok {
		return source.Repository{}, &source.Error{Kind: source.KindNotFound, Resource: ref.String()}
	}
	return source.Repository{Source: ref.Source, Owner: ref.Owner, Name: ref.Repo, Description: "the " + ref.Repo, Stars: 3}, nil
}

func (f *fakeSource) Releases(_ context.Context, ref source.RepoRef, _ string) (source.ReleasePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	f.calls = append(f.calls, ref.String())
	if err := f.failures[key(ref)]; err != nil {
		return source.ReleasePage{}, err
	}
	page, ok := f.pages[key(ref)]
	if !ok {
		return source.ReleasePage{}, &source.Error{Kind: source.KindNotFound, Resource: ref.String()}
	}
	return page, nil
}

func (f *fakeSource) Allowance() source.Allowance { return source.Allowance{} }

func openLibrary(t *testing.T, src source.Source) *catalog.Catalog {
	t.Helper()
	db, err := database.Open(t.Context(), filepath.Join(t.TempDir(), "app.db"), migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lib, err := catalog.Open(t.Context(), db.SQL, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func entriesOf(refs ...source.RepoRef) []exchange.Entry {
	out := make([]exchange.Entry, len(refs))
	for i, r := range refs {
		out[i] = exchange.Entry{Ref: r}
	}
	return out
}

func ref(owner, repo string) source.RepoRef { return source.RepoRef{Owner: owner, Repo: repo} }

func TestImportAddsEachRepositoryOnceAndCountsWhatItLeftOut(t *testing.T) {
	site := newFakeSource()
	for _, name := range []string{"one", "two", "three"} {
		site.publish("", "acme", name)
	}
	lib := openLibrary(t, site)
	if _, err := lib.CreateGroup(t.Context(), "Work"); err != nil {
		t.Fatal(err)
	}
	preview, _ := lib.Preview(t.Context(), ref("acme", "two"))
	if _, err := lib.Add(t.Context(), preview, catalog.AddOptions{}); err != nil {
		t.Fatal(err)
	}

	var seen []exchange.Progress
	entries := entriesOf(ref("acme", "one"), ref("ACME", "TWO"), ref("acme", "missing"), ref("acme", "three"))
	entries[0].Group, entries[3].Group, entries[3].Kind = "work", "Brand new", selection.ItemTool
	entries[0].Prereleases = true
	result := exchange.Import(t.Context(), lib, entries, exchange.Options{Metadata: true}, func(p exchange.Progress) { seen = append(seen, p) })

	if len(result.Added) != 2 || result.AlreadyTracked != 1 || len(result.Failed) != 1 || result.Stopped != nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Failed[0].Ref.Repo != "missing" {
		t.Errorf("failed = %+v", result.Failed)
	}
	if len(seen) != 4 || seen[0] != (exchange.Progress{Done: 0, Total: 4, Ref: ref("acme", "one")}) || seen[3].Done != 3 {
		t.Errorf("progress = %+v", seen)
	}
	groups := lib.Groups().Get()
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = g.Name
	}
	same(t, names, "Brand new", "Work")
	one, _ := lib.Find(ref("acme", "one"))
	three, _ := lib.Find(ref("acme", "three"))
	if groupName(groups, one.Item.GroupID) != "Work" || !one.Item.IncludePrereleases {
		t.Errorf("one = %+v: an existing group is found whatever its case", one.Item)
	}
	if groupName(groups, three.Item.GroupID) != "Brand new" || three.Item.Kind != selection.ItemTool {
		t.Errorf("three = %+v: a group the file names is made", three.Item)
	}
}

func groupName(groups []catalog.Group, id int64) string {
	for _, g := range groups {
		if g.ID == id {
			return g.Name
		}
	}
	return ""
}

func TestAChosenGroupOverridesTheFilesAndInstalledSoftwareIsRecorded(t *testing.T) {
	site := newFakeSource()
	site.publish("", "acme", "one")
	lib := openLibrary(t, site)
	chosen, err := lib.CreateGroup(t.Context(), "Chosen")
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesOf(ref("acme", "one"))
	entries[0].Group = "From the file"
	result := exchange.Import(t.Context(), lib, entries, exchange.Options{GroupID: chosen.ID, AlreadyInstalled: true}, nil)
	if len(result.Added) != 1 {
		t.Fatalf("result = %+v", result)
	}
	got, _ := lib.Find(ref("acme", "one"))
	if got.Item.GroupID != chosen.ID || len(lib.Groups().Get()) != 1 {
		t.Errorf("group = %d among %v: an explicit choice wins and creates nothing", got.Item.GroupID, lib.Groups().Get())
	}
	if got.InstalledVersion() != "v2.0.0" {
		t.Errorf("installed version = %q, want the latest recorded as installed", got.InstalledVersion())
	}
}

func TestImportFetchesMetadataOnlyWhenAskedTo(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		site := newFakeSource()
		site.publish("", "acme", "one")
		lib := openLibrary(t, site)
		exchange.Import(t.Context(), lib, entriesOf(ref("acme", "one")), exchange.Options{Metadata: metadata}, nil)
		got, _ := lib.Find(ref("acme", "one"))
		if (site.repoCalls == 1) != metadata || got.Item.MetadataFetched != metadata {
			t.Errorf("Metadata=%v: %d repository requests, MetadataFetched=%v", metadata, site.repoCalls, got.Item.MetadataFetched)
		}
	}
}

func TestImportStopsAtTheRequestLimitAndKeepsWhatItAdded(t *testing.T) {
	site := newFakeSource()
	for _, name := range []string{"one", "two", "three", "four"} {
		site.publish("", "acme", name)
	}
	reset := time.Now().Add(time.Hour)
	site.fail("", "acme", "three", &source.Error{Kind: source.KindRateLimited, ResetAt: reset})
	lib := openLibrary(t, site)
	result := exchange.Import(t.Context(), lib, entriesOf(ref("acme", "one"), ref("acme", "two"), ref("acme", "three"), ref("acme", "four")), exchange.Options{}, nil)

	var se *source.Error
	if !errors.As(result.Stopped, &se) || se.Kind != source.KindRateLimited {
		t.Fatalf("stopped = %v", result.Stopped)
	}
	if len(result.Added) != 2 || len(result.Failed) != 0 {
		t.Errorf("result = %+v", result)
	}
	same(t, site.calls, "acme/one", "acme/two", "acme/three")
	if _, ok := lib.Find(ref("acme", "two")); !ok {
		t.Error("what was added before the limit was lost")
	}
}

func TestACanceledImportStops(t *testing.T) {
	site := newFakeSource()
	site.publish("", "acme", "one")
	lib := openLibrary(t, site)
	ctx, cancel := context.WithCancel(t.Context())
	result := exchange.Import(ctx, lib, entriesOf(ref("acme", "one"), ref("acme", "two")), exchange.Options{}, func(p exchange.Progress) {
		if p.Done == 1 {
			cancel()
		}
	})
	if !errors.Is(result.Stopped, context.Canceled) || len(result.Added) != 1 {
		t.Errorf("result = %+v", result)
	}
}

func TestImportRemovesAnImportedRepositoryFromTryLater(t *testing.T) {
	site := newFakeSource()
	site.publish("", "acme", "one")
	lib := openLibrary(t, site)
	if _, err := lib.SaveForLater(t.Context(), source.Repository{Owner: "acme", Name: "one"}, "later"); err != nil {
		t.Fatal(err)
	}
	exchange.Import(t.Context(), lib, entriesOf(ref("acme", "one")), exchange.Options{}, nil)
	if saved := lib.TryLaterEntries().Get(); len(saved) != 0 {
		t.Errorf("still saved for later: %+v", saved)
	}
}

func TestImportAddsRepositoriesOfOtherSites(t *testing.T) {
	const gitlab = "gitlab:gitlab.com"
	site := newFakeSource()
	site.publish(gitlab, "acme/team", "tool")
	lib := openLibrary(t, site)
	parsed := parse(t, "https://gitlab.com/acme/team/tool/-/releases\nhttps://gitlab.com/acme/team/tool")
	result := exchange.Import(t.Context(), lib, parsed.Entries, exchange.Options{Metadata: true}, nil)
	if len(result.Added) != 1 || result.Added[0].Item.Source != gitlab || result.Added[0].Item.Owner != "acme/team" {
		t.Fatalf("result = %+v", result)
	}
}

func TestExportThenImportGivesTheSameItems(t *testing.T) {
	const gitlab = "gitlab:gitlab.com"
	site := newFakeSource()
	site.publish("", "acme", "app")
	site.publish("", "acme", "cli")
	site.publish(gitlab, "acme/team", "tool")
	from := openLibrary(t, site)
	group, err := from.CreateGroup(t.Context(), "Work")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ref  source.RepoRef
		opts catalog.AddOptions
	}{
		{ref("acme", "app"), catalog.AddOptions{GroupID: group.ID, IncludePrereleases: true, AlreadyHave: true}},
		{ref("acme", "cli"), catalog.AddOptions{Kind: selection.ItemTool}},
		{source.RepoRef{Owner: "acme/team", Repo: "tool", Source: gitlab}, catalog.AddOptions{}},
	} {
		preview, err := from.Preview(t.Context(), c.ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := from.Add(t.Context(), preview, c.opts); err != nil {
			t.Fatal(err)
		}
	}

	data, err := exchange.Export(from.Entries().Get(), from.Groups().Get(), time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Format  string           `json:"format"`
		Version int              `json:"version"`
		Items   []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Format != "release-sync-library" || doc.Version != 1 || len(doc.Items) != 3 {
		t.Fatalf("document = %s", data)
	}
	for _, forbidden := range []string{"token", "path", "v2.0.0", "installed", "setup.exe", "etag"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Errorf("the export holds %q, which is device-local:\n%s", forbidden, data)
		}
	}

	to := openLibrary(t, site)
	parsed := parse(t, string(data))
	result := exchange.Import(t.Context(), to, parsed.Entries, exchange.Options{Metadata: true}, nil)
	if len(result.Added) != 3 || result.AlreadyTracked != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v", result)
	}
	for _, want := range from.Entries().Get() {
		got, ok := to.Find(want.Ref())
		if !ok {
			t.Errorf("%s is missing after the import", want.FullName())
			continue
		}
		if got.Item.Source != want.Item.Source || got.Item.IncludePrereleases != want.Item.IncludePrereleases || got.Item.Kind != want.Item.Kind ||
			groupName(to.Groups().Get(), got.Item.GroupID) != groupName(from.Groups().Get(), want.Item.GroupID) {
			t.Errorf("%s: imported %+v, exported %+v", want.FullName(), got.Item, want.Item)
		}
		if got.InstalledVersion() != "" {
			t.Errorf("%s arrived installed (%q): installs are device-local", want.FullName(), got.InstalledVersion())
		}
	}
	// Importing the same file again adds nothing.
	again := exchange.Import(t.Context(), to, parsed.Entries, exchange.Options{}, nil)
	if len(again.Added) != 0 || again.AlreadyTracked != 3 {
		t.Errorf("a second import = %+v", again)
	}
}

func TestLinkListHasOneAddressPerItem(t *testing.T) {
	site := newFakeSource()
	site.publish("", "acme", "app")
	site.publish("forgejo:codeberg.org", "acme", "tool")
	lib := openLibrary(t, site)
	for _, r := range []source.RepoRef{ref("acme", "app"), {Owner: "acme", Repo: "tool", Source: "forgejo:codeberg.org"}} {
		preview, err := lib.Preview(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lib.Add(t.Context(), preview, catalog.AddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	want := "https://github.com/acme/app\nhttps://codeberg.org/acme/tool\n"
	if got := exchange.LinkList(lib.Entries().Get()); got != want {
		t.Errorf("LinkList = %q, want %q", got, want)
	}
	if parsed := parse(t, exchange.LinkList(lib.Entries().Get())); len(parsed.Entries) != 2 {
		t.Errorf("the link list does not read back: %v", refs(parsed))
	}
}
