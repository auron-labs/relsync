package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
)

// messages are the activity log's messages, newest first.
func messages(e *env) []string {
	entries := e.screen.activity.Entries().Get()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Message)
	}
	return out
}

func hasMessage(e *env, prefix string) bool {
	return slices.ContainsFunc(messages(e), func(m string) bool { return strings.HasPrefix(m, prefix) })
}

func TestCheckingForUpdatesFindsWhatIsNewAndSaysSo(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "A tool", release("v1.0.0", 1, "First"))
	e.source.publish("other", "Another", release("v3.0.0", 1, "Nothing new"))
	e.track("tool", catalog.AddOptions{AlreadyHave: true})
	e.track("other", catalog.AddOptions{AlreadyHave: true})
	tt := e.open()

	click(t, tt, "Check for updates")
	e.settle()
	requireText(t, tt, "Everything is up to date")
	if when, ok := e.screen.prefs.Get().lastFullCheck(); !ok || time.Since(when) > time.Minute {
		t.Errorf("last full check = %v, %v", when, ok)
	}
	if got := messages(e); len(got) < 1 || got[0] != "Checked 2 items · no updates" {
		t.Errorf("activity = %q", got)
	}

	e.source.publish("tool", "A tool", release("v1.1.0", 5, "Fixes a crash on start"), release("v1.0.0", 1, "First"))
	click(t, tt, "Check for updates")
	e.settle()
	requireText(t, tt, "1 update available", "Update available", "v1.0.0 → v1.1.0")
	if n := e.screen.updateCount.Get(); n != 1 {
		t.Errorf("the Updates badge = %d", n)
	}
	if got := messages(e); got[0] != "Checked 2 items · 1 update available" {
		t.Errorf("activity = %q", got)
	}
}

func TestAutomaticChecksAreQuiet(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.0.0", 1, ""))
	e.track("tool", catalog.AddOptions{AlreadyHave: true})
	tt := e.open()
	e.screen.checkLibrary(false)
	e.settle()
	requireNoText(t, tt, "Everything is up to date")
	if got := messages(e); len(got) != 1 {
		t.Errorf("activity = %q", got)
	}
}

func TestCheckingAnEmptyLibrary(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	click(t, tt, "Check for updates")
	requireText(t, tt, "Add something to your library first.")
	if _, started := e.screen.jobs.Status(checkAllJob); started {
		t.Error("an empty library started a check")
	}
}

func TestF5ChecksForUpdates(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.0.0", 1, ""))
	e.track("tool", catalog.AddOptions{})
	tt := e.open()
	tt.Key(0, ui.KeyF5)
	e.settle()
	requireText(t, tt, "Everything is up to date")
}

func TestTheCheckButtonShowsProgressWhileChecking(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	for _, name := range []string{"a", "b", "c"} {
		e.source.publish(name, "", release("v1", 1, ""))
		e.track(name, catalog.AddOptions{})
	}
	e.source.block = make(chan struct{})
	tt := e.open()
	click(t, tt, "Check for updates")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if status, ok := e.screen.jobs.Status(checkAllJob); ok && status.Total == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the check never reported its size")
		}
		time.Sleep(time.Millisecond)
	}
	requireText(t, tt, "Checking 0/3")
	requireNoText(t, tt, "Check for updates")
	close(e.source.block)
	e.settle()
	requireText(t, tt, "Check for updates")
}

func TestARateLimitStopsTheCheckWithOneWarning(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	for _, name := range []string{"a", "b", "c"} {
		e.source.publish(name, "", release("v1", 1, ""))
		e.track(name, catalog.AddOptions{})
	}
	prefs := e.screen.prefs.Get()
	prefs.ParallelChecks = 1
	e.screen.prefs.Set(prefs)
	reset := time.Now().Add(time.Hour)
	for _, name := range []string{"a", "b", "c"} {
		e.source.fail(name, &source.Error{Kind: source.KindRateLimited, Status: 403, ResetAt: reset})
	}
	before := e.source.releaseRequests()
	tt := e.open()
	click(t, tt, "Check for updates")
	e.settle()
	requireText(t, tt, "GitHub rate limit reached. It resets at", "Add a token in Settings to raise the limit to 5,000 per hour.")
	requireNoText(t, tt, "Everything is up to date")
	if got := e.source.releaseRequests() - before; got != 1 {
		t.Errorf("%d requests after the first was rate limited", got)
	}
	if got := messages(e); got[0] != "Update check stopped: GitHub rate limit reached." {
		t.Errorf("activity = %q", got)
	}
}

func TestFailedChecksAreCountedInTheActivityEntry(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("good", "", release("v1", 1, ""))
	e.source.publish("bad", "", release("v1", 1, ""))
	e.track("good", catalog.AddOptions{})
	e.track("bad", catalog.AddOptions{})
	e.source.fail("bad", &source.Error{Kind: source.KindUnreachable})
	tt := e.open()
	click(t, tt, "Check for updates")
	e.settle()
	if got := messages(e); got[0] != "Checked 2 items · no updates 1 item could not be checked." {
		t.Errorf("activity = %q", got)
	}
}

func TestStartupCheckFollowsTheSetting(t *testing.T) {
	for _, on := range []bool{true, false} {
		prefs := defaultPreferences()
		prefs.CheckOnStartup = on
		e := newEnv(t, prefs)
		e.source.publish("tool", "", release("v1", 1, ""))
		e.track("tool", catalog.AddOptions{})
		cancel := e.screen.scheduleStartupCheck(time.Millisecond)
		defer cancel()
		deadline := time.Now().Add(300 * time.Millisecond)
		ran := false
		for time.Now().Before(deadline) && !ran {
			_, ran = e.screen.jobs.Status(checkAllJob)
			time.Sleep(time.Millisecond)
		}
		e.settle()
		if ran != on {
			t.Errorf("setting %v: check ran = %v", on, ran)
		}
	}
}

func TestCheckNowChecksOneItemAndReportsAFailure(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1.0.0", 1, ""))
	entry := e.track("tool", catalog.AddOptions{AlreadyHave: true})
	e.source.publish("tool", "", release("v1.1.0", 4, "New"), release("v1.0.0", 1, ""))
	tt := e.open()
	e.screen.openItem(entry.Item.ID)
	requireText(t, tt, "Up to date")
	click(t, tt, "Check now")
	e.settle()
	requireText(t, tt, "Update available", "v1.1.0")

	e.source.fail("tool", &source.Error{Kind: source.KindTimedOut})
	click(t, tt, "Check now")
	e.settle()
	requireText(t, tt, "GitHub took too long to respond.")
	if !hasMessage(e, "Could not check acme/tool: GitHub took too long to respond.") {
		t.Errorf("activity = %q", messages(e))
	}
}

func TestUpdatesPageListsWhatIsNewerAndSkipRemovesIt(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v2.0.0", 6, "## Highlights\n\n- **Faster** start\n- See [the guide](https://example.test)"),
		release("v1.0.0", 1, ""))
	e.source.publish("calm", "", release("v1.0.0", 1, ""))
	e.track("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	e.track("calm", catalog.AddOptions{AlreadyHave: true})
	tt := e.open()
	click(t, tt, "Updates")
	requireText(t, tt, "tool", "v1.0.0 → v2.0.0", "acme/tool", "Released Nov 7, 2026", "Highlights\n• Faster start\n• See the guide", "Skip", "Details")
	requireNoText(t, tt, "calm", "You're all caught up")

	click(t, tt, "Skip")
	e.settle()
	requireText(t, tt, "You're all caught up", "Check for updates to look for new releases.")
	if !hasMessage(e, "Skipped v2.0.0 of acme/tool") {
		t.Errorf("activity = %q", messages(e))
	}
	if n := e.screen.updateCount.Get(); n != 0 {
		t.Errorf("the Updates badge = %d", n)
	}
}

func TestUpdatesPageEmptyStateShowsTheLastCheck(t *testing.T) {
	prefs := defaultPreferences()
	prefs.LastFullCheck = time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC).Unix()
	e := newEnv(t, prefs)
	tt := e.open()
	click(t, tt, "Updates")
	requireText(t, tt, "You're all caught up", "Last checked ")
	requireNoText(t, tt, "Check for updates to look for new releases.")
}

func TestUpdatesDetailsOpensTheDrawer(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v2.0.0", 6, "Shiny"), release("v1.0.0", 1, "Old"))
	e.track("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	tt := e.open()
	click(t, tt, "Updates")
	click(t, tt, "Details")
	requireText(t, tt, "What's new since your version", "Shiny", "Mark as installed (current version)")
}

func TestActivityPageListsEntriesAndClears(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", release("v1", 1, ""))
	entry := e.track("tool", catalog.AddOptions{})
	ctx := t.Context()
	e.screen.record(ctx, activity.Success, entry.Item.ID, "Added acme/tool")
	e.screen.record(ctx, activity.Warning, 0, "Update check stopped: GitHub rate limit reached.")
	e.screen.record(ctx, activity.Error, 0, "Could not check acme/tool: Check failed")
	tt := e.open()
	click(t, tt, "Activity")
	requireText(t, tt, "Added acme/tool", "Update check stopped: GitHub rate limit reached.", "Could not check acme/tool: Check failed", "Clear", "Details")
	requireNoText(t, tt, "No activity yet")

	click(t, tt, "Details")
	requireText(t, tt, "Check now")
	click(t, tt, "Clear")
	e.settle()
	requireText(t, tt, "No activity yet")
	if len(e.screen.activity.Entries().Get()) != 0 {
		t.Error("the log was not cleared")
	}
}
