package catalog_test

import (
	"errors"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

func TestAdoptingSoftwareRecordsItsInstallAndTheGuessedLinkAcrossARestart(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v2.0.0", 2), rel("v1.0.0", 1)))
	rec := install.Record{
		Method: install.MethodInstaller, Folder: `C:\Apps\Tool`, MainExe: "tool.exe", UninstallKey: "CurrentUser|64|Tool",
		Scope: platform.ScopeUser, EntryVersion: "1.0.0",
	}
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0", Install: &rec, LinkGuessed: true})
	check := func(when string) {
		t.Helper()
		got := h.get(e.Item.ID)
		in, ok := got.Installed()
		if !ok || in.Method != install.MethodInstaller || in.UninstallKey != "CurrentUser|64|Tool" || in.Folder != `C:\Apps\Tool` ||
			in.MainExe != "tool.exe" || in.Scope != platform.ScopeUser || in.EntryVersion != "1.0.0" || in.Tag != "v1.0.0" {
			t.Errorf("%s: install = %+v, %v", when, in, ok)
		}
		if !got.Item.LinkGuessed || !got.HasUpdate() {
			t.Errorf("%s: guessed %v, update %v", when, got.Item.LinkGuessed, got.HasUpdate())
		}
	}
	check("added")
	h.reopen()
	check("reopened")

	if err := h.catalog.SetLinkVerified(t.Context(), e.Item.ID); err != nil {
		t.Fatal(err)
	}
	if h.get(e.Item.ID).Item.LinkGuessed {
		t.Error("the link is still guessed after it was confirmed")
	}
}

func TestAdoptingWithoutAnInstallRecordOnlySaysWhichVersionTheUserHas(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1.0.0", 1)))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true})
	if _, managed := e.Installed(); managed || e.InstalledVersion() != "v1.0.0" || e.Item.LinkGuessed {
		t.Errorf("entry = %+v", e)
	}
}

func TestChangingTheRepositoryKeepsTheInstalledVersionAndDropsWhatBelongedToTheOldOne(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "wrong", page(`"w"`, rel("v3.0.0", 3), rel("v1.0.0", 1)))
	h.source.publish("acme", "right", page(`"r"`, rel("v2.0.0", 2), rel("v1.0.0", 1)))
	e := h.add("wrong", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0", LinkGuessed: true, Kind: selection.ItemApp})
	ctx := t.Context()
	if err := h.catalog.SetSkippedVersion(ctx, e.Item.ID, "v3.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.SetIntent(ctx, e.Item.ID, &selection.Intent{Product: "wrong", Kind: selection.ArtifactSetupExe, Explicit: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.SetHoldUpdates(ctx, e.Item.ID, true); err != nil {
		t.Fatal(err)
	}

	p, err := h.catalog.Preview(ctx, source.RepoRef{Owner: "acme", Repo: "right"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := h.catalog.ChangeRepository(ctx, e.Item.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string, got catalog.Entry) {
		t.Helper()
		if got.Item.ID != e.Item.ID || got.FullName() != "acme/right" || got.Item.LinkGuessed || got.Item.SkippedVersion != "" || got.Intent != nil {
			t.Errorf("%s: item = %+v, intent %+v", when, got.Item, got.Intent)
		}
		if got.InstalledVersion() != "v1.0.0" || !got.Item.HoldUpdates || got.Item.Kind != selection.ItemApp {
			t.Errorf("%s: installed %q, hold %v, kind %q", when, got.InstalledVersion(), got.Item.HoldUpdates, got.Item.Kind)
		}
		if tag := latestTag(t, got); tag != "v2.0.0" || len(got.Releases) != 2 {
			t.Errorf("%s: latest %q of %d cached releases", when, tag, len(got.Releases))
		}
	}
	check("changed", changed)
	h.reopen()
	check("reopened", h.get(e.Item.ID))
}

func TestChangingToARepositoryAnotherItemTracksIsRefused(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "one", page("", rel("v1.0.0", 1)))
	h.source.publish("acme", "two", page("", rel("v1.0.0", 1)))
	one := h.add("one", catalog.AddOptions{})
	two := h.add("two", catalog.AddOptions{})
	p, err := h.catalog.Preview(t.Context(), two.Ref())
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.catalog.ChangeRepository(t.Context(), one.Item.ID, p)
	if !errors.Is(err, catalog.ErrExists) || other.Item.ID != two.Item.ID {
		t.Fatalf("other = %+v, err = %v", other.Item, err)
	}
	if h.get(one.Item.ID).FullName() != "acme/one" {
		t.Error("the item was changed")
	}
	if _, err := h.catalog.ChangeRepository(t.Context(), 999, p); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("missing item: %v", err)
	}
}

func TestTagForVersion(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v2.1.0", 3), rel("v2.0.0", 2), rel("v1.0.0", 1)))
	e := h.add("tool", catalog.AddOptions{})
	for _, tc := range []struct{ name, seen, want string }{
		{"unknown version takes the latest", "", "v2.1.0"},
		{"a version with no numbers is unknown", "latest", "v2.1.0"},
		{"a version that is a release", "2.0.0", "v2.0.0"},
		{"a four-part version of a release", "2.0.0.0", "v2.0.0"},
		{"a version ahead of the latest takes the latest", "3.0.0", "v2.1.0"},
		{"the latest itself", "v2.1.0", "v2.1.0"},
		{"a version older than every cached release keeps the update visible", "0.9.1", "0.9.1"},
	} {
		if got := e.TagForVersion(tc.seen); got != tc.want {
			t.Errorf("%s: TagForVersion(%q) = %q, want %q", tc.name, tc.seen, got, tc.want)
		}
	}
	h.source.publish("acme", "none", page(""))
	if got := h.add("none", catalog.AddOptions{}).TagForVersion("1.0"); got != "" {
		t.Errorf("no releases: %q", got)
	}
}
