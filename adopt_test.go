package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
)

// guessedItem is an Item matched to a repository by name, at an old version.
func (e *env) guessedItem(name string) catalog.Entry {
	e.t.Helper()
	e.source.publish(name, "The repository it was matched to", release("v2.0.0", 5, "New"), release("v1.0.0", 1, "Old"))
	return e.track(name, catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0", LinkGuessed: true})
}

// waitText frames until the text shows.
func waitText(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tt.Frame()
		if tt.HasText(text) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in %q", text, tt.Texts())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAGuessedLinkAsksWhetherItIsTheRightProjectUntilConfirmed(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	guessed := e.guessedItem("guessed")
	e.source.publish("verified", "", release("v1.0.0", 1, ""))
	verified := e.track("verified", catalog.AddOptions{})
	tt := e.open()

	e.screen.openItem(verified.Item.ID)
	requireNoText(t, tt, "Is this the right project?")
	e.screen.openItem(guessed.Item.ID)
	requireText(t, tt, "Is this the right project?", "Yes, it's right", "Change repository…", "Find a better match",
		"matched this program to the repository above by its name only")

	click(t, tt, "Yes, it's right")
	e.settle()
	requireNoText(t, tt, "Is this the right project?")
	if got, _ := e.screen.catalog.Get(guessed.Item.ID); got.Item.LinkGuessed {
		t.Error("the link is still guessed")
	}
	if got := e.screen.activity.Entries().Get(); len(got) != 1 || got[0].Message != "Confirmed that guessed follows acme/guessed" {
		t.Errorf("activity = %+v", got)
	}
}

func TestChangingTheRepositoryOfAGuessedLink(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	guessed := e.guessedItem("wrong")
	e.source.publish("right", "The right one", release("v1.5.0", 3, ""), release("v1.0.0", 1, ""))
	if err := e.screen.catalog.SetSkippedVersion(t.Context(), guessed.Item.ID, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	tt := e.open()
	e.screen.openItem(guessed.Item.ID)
	tt.Frame()

	click(t, tt, "Change repository…")
	requireText(t, tt, "Change the repository of wrong", "It's currently linked to acme/wrong.")
	typeInto(t, tt, "Repository address", "not a repository")
	click(t, tt, "Switch")
	requireText(t, tt, "That doesn't look like a repository address.")
	click(t, tt, "Cancel")
	click(t, tt, "Change repository…")
	typeInto(t, tt, "Repository address", "https://github.com/acme/right")
	click(t, tt, "Switch")
	e.settle()
	tt.Frame()

	got, _ := e.screen.catalog.Get(guessed.Item.ID)
	if got.FullName() != "acme/right" || got.Item.LinkGuessed || got.Item.SkippedVersion != "" || got.InstalledVersion() != "v1.0.0" {
		t.Errorf("item = %+v, installed %q", got.Item, got.InstalledVersion())
	}
	requireText(t, tt, "wrong now follows acme/right", "v1.0.0 → v1.5.0")
	requireNoText(t, tt, "Is this the right project?")

	// A repository that is not there changes nothing.
	e.screen.changeRepository(got, func(ctx context.Context) (catalog.Preview, error) {
		return e.screen.catalog.Preview(ctx, source.RepoRef{Owner: "acme", Repo: "missing"})
	})
	e.settle()
	requireText(t, tt, "Could not switch repositories")
	if again, _ := e.screen.catalog.Get(guessed.Item.ID); again.FullName() != "acme/right" {
		t.Errorf("item = %s", again.FullName())
	}
}

func TestFindingABetterMatchOffersTheRepositoriesThatFitAndSwitchesToTheOneChosen(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	guessed := e.guessedItem("wrongapp")
	e.source.publish("rightapp", "The right one", release("v1.2.0", 3, ""), release("v1.0.0", 1, ""))
	search := &fakeSearcher{results: map[string][]source.Repository{
		"wrongapp": {repoOf("acme", "wrongapp", 500), repoOf("someone", "wrongapp-fork", 40), repoOf("acme", "unrelated", 900)},
	}}
	e.screen.searcher = search
	e.screen.searchPace = time.Millisecond
	tt := e.open()
	e.screen.openItem(guessed.Item.ID)
	tt.Frame()

	click(t, tt, "Find a better match")
	waitText(t, tt, "someone/wrongapp-fork")
	requireText(t, tt, "Find a better match for wrongapp", "40 stars", "wrongapp-fork by someone")
	// The repository it follows now, and those that do not fit, are not offered.
	requireNoText(t, tt, "acme/unrelated", "500 stars")

	// Switching needs the repository's releases, which the fake has for another name.
	e.source.publishOwned("someone", "wrongapp-fork", "Fork", release("v3.0.0", 9, ""), release("v1.0.0", 1, ""))
	click(t, tt, "Use someone/wrongapp-fork for wrongapp")
	e.settle()
	got, _ := e.screen.catalog.Get(guessed.Item.ID)
	if got.FullName() != "someone/wrongapp-fork" || got.Item.LinkGuessed || got.InstalledVersion() != "v1.0.0" {
		t.Errorf("item = %+v", got.Item)
	}
}

func TestFindingABetterMatchWithNothingElseSaysSo(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	guessed := e.guessedItem("lonely")
	e.screen.searcher = &fakeSearcher{results: map[string][]source.Repository{}}
	e.screen.searchPace = time.Millisecond
	tt := e.open()
	e.screen.openItem(guessed.Item.ID)
	tt.Frame()
	click(t, tt, "Find a better match")
	waitText(t, tt, "No other match was found on GitHub.")
}

// publishApp publishes a portable app in two versions and tracks it at the old one.
func (f *installFixture) publishApp(guessed bool) catalog.Entry {
	f.t.Helper()
	f.source.publish("app", "An app",
		f.release("v2.0.0", 5, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}),
		f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	return f.track("app", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0", LinkGuessed: guessed})
}

func TestAnUpdateFromAGuessedLinkAsksFirstAndKeepsTheAnswer(t *testing.T) {
	f := newInstallFixture(t)
	entry := f.publishApp(true)
	const question = "Is acme/app the right project?"

	// Declining leaves the link guessed and the machine alone.
	if !f.screen.startInstall(entry.Item.ID, "v2.0.0", nil) {
		t.Fatal("the update did not start")
	}
	f.waitFor(question)
	requireText(t, f.tt, "matched “app” to https://github.com/acme/app by its name only", "Yes, it's the right one")
	click(t, f.tt, "Cancel")
	f.finish()
	if got := f.entry(entry); !got.Item.LinkGuessed || got.InstalledVersion() != "v1.0.0" || len(filesIn(t, f.apps)) != 0 {
		t.Errorf("item = %+v, installed %q, files %v", got.Item, got.InstalledVersion(), filesIn(t, f.apps))
	}

	// Confirming goes on, and the link is no longer a guess.
	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.answer(question, "Yes, it's the right one")
	f.finish()
	if got := f.entry(entry); got.Item.LinkGuessed || got.InstalledVersion() != "v2.0.0" {
		t.Errorf("item = %+v, installed %q", got.Item, got.InstalledVersion())
	}
	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.finish()
	requireNoText(t, f.tt, question)
}

func TestAnUpdateFromAVerifiedLinkAsksNothing(t *testing.T) {
	f := newInstallFixture(t)
	entry := f.publishApp(false)
	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.finish()
	requireNoText(t, f.tt, "the right project?")
	if got := f.entry(entry); got.InstalledVersion() != "v2.0.0" {
		t.Errorf("installed %q", got.InstalledVersion())
	}
}

func TestAddingARepositoryOfAProgramAlreadyOnThisPCOffersToUseIt(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	fake := &platform.Fake{}
	e.screen.platform = fake
	dir := filepath.Join(t.TempDir(), "Tool")
	write(t, filepath.Join(dir, "tool.exe"), "program")
	fake.AddProgram(platform.Program{
		Key: "u|tool", Scope: platform.ScopeUser, Name: "Tool", Publisher: "Acme", Version: "1.0.0",
		UninstallCommand: "unins", InstallLocation: dir, DisplayIcon: filepath.Join(dir, "tool.exe"),
	})
	fake.AddProgram(platform.Program{Key: "u|other", Name: "Something Else", UninstallCommand: "unins"})
	e.source.publish("tool", "A tool", release("v2.0.0", 5, ""), release("v1.0.0", 1, ""))
	tt := e.open()

	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Already on this PC?", "Found “Tool” 1.0.0 in Windows' installed programs.", "Locate…", "Version you have")
	if !e.screen.add.alreadyHave || e.screen.add.version != "1.0.0" {
		t.Fatalf("add dialog: already have %v, version %q", e.screen.add.alreadyHave, e.screen.add.version)
	}
	click(t, tt, "Add")
	e.settle()

	got, ok := e.screen.catalog.Find(source.RepoRef{Owner: "acme", Repo: "tool"})
	in, managed := got.Installed()
	if !ok || !managed || in.UninstallKey != "u|tool" || in.Scope != platform.ScopeUser || in.MainExe != "tool.exe" ||
		got.InstalledVersion() != "v1.0.0" || !got.HasUpdate() || got.Item.LinkGuessed {
		t.Errorf("entry = %+v, install %+v", got.Item, in)
	}
}

func TestAddingARepositoryWithNoProgramOnThisPCFindsNothingToOffer(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	fake := &platform.Fake{}
	fake.AddProgram(platform.Program{Key: "held", Name: "Tool", UninstallCommand: "unins"})
	e.screen.platform = fake
	e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	// The Library holds the only program that fits already.
	e.source.publish("other", "", release("v1.0.0", 1, ""))
	e.track("other", catalog.AddOptions{AlreadyHave: true, Install: &install.Record{Method: install.MethodInstaller, UninstallKey: "held"}})
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Already on this PC?", "I already have this")
	requireNoText(t, tt, "Found “Tool”", "Version you have")
}

func TestAProgramThePersonLocatedIsRecordedByItsFile(t *testing.T) {
	st := &addState{alreadyHave: true, located: filepath.Join("tools", "tool.exe"), found: &platform.Program{Key: "k"}}
	rec := adoptedOnPC(st, "tool")
	if rec == nil || rec.Method != install.MethodInstaller || rec.UninstallKey != "" || rec.Folder != "tools" || rec.MainExe != "tool.exe" {
		t.Errorf("located record = %+v", rec)
	}
	st.located = ""
	if rec := adoptedOnPC(st, "tool"); rec == nil || rec.UninstallKey != "k" {
		t.Errorf("found record = %+v", rec)
	}
	st.found = nil
	if rec := adoptedOnPC(st, "tool"); rec != nil {
		t.Errorf("a record with nothing to record: %+v", rec)
	}
}

func TestAToolThatAnotherCopyOnThePathShadowsSaysSoAndNamesTheFolder(t *testing.T) {
	elsewhere := filepath.Join(t.TempDir(), "scoop", "shims")
	if err := os.MkdirAll(elsewhere, 0o750); err != nil {
		t.Fatal(err)
	}
	err := os.WriteFile(filepath.Join(elsewhere, "tool.exe"), []byte("other"), 0o700) //nolint:gosec // The file must be executable to be found on the PATH.
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", elsewhere+string(os.PathListSeparator)+os.Getenv("PATH"))

	f := newInstallFixture(t)
	f.source.publish("tool", "A tool", f.release("v1.0.0", 1, "tool-win-x64.zip", map[string][]byte{"tool.exe": console("t")}))
	entry := f.track("tool", catalog.AddOptions{Kind: selection.ItemTool})
	f.show(entry)
	click(t, f.tt, "Install")
	f.finish()

	want := "tool already runs from " + elsewhere + ", which is earlier on your PATH than the tools folder. That copy runs instead of this one."
	requireText(t, f.tt, want)
	var recorded bool
	for _, a := range f.screen.activity.Entries().Get() {
		recorded = recorded || strings.HasSuffix(a.Message, want)
	}
	if !recorded {
		t.Errorf("activity = %+v", f.screen.activity.Entries().Get())
	}
	if got := filesIn(t, f.tools); !slices.Equal(got, []string{"tool.exe"}) {
		t.Errorf("tools folder = %v", got)
	}
}
