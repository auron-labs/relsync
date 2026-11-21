package main

import (
	"slices"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"
)

func TestReleaseNotesAreCumulativeForAnItemThatIsBehind(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "A tool",
		release("v1.3.0", 9, "Third change"), release("v1.2.0", 6, "Second change"),
		release("v1.1.0", 3, "Installed release notes"), release("v1.0.0", 1, "Oldest"))
	entry := e.track("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.1.0"})
	tt := e.open()
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	requireText(t, tt, "What's new since your version", "v1.3.0 · 2026-11-10", "Third change", "v1.2.0 · 2026-11-07", "Second change")
	requireNoText(t, tt, "Installed release notes", "Oldest release")
	// The version card and the list of every version.
	requireText(t, tt, "Installed", "Latest", "Released", "Last checked", "All versions", "latest")
	if got := countText(tt, "v1.0.0"); got != 1 { // only in the version list
		t.Errorf("v1.0.0 occurs %d times", got)
	}
}

func TestReleaseNotesOfAnItemThatIsCurrent(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.1.0", 3, "Latest notes"), release("v1.0.0", 1, "Old notes"))
	entry := e.track("tool", catalog.AddOptions{})
	tt := e.open()
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	requireText(t, tt, "Release notes", "Latest notes", "Not installed")
	requireNoText(t, tt, "Old notes", "What's new since your version")
}

func TestNoNotesAreProvided(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.0.0", 1, "  "))
	entry := e.track("tool", catalog.AddOptions{})
	tt := e.open()
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	requireText(t, tt, "No release notes were provided.")
}

func TestARepositoryThatIsGoneIsExplained(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.0.0", 1, ""))
	entry := e.track("tool", catalog.AddOptions{AlreadyHave: true})
	e.source.fail("tool", &source.Error{Kind: source.KindNotFound, Status: 404, Resource: "acme/tool"})
	tt := e.open()
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	click(t, tt, "Check now")
	e.settle()
	requireText(t, tt, "acme/tool can't be found on GitHub any more. It may have moved or been removed. Your installed copy isn't affected.")
	if got, _ := e.screen.catalog.Get(entry.Item.ID); !got.Item.RepoMissing {
		t.Error("the Item is not marked missing")
	}
}

func TestNoReleasesAndNoStableReleases(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	pre := release("v1.0.0-beta1", 2, "Beta notes")
	pre.Prerelease = true
	e.source.publish("empty", "")
	e.source.publish("beta", "", pre)
	empty := e.track("empty", catalog.AddOptions{})
	beta := e.track("beta", catalog.AddOptions{})
	tt := e.open()

	e.screen.openItem(empty.Item.ID)
	tt.Frame()
	requireText(t, tt, "No releases have been published for this repository.")
	e.screen.openItem(beta.Item.ID)
	tt.Frame()
	requireText(t, tt, "No stable releases yet. Turn on pre-releases to track the newest build.")

	// Turning pre-releases on checks again at once and the newest build shows.
	before := e.source.releaseRequests()
	click(t, tt, "Include pre-releases")
	e.settle()
	requireText(t, tt, "Beta notes", "pre-release")
	requireNoText(t, tt, "No stable releases yet.")
	if e.source.releaseRequests() <= before {
		t.Error("changing the pre-release option did not check again")
	}
	if got, _ := e.screen.catalog.Get(beta.Item.ID); !got.Item.IncludePrereleases {
		t.Error("the option was not saved")
	}
}

func TestMarkAsInstalledAndSkipThisVersion(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v2.0.0", 5, "Two"), release("v1.0.0", 1, "One"))
	entry := e.track("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	tt := e.open()
	e.screen.openItem(entry.Item.ID)
	tt.Frame()

	click(t, tt, "Skip this version")
	e.settle()
	requireText(t, tt, "Skipped v2.0.0", "Stop skipping")
	requireNoText(t, tt, "Skip this version")
	click(t, tt, "Stop skipping")
	e.settle()
	requireText(t, tt, "Update available", "Skip this version")

	click(t, tt, "Mark as installed (current version)")
	e.settle()
	requireText(t, tt, "Up to date")
	requireNoText(t, tt, "Skip this version", "What's new since your version")
	got, _ := e.screen.catalog.Get(entry.Item.ID)
	if got.InstalledVersion() != "v2.0.0" {
		t.Errorf("installed = %q", got.InstalledVersion())
	}
	want := []string{"Marked acme/tool as installed (v2.0.0)", "Stopped skipping acme/tool", "Skipped v2.0.0 of acme/tool"}
	if got := messages(e); !slices.Equal(got[:3], want) {
		t.Errorf("activity = %q, want %q first", got, want)
	}
}

func TestIgnoreUpdatesOption(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v2.0.0", 5, ""), release("v1.0.0", 1, ""))
	entry := e.track("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	tt := e.open()
	tt.SetSize(windowWidth, 1400) // The drawer is taller since it lists the download.
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	click(t, tt, "Ignore updates")
	e.settle()
	requireText(t, tt, "Update ignored")
	if n := e.screen.updateCount.Get(); n != 0 {
		t.Errorf("a held Item counts as an update: %d", n)
	}
	click(t, tt, "Ignore updates")
	e.settle()
	requireText(t, tt, "Update available")
}

func TestOpenOnGitHub(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.0.0", 1, ""))
	entry := e.track("tool", catalog.AddOptions{})
	var opened []string
	e.screen.openURL = func(address string) error { opened = append(opened, address); return nil }
	tt := e.open()
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	click(t, tt, "Open on GitHub")
	if !slices.Equal(opened, []string{"https://github.com/acme/tool"}) {
		t.Errorf("opened %v", opened)
	}
}

func TestStopTrackingAsksFirstAndLeavesTheLibraryEmpty(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.0.0", 1, ""))
	entry := e.track("tool", catalog.AddOptions{AlreadyHave: true})
	tt := e.open()
	e.screen.openItem(entry.Item.ID)
	tt.Frame()

	click(t, tt, "Stop tracking")
	requireText(t, tt, "Stop tracking tool?", "Whatever is installed is not affected.")
	click(t, tt, "Cancel")
	requireNoText(t, tt, "Whatever is installed is not affected.")
	if _, ok := e.screen.catalog.Get(entry.Item.ID); !ok {
		t.Fatal("cancelling removed the item")
	}

	click(t, tt, "Stop tracking")
	click(t, tt, "Remove from library")
	e.settle()
	if _, ok := e.screen.catalog.Get(entry.Item.ID); ok {
		t.Fatal("the item is still tracked")
	}
	requireText(t, tt, "Your library is empty")
	if e.screen.drawer.open {
		t.Error("the drawer stayed open for a removed item")
	}
	if got := messages(e); got[0] != "Stopped tracking acme/tool" {
		t.Errorf("activity = %q", got)
	}
}
