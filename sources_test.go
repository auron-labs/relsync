package main

import (
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"
)

const (
	gitlabKey   = "gitlab:gitlab.com"
	codebergKey = "forgejo:codeberg.org"
)

// publishOn makes a repository known on a site other than GitHub.
func (f *fakeSource) publishOn(site, owner, name, description string, releases ...source.Release) {
	f.publishOwned(owner, name, description, releases...)
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.ToLower(owner + "/" + name)
	repo := f.repos[key]
	repo.Source = site
	f.repos[key] = repo
}

func (e *env) trackOn(site, owner, name string) catalog.Entry {
	e.t.Helper()
	preview, err := e.screen.catalog.Preview(e.t.Context(), source.RepoRef{Owner: owner, Repo: name, Source: site})
	if err != nil {
		e.t.Fatal(err)
	}
	entry, err := e.screen.catalog.Add(e.t.Context(), preview, catalog.AddOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	return entry
}

func TestItemsFromOtherSitesShowTheirSiteAndOpenTheProjectPage(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "On GitHub", release("v1.0.0", 1, ""))
	e.source.publishOn(codebergKey, "forge", "gadget", "On Codeberg", release("v1.0.0", 1, ""))
	e.source.publishOn("gitlab:git.example.com", "team/sub", "widget", "Self-hosted", release("v1.0.0", 1, ""))
	e.track("tool", catalog.AddOptions{})
	gadget := e.trackOn(codebergKey, "forge", "gadget")
	widget := e.trackOn("gitlab:git.example.com", "team/sub", "widget")
	tt := e.open()
	requireText(t, tt, "acme/tool", "forge/gadget", "team/sub/widget", "Codeberg", "git.example.com")
	if countText(tt, "GitHub") > 1 { // The sidebar card is the only mention.
		t.Errorf("GitHub items carry a badge: %q", tt.Texts())
	}

	e.screen.openItem(gadget.Item.ID)
	requireText(t, tt, "Open project page")
	requireNoText(t, tt, "Open on GitHub")
	var opened string
	e.screen.openURL = func(address string) error { opened = address; return nil }
	click(t, tt, "Open project page")
	if opened != "https://codeberg.org/forge/gadget" {
		t.Errorf("opened %q", opened)
	}
	e.screen.openRepository(widget)
	if opened != "https://git.example.com/team/sub/widget" {
		t.Errorf("a nested GitLab project opened %q", opened)
	}

	e.screen.openItem(1)
	requireText(t, tt, "Open on GitHub")
}

func TestPastingALinkToAnotherSiteAddsItsProject(t *testing.T) {
	prefs := defaultPreferences()
	prefs.InstallAfterAdding = false
	e := newEnv(t, prefs)
	e.source.publishOn("gitlab:gitlab.com", "team/sub", "widget", "A GitLab project", release("v1.0.0", 1, ""))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "https://gitlab.com/team/sub/widget/-/releases")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Add to library", "A GitLab project")
	click(t, tt, "Add")
	e.settle()

	got, ok := e.screen.catalog.Find(source.RepoRef{Owner: "team/sub", Repo: "widget", Source: gitlabKey})
	if !ok || got.Item.Source != gitlabKey {
		t.Fatalf("tracked = %+v, %v", got.Item, ok)
	}
	if _, onGitHub := e.screen.catalog.Find(source.RepoRef{Owner: "team/sub", Repo: "widget"}); onGitHub {
		t.Error("the GitLab project was taken for a GitHub one")
	}
}

func TestAnAddressNoSiteAnswersSaysSoInsteadOfOpeningThePage(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	e.source.failures["acme/nothing"] = &source.Error{Kind: source.KindNoProject, Site: "example.org", Resource: "acme/nothing"}
	typeInto(t, tt, "Search or paste a repository link", "https://example.org/acme/nothing")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Release Sync couldn't find a project at that address.")
}

func TestTheSitesOfTrackedItemsAreRememberedAfterARestart(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publishOn("forgejo:git.example.org", "acme", "tool", "Self-hosted", release("v1.0.0", 1, ""))
	e.trackOn("forgejo:git.example.org", "acme", "tool")
	e.screen.learnSites()
	ref, ok := e.screen.sources.ParseLink("https://git.example.org/acme/other/issues")
	if !ok || ref.Source != "forgejo:git.example.org" {
		t.Errorf("ParseLink = %+v, %v: a host the Library is on is read as its kind", ref, ok)
	}
}
