package catalog_test

import (
	"errors"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"
)

const gitlabSite = "gitlab:gitlab.com"

// publishOn makes a repository known on a site other than GitHub.
func (f *fakeSource) publishOn(site, owner, name string, p source.ReleasePage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := source.RepoRef{Owner: owner, Repo: name, Source: site}
	f.repos[key(ref)] = source.Repository{Source: site, Owner: owner, Name: name, Description: name + " description", Stars: 5}
	f.pages[key(ref)] = p
}

func (h *harness) addOn(site, owner, name string, opts catalog.AddOptions) catalog.Entry {
	h.t.Helper()
	p, err := h.catalog.Preview(h.t.Context(), source.RepoRef{Owner: owner, Repo: name, Source: site})
	if err != nil {
		h.t.Fatal(err)
	}
	e, err := h.catalog.Add(h.t.Context(), p, opts)
	if err != nil {
		h.t.Fatal(err)
	}
	return e
}

func TestTheSameNameOnAnotherSiteIsAnotherItem(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	h.source.publishOn(gitlabSite, "acme", "tool", page("", rel("v9", 2)))
	onGitHub := h.add("tool", catalog.AddOptions{})
	onGitLab := h.addOn(gitlabSite, "acme", "tool", catalog.AddOptions{})

	if onGitHub.Item.Source != "github" || onGitLab.Item.Source != gitlabSite {
		t.Fatalf("sources = %q and %q", onGitHub.Item.Source, onGitLab.Item.Source)
	}
	if got, ok := h.catalog.Find(source.RepoRef{Owner: "ACME", Repo: "Tool", Source: gitlabSite}); !ok || got.Item.ID != onGitLab.Item.ID {
		t.Errorf("Find on GitLab = %v, %v", got.Item.ID, ok)
	}
	p, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: "acme", Repo: "tool", Source: gitlabSite})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.catalog.Add(t.Context(), p, catalog.AddOptions{}); !errors.Is(err, catalog.ErrExists) {
		t.Errorf("adding the GitLab project twice = %v, want ErrExists", err)
	}
	if latest, _ := h.get(onGitLab.Item.ID).Latest(); latest.Tag != "v9" {
		t.Errorf("the GitLab Item follows %q, want its own releases", latest.Tag)
	}
	h.reopen()
	if got := h.get(onGitLab.Item.ID); got.Ref().Source != gitlabSite {
		t.Errorf("source after a restart = %q", got.Ref().Source)
	}
}

func TestChecksAskTheSiteOfTheItemAndNeedNoGitHubAvatar(t *testing.T) {
	h := openHarness(t)
	h.source.publishOn(gitlabSite, "acme/team", "widget", page(`"1"`, rel("v1", 1)))
	e := h.addOn(gitlabSite, "acme/team", "widget", catalog.AddOptions{})
	h.check(e.Item.ID, true)
	if last := h.source.releaseCalls[len(h.source.releaseCalls)-1]; last != "gitlab:gitlab.com|acme/team/widget" {
		t.Errorf("the check asked for %q", last)
	}
	if got := e.AvatarURL(); got != "" {
		t.Errorf("avatar = %q: only GitHub's can be worked out from the owner", got)
	}
}

func TestPreviewReleasesSpendsOneRequestAndTheNextCheckFetchesTheMetadata(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	p, err := h.catalog.PreviewReleases(t.Context(), source.RepoRef{Owner: "acme", Repo: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.source.repoCalls) != 0 {
		t.Fatalf("repository requests = %v, want none", h.source.repoCalls)
	}
	e, err := h.catalog.Add(t.Context(), p, catalog.AddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if e.Item.MetadataFetched || e.Item.Description != "" {
		t.Errorf("an Item added without metadata claims it: %+v", e.Item)
	}
	h.check(e.Item.ID, false)
	if got := h.get(e.Item.ID).Item; !got.MetadataFetched || got.Description != "tool description" {
		t.Errorf("after a check: %+v", got)
	}
}

func TestPreviewReleasesLooksUpAHostOfUnknownKindInFull(t *testing.T) {
	h := openHarness(t)
	h.source.publishOn("web:git.example.com", "acme", "tool", page("", rel("v1", 1)))
	if _, err := h.catalog.PreviewReleases(t.Context(), source.RepoRef{Owner: "acme", Repo: "tool", Source: "web:git.example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(h.source.repoCalls) != 1 {
		t.Errorf("repository requests = %v: the host's kind is found by asking for the repository", h.source.repoCalls)
	}
}

func TestChangingTheRepositoryCanMoveAnItemToAnotherSite(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	h.source.publishOn("forgejo:codeberg.org", "acme", "tool", page("", rel("v2", 2)))
	e := h.add("tool", catalog.AddOptions{})
	p, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: "acme", Repo: "tool", Source: "forgejo:codeberg.org"})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := h.catalog.ChangeRepository(t.Context(), e.Item.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Item.Source != "forgejo:codeberg.org" || moved.Item.ID != e.Item.ID {
		t.Errorf("moved = %+v", moved.Item)
	}
	h.reopen()
	if got := h.get(e.Item.ID); got.Ref().Source != "forgejo:codeberg.org" {
		t.Errorf("source after a restart = %q", got.Ref().Source)
	}
}
