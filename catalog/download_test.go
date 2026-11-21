package catalog_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

// withFiles gives a release exactly these files.
func withFiles(names ...string) func(*source.Release) {
	return func(r *source.Release) {
		r.Assets = nil
		for _, n := range names {
			r.Assets = append(r.Assets, source.Asset{Name: n, Size: 1 << 20})
		}
	}
}

func windows(arch string) selection.Policy {
	return selection.Policy{Target: selection.Target{OS: selection.OSWindows, Arch: arch}, PreferInstallers: true}
}

func offeredFile(t *testing.T, e catalog.Entry) (tag, file string) {
	t.Helper()
	offer, ok := e.LatestInstallable()
	if !ok {
		return "", ""
	}
	return offer.Release.Tag, offer.Pick().Asset.Name
}

func TestANewerReleaseWithNothingInstallableDoesNotHideTheNewestInstallableOne(t *testing.T) {
	h := openHarness(t)
	h.catalog.SetPolicy(windows(selection.ArchX64))
	h.source.publish("acme", "tool", page("",
		rel("v2.0.0", 5, withFiles("tool-linux-x64.tar.gz")),
		rel("v1.9.0", 3, withFiles("tool-x64-setup.exe")),
		rel("v1.8.0", 1, withFiles("tool-x64-setup.exe"))))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.8.0"})

	if tag, file := offeredFile(t, e); tag != "v1.9.0" || file != "tool-x64-setup.exe" {
		t.Fatalf("offered %s %s", tag, file)
	}
	newest, offered, skipped := e.SkippedOver()
	if !skipped || newest.Tag != "v2.0.0" || offered.Tag != "v1.9.0" {
		t.Errorf("skipped over %q for %q, %v", newest.Tag, offered.Tag, skipped)
	}
	// The update is to the release that can be installed.
	if !e.HasUpdate() || e.Status(false) != catalog.StatusUpdate || latestTag(t, e) != "v2.0.0" {
		t.Errorf("status %v, newest %q", e.Status(false), latestTag(t, e))
	}
	if got, _ := e.Offered(); got.Tag != "v1.9.0" {
		t.Errorf("offered release = %q", got.Tag)
	}
}

func TestAnItemWithNoDownloadForThisPCCanStillBeTracked(t *testing.T) {
	h := openHarness(t)
	h.catalog.SetPolicy(windows(selection.ArchX64))
	h.source.publish("acme", "tool", page("", rel("v1.0.0", 1, withFiles("tool-linux-x64.tar.gz", "tool-x64.deb"))))
	e := h.add("tool", catalog.AddOptions{})
	if _, ok := e.LatestInstallable(); ok || e.Status(false) != catalog.StatusNoDownload || e.UpdateAvailable() {
		t.Fatalf("status %v", e.Status(false))
	}
	if e.Item.LastFailure != catalog.FailNone {
		t.Errorf("a release with no download is not a failed check: %q", e.Item.LastFailure)
	}
	// A release that has one turns it around.
	h.source.publish("acme", "tool", page(`"e2"`, rel("v1.1.0", 2, withFiles("tool-windows-x64.zip")), rel("v1.0.0", 1)))
	h.check(e.Item.ID, true)
	if got := h.get(e.Item.ID); got.Status(false) != catalog.StatusNotInstalled {
		t.Errorf("status after a download appeared: %v", got.Status(false))
	}
}

func TestChangingThePolicyChoosesAgainForEveryItem(t *testing.T) {
	h := openHarness(t)
	h.catalog.SetPolicy(windows(selection.ArchX64))
	h.source.publish("acme", "tool", page("", rel("v1.0.0", 1, withFiles("tool-x64-setup.exe", "tool-arm64-setup.exe"))))
	e := h.add("tool", catalog.AddOptions{})
	if _, file := offeredFile(t, e); file != "tool-x64-setup.exe" {
		t.Fatalf("on x64: %s", file)
	}
	updates := 0
	h.catalog.Entries().Subscribe(func([]catalog.Entry) { updates++ })
	h.catalog.SetPolicy(windows(selection.ArchARM64))
	if _, file := offeredFile(t, h.get(e.Item.ID)); file != "tool-arm64-setup.exe" || updates != 1 {
		t.Errorf("on arm64: %s after %d updates", file, updates)
	}
	h.catalog.SetPolicy(windows(selection.ArchARM64)) // Nothing changed.
	if updates != 1 {
		t.Errorf("an unchanged policy published %d times", updates)
	}
	// New policy applies to Items loaded later.
	h.reopen()
	h.catalog.SetPolicy(windows(selection.ArchX64))
	if _, file := offeredFile(t, h.get(e.Item.ID)); file != "tool-x64-setup.exe" {
		t.Errorf("after restart: %s", file)
	}
}

func TestTheChosenFileSurvivesARestartAndBreaksWhenItDisappears(t *testing.T) {
	h := openHarness(t)
	h.catalog.SetPolicy(windows(selection.ArchX64))
	h.source.publish("acme", "tool", page(`"e1"`, rel("v1.0.0", 1, withFiles("tool-1.0-x64.msi", "tool-1.0-x64-setup.exe", "tool-1.0-win-x64-portable.zip"))))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	if _, file := offeredFile(t, e); file != "tool-1.0-x64-setup.exe" {
		t.Fatalf("best match = %s", file)
	}
	var portable selection.Candidate
	for _, c := range e.Resolve(e.Releases[0]).Candidates {
		if c.Kind == selection.ArtifactArchive {
			portable = c
		}
	}
	chosen := selection.IntentOf(portable, true)
	if err := h.catalog.SetIntent(t.Context(), e.Item.ID, &chosen); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	got := h.get(e.Item.ID)
	if got.Intent == nil || !reflect.DeepEqual(*got.Intent, chosen) {
		t.Fatalf("intent after restart = %+v, want %+v", got.Intent, chosen)
	}

	// The next release keeps the kind of file, named differently.
	h.source.publish("acme", "tool", page(`"e2"`, rel("v1.1.0", 2, withFiles("tool-1.1-x64.msi", "tool-1.1-x64-setup.exe", "tool-1.1-win-x64-portable.zip")), rel("v1.0.0", 1)))
	h.check(e.Item.ID, true)
	got = h.get(e.Item.ID)
	if tag, file := offeredFile(t, got); tag != "v1.1.0" || file != "tool-1.1-win-x64-portable.zip" || got.NeedsAttention() {
		t.Fatalf("offered %s %s, needs attention %v", tag, file, got.NeedsAttention())
	}

	// A release without it is offered the best match and surfaced, not guessed.
	h.source.publish("acme", "tool", page(`"e3"`, rel("v1.2.0", 3, withFiles("tool-1.2-x64.msi", "tool-1.2-x64-setup.exe")), rel("v1.1.0", 2), rel("v1.0.0", 1)))
	h.check(e.Item.ID, true)
	got = h.get(e.Item.ID)
	offer, _ := got.LatestInstallable()
	if !offer.Result.IntentBroken || offer.Pick().Asset.Name != "tool-1.2-x64-setup.exe" {
		t.Fatalf("offer = %+v", offer.Result)
	}
	if got.Status(false) != catalog.StatusAttention || !got.NeedsAttention() {
		t.Errorf("status = %v", got.Status(false))
	}
	// Holding updates, or choosing Automatic, ends the attention.
	if err := h.catalog.SetIntent(t.Context(), e.Item.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got = h.get(e.Item.ID); got.Intent != nil || got.Status(false) != catalog.StatusUpdate {
		t.Errorf("after Automatic: intent %v, status %v", got.Intent, got.Status(false))
	}
}

func TestChangingTheKindOrMethodForgetsTheChosenFile(t *testing.T) {
	h := openHarness(t)
	h.catalog.SetPolicy(windows(selection.ArchX64))
	h.source.publish("acme", "tool", page("", rel("v1.0.0", 1, withFiles("tool-x64.msi", "tool-win-x64.zip"))))
	e := h.add("tool", catalog.AddOptions{})
	chosen := selection.IntentOf(e.Resolve(e.Releases[0]).Candidates[0], true)

	for name, change := range map[string]func() error{
		"kind":   func() error { return h.catalog.SetKind(t.Context(), e.Item.ID, selection.ItemTool) },
		"method": func() error { return h.catalog.SetMethod(t.Context(), e.Item.ID, selection.MethodPortable) },
	} {
		if err := h.catalog.SetIntent(t.Context(), e.Item.ID, &chosen); err != nil {
			t.Fatal(err)
		}
		if err := change(); err != nil {
			t.Fatal(err)
		}
		if got := h.get(e.Item.ID); got.Intent != nil {
			t.Errorf("%s: the chosen file was kept: %+v", name, got.Intent)
		}
	}
	got := h.get(e.Item.ID)
	if got.Item.Kind != selection.ItemTool || got.Item.Method != selection.MethodPortable {
		t.Errorf("kind %q, method %q", got.Item.Kind, got.Item.Method)
	}
	if _, file := offeredFile(t, got); file != "tool-win-x64.zip" {
		t.Errorf("a portable tool takes the archive: %s", file)
	}
	h.reopen()
	if got = h.get(e.Item.ID); got.Item.Kind != selection.ItemTool || got.Item.Method != selection.MethodPortable {
		t.Errorf("after restart: kind %q, method %q", got.Item.Kind, got.Item.Method)
	}
	if err := h.catalog.SetKind(t.Context(), 99, selection.ItemApp); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown item: %v", err)
	}
}

func TestTheKindIsGuessedUnlessChosen(t *testing.T) {
	h := openHarness(t)
	h.catalog.SetPolicy(windows(selection.ArchX64))
	// The fake repository's topics say cli.
	h.source.publish("acme", "tool", page("", rel("v1.0.0", 1, withFiles("tool-x64.zip"))))
	e := h.add("tool", catalog.AddOptions{})
	if got := e.KindFor(e.Releases[0]); got != selection.ItemTool {
		t.Errorf("guessed %q", got)
	}
	h.source.publish("acme", "app", page("", rel("v1.0.0", 1, withFiles("app-x64.msi"))))
	if got := h.add("app", catalog.AddOptions{}); got.KindFor(got.Releases[0]) != selection.ItemApp {
		t.Error("an installer means an app")
	}
	h.source.publish("acme", "chosen", page("", rel("v1.0.0", 1, withFiles("chosen-x64.msi"))))
	e = h.add("chosen", catalog.AddOptions{Kind: selection.ItemTool})
	if e.KindFor(e.Releases[0]) != selection.ItemTool || e.Status(false) != catalog.StatusNoDownload {
		t.Errorf("a tool that only has an installer: status %v", e.Status(false))
	}
	if offer := e.Resolve(e.Releases[0]); !offer.OfferApp {
		t.Error("the switch to an app is offered")
	}
}

func TestAddingWithAChosenFileStoresIt(t *testing.T) {
	h := openHarness(t)
	h.catalog.SetPolicy(windows(selection.ArchX64))
	h.source.publish("acme", "tool", page("", rel("v1.0.0", 1, withFiles("tool-1.0-x64-setup.exe", "tool-1.0-win-x64.zip"))))
	preview, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: "acme", Repo: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	draft := preview.Draft(catalog.AddOptions{Kind: selection.ItemApp})
	var zip selection.Candidate
	for _, c := range draft.Resolve(draft.Releases[0]).Candidates {
		if c.Kind == selection.ArtifactArchive {
			zip = c
		}
	}
	intent := selection.IntentOf(zip, true)
	e, err := h.catalog.Add(t.Context(), preview, catalog.AddOptions{Kind: selection.ItemApp, Intent: &intent})
	if err != nil {
		t.Fatal(err)
	}
	h.reopen()
	got := h.get(e.Item.ID)
	if _, file := offeredFile(t, got); file != "tool-1.0-win-x64.zip" || got.Item.Kind != selection.ItemApp || got.Intent == nil || !got.Intent.Explicit {
		t.Errorf("file %q, kind %q, intent %+v", file, got.Item.Kind, got.Intent)
	}
}
