package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
)

// open returns a tester over e's screen, after its first frame.
func (e *env) open() *ui.Tester {
	tt := newTester(e.screen)
	tt.Frame()
	return tt
}

// track adds a repository to the Library without going through the dialog.
func (e *env) track(name string, options catalog.AddOptions) catalog.Entry {
	e.t.Helper()
	preview, err := e.screen.catalog.Preview(e.t.Context(), source.RepoRef{Owner: testOwner, Repo: name})
	if err != nil {
		e.t.Fatal(err)
	}
	entry, err := e.screen.catalog.Add(e.t.Context(), preview, options)
	if err != nil {
		e.t.Fatal(err)
	}
	return entry
}

// trackOwned is track for a repository of another owner.
func (e *env) trackOwned(owner, name string, options catalog.AddOptions) catalog.Entry {
	e.t.Helper()
	preview, err := e.screen.catalog.Preview(e.t.Context(), source.RepoRef{Owner: owner, Repo: name})
	if err != nil {
		e.t.Fatal(err)
	}
	entry, err := e.screen.catalog.Add(e.t.Context(), preview, options)
	if err != nil {
		e.t.Fatal(err)
	}
	return entry
}

// typeInto focuses the control with the accessible name label and types text.
func typeInto(t *testing.T, tt *ui.Tester, label, text string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Type(text)
	tt.Frame()
}

func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func requireText(t *testing.T, tt *ui.Tester, texts ...string) {
	t.Helper()
	tt.Frame()
	for _, text := range texts {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
}

func requireNoText(t *testing.T, tt *ui.Tester, texts ...string) {
	t.Helper()
	tt.Frame()
	for _, text := range texts {
		if tt.HasText(text) {
			t.Errorf("unexpected %q in %q", text, tt.Texts())
		}
	}
}

func TestTheTopBoxButtonSaysAddForARepositoryAndSearchOtherwise(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	// A button's label occurs twice: once as its name, once as its text.
	if countText(tt, "Search") == 0 || countText(tt, "Add") != 0 {
		t.Fatalf("empty box: %q", tt.Texts())
	}
	typeInto(t, tt, "Search or paste a repository link", "https://github.com/acme/tool/releases")
	if countText(tt, "Add") == 0 || countText(tt, "Search") != 0 {
		t.Fatalf("repository link: %q", tt.Texts())
	}
	typeInto(t, tt, "Search or paste a repository link", " and more words")
	if countText(tt, "Search") == 0 || countText(tt, "Add") != 0 {
		t.Fatalf("text with spaces: %q", tt.Texts())
	}
}

func TestAddingARepositoryFromTheTopBox(t *testing.T) {
	prefs := defaultPreferences()
	prefs.InstallAfterAdding = false // These releases carry setup programs, which would now be installed.
	e := newEnv(t, prefs)
	e.source.publish("tool", "A handy tool", release("v1.1.0", 5, "Fixes"), release("v1.0.0", 1, "First"))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "git@github.com:acme/tool.git")
	click(t, tt, "Add")
	e.settle()

	// The preview shows what will be tracked, and nothing is tracked yet.
	requireText(t, tt, "Add to library", "tool", "acme", "A handy tool", "1,234 stars", "Latest version: v1.1.0",
		"Include pre-releases", "I already have this")
	if n := len(e.screen.catalog.Entries().Get()); n != 0 {
		t.Fatalf("%d items tracked before confirming", n)
	}
	if e.screen.omnibox != "" {
		t.Errorf("the top box still holds %q", e.screen.omnibox)
	}

	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "tool", "acme/tool", "A handy tool", "v1.1.0 available", "Not installed", "Added tool to your library")
	requireNoText(t, tt, "Your library is empty", "Add to library")
	entries := e.screen.catalog.Entries().Get()
	if len(entries) != 1 || entries[0].InstalledVersion() != "" {
		t.Fatalf("entries = %+v", entries)
	}
	activity := e.screen.activity.Entries().Get()
	if len(activity) != 1 || activity[0].Message != "Added acme/tool" || activity[0].ItemID != entries[0].Item.ID {
		t.Errorf("activity = %+v", activity)
	}
}

func TestIAlreadyHaveThisWithAVersion(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v2.0.0", 5, ""), release("v1.0.0", 1, ""))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireNoText(t, tt, "Version you have")
	click(t, tt, "I already have this")
	requireText(t, tt, "Version you have")
	typeInto(t, tt, "Version you have", "v1.0.0")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "v1.0.0 → v2.0.0", "Update available")
}

func TestIncludePreReleasesStartsFromTheSetting(t *testing.T) {
	prefs := defaultPreferences()
	prefs.DefaultIncludePrereleases = true
	e := newEnv(t, prefs)
	pre := release("v2.0.0-rc1", 5, "")
	pre.Prerelease = true
	e.source.publish("tool", "", pre, release("v1.0.0", 1, ""))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Latest version: v2.0.0-rc1")
	// Unticking it changes what the preview calls latest.
	click(t, tt, "Include pre-releases")
	requireText(t, tt, "Latest version: v1.0.0")
}

func TestAddingARepositoryWithNoReleases(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("empty", "Nothing yet")
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/empty")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "No releases yet")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "empty", "No releases")
}

func TestAddingAMissingRepositoryShowsWhyAndTracksNothing(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/missing")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "acme/missing wasn't found. If it's private, add a token with access in Settings.", "Close")
	requireNoText(t, tt, "Latest version")
	click(t, tt, "Close")
	requireNoText(t, tt, "Add to library")
	if len(e.screen.catalog.Entries().Get()) != 0 {
		t.Error("a missing repository was tracked")
	}
}

func TestAddingATrackedRepositorySelectsTheExistingItem(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "A handy tool", release("v1.0.0", 1, "First release notes"))
	tracked := e.track("tool", catalog.AddOptions{})
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "ACME/Tool")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "tool is already in your library", "First release notes", "Check now")
	requireNoText(t, tt, "Add to library")
	if n := len(e.screen.catalog.Entries().Get()); n != 1 || !e.screen.drawer.open {
		t.Errorf("%d items, drawer open %v", n, e.screen.drawer.open)
	}
	_ = tracked
}

func TestOtherTextInTheTopBox(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	var opened []string
	e.screen.openURL = func(address string) error { opened = append(opened, address); return nil }
	tt := e.open()

	// Search arrives with Discover: free text does nothing.
	typeInto(t, tt, "Search or paste a repository link", "a text editor")
	click(t, tt, "Search")
	if len(opened) != 0 || tt.HasText("Add to library") {
		t.Fatalf("free text did something: opened %v", opened)
	}
	// A web address that is not a repository opens in the browser.
	e.screen.omnibox = "https://github.com/orgs/acme/people"
	tt.Frame()
	click(t, tt, "Search")
	if !slices.Equal(opened, []string{"https://github.com/orgs/acme/people"}) {
		t.Errorf("opened %v", opened)
	}
}

func TestPressingEnterInTheTopBoxAdds(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "A handy tool", release("v1.0.0", 1, ""))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	tt.Key(0, ui.KeyEnter)
	e.settle()
	requireText(t, tt, "Add to library", "Latest version: v1.0.0")
}

func TestAFailedAddIsRecordedAndShown(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.0.0", 1, ""))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	// The database goes away between the preview and confirming.
	if err := e.db.Close(); err != nil {
		t.Fatal(err)
	}
	click(t, tt, "Add")
	e.settle()
	tt.Frame()
	shown := false
	for _, text := range tt.Texts() {
		shown = shown || strings.HasPrefix(text, "Could not add acme/tool: ")
	}
	if !shown {
		t.Errorf("failure not shown: %q", tt.Texts())
	}
	if n := len(e.screen.catalog.Entries().Get()); n != 0 {
		t.Errorf("%d items tracked", n)
	}
}
