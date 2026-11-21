package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/exchange"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// textContaining reports a text of the page that has part in it.
func textContaining(tt *ui.Tester, part string) bool {
	return slices.ContainsFunc(tt.Texts(), func(text string) bool { return strings.Contains(text, part) })
}

func (e *env) openImportPage() *ui.Tester {
	e.t.Helper()
	tt := e.open()
	e.screen.navigate("import")
	tt.Frame()
	return tt
}

func TestTheExportCardSaysSoWhenTheLibraryIsEmpty(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.openImportPage()
	requireText(t, tt, "Your library is empty, so there is nothing to export.")
	for _, button := range []string{"Export library (.json)", "Export as link list (.txt)", "Copy all links to clipboard"} {
		if hasLabel(tt, button) {
			t.Errorf("an empty library offers %q", button)
		}
	}
}

func TestExportsCarryTheRepositoriesAndNothingOfThisPC(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("one", "First", release("v1.0.0", 1, ""))
	e.source.publish("two", "Second", release("v2.0.0", 1, ""))
	e.track("one", catalog.AddOptions{AlreadyHave: true})
	e.track("two", catalog.AddOptions{IncludePrereleases: true})
	tt := e.openImportPage()

	var copied string
	e.screen.copyText = func(text string) { copied = text }
	click(t, tt, "Copy all links to clipboard")
	if want := "https://github.com/acme/one\nhttps://github.com/acme/two\n"; copied != want {
		t.Errorf("clipboard = %q, want %q", copied, want)
	}

	dir := t.TempDir()
	var asked []string
	e.screen.savePath = func(opts mygo.SaveDialogOptions) (string, error) {
		asked = append(asked, opts.DefaultPath)
		return filepath.Join(dir, opts.DefaultPath), nil
	}
	click(t, tt, "Export library (.json)")
	click(t, tt, "Export as link list (.txt)")
	waitForFile(t, filepath.Join(dir, "release-sync-library.json"))
	waitForFile(t, filepath.Join(dir, "release-sync-library.txt"))
	slices.Sort(asked)
	if !slices.Equal(asked, []string{"release-sync-library.json", "release-sync-library.txt"}) {
		t.Errorf("default names = %v", asked)
	}

	data := readFile(t, filepath.Join(dir, "release-sync-library.json"))
	parsed, err := exchange.Parse(data, e.screen.sources)
	if err != nil || len(parsed.Entries) != 2 || !parsed.Entries[1].Prereleases {
		t.Errorf("the saved export reads back as %+v, %v", parsed, err)
	}
	for _, device := range []string{"v1.0.0", "installed", "token", "setup.exe"} {
		if strings.Contains(data, device) {
			t.Errorf("the export holds %q:\n%s", device, data)
		}
	}
	if got := readFile(t, filepath.Join(dir, "release-sync-library.txt")); got != copied {
		t.Errorf("link list = %q, want %q", got, copied)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not written", path)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestImportingPastedTextAddsTheNewRepositoriesAndSaysWhatItSkipped(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	for _, name := range []string{"one", "two", "three"} {
		e.source.publish(name, "The "+name, release("v1.0.0", 1, ""))
	}
	e.track("two", catalog.AddOptions{})
	tt := e.openImportPage()
	requireText(t, tt, "Import repositories", "Drop a file here (.json, .txt, .csv or .md)")
	if tt.HasText("Imported") {
		t.Fatal("a result before any import")
	}

	typeInto(t, tt, "Or paste text", "https://github.com/acme/one https://github.com/acme/two https://github.com/acme/missing https://github.com/acme/three")
	click(t, tt, "Import")
	e.settle()
	tt.Frame()

	if !textContaining(tt, "Imported 2 apps · 1 already tracked · 1 couldn't be added") {
		t.Errorf("result missing from %q", tt.Texts())
	}
	for _, name := range []string{"one", "two", "three"} {
		if _, ok := e.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: name}); !ok {
			t.Errorf("acme/%s is not in the library", name)
		}
	}
	if !hasMessage(e, "Import finished: Imported 2 apps") {
		t.Errorf("activity = %q", messages(e))
	}
}

func TestImportingAFileUsesItsGroupsUnlessAGroupIsChosen(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("one", "The one", release("v1.0.0", 1, ""))
	e.source.publish("two", "The two", release("v1.0.0", 1, ""))
	path := filepath.Join(t.TempDir(), "library.json")
	write(t, path, `{"format":"release-sync-library","version":1,"items":[
		{"url":"https://github.com/acme/one","group":"Work"},
		{"url":"https://github.com/acme/two","group":"Play"}]}`)
	tt := e.openImportPage()

	e.screen.loadImportFile(path)
	e.settle()
	requireText(t, tt, "library.json is ready to import")
	click(t, tt, "Import")
	e.settle()
	tt.Frame()

	if !textContaining(tt, "Imported 2 apps") {
		t.Errorf("result missing from %q", tt.Texts())
	}
	groups := e.screen.catalog.Groups().Get()
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = g.Name
	}
	if !slices.Equal(names, []string{"Play", "Work"}) {
		t.Errorf("groups = %v, want the ones the file names", names)
	}

	click(t, tt, "Clear")
	tt.Frame()
	requireText(t, tt, "Drop a file here (.json, .txt, .csv or .md)")
}

func TestAChosenGroupTakesEveryImportedItem(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("one", "The one", release("v1.0.0", 1, ""))
	group, err := e.screen.catalog.CreateGroup(t.Context(), "Chosen")
	if err != nil {
		t.Fatal(err)
	}
	tt := e.openImportPage()
	e.screen.importPage.text = `{"apps":[{"url":"https://github.com/acme/one","group":"Elsewhere"}]}`
	e.screen.importPage.group = groupValue(group.ID)
	click(t, tt, "Import")
	e.settle()

	got, _ := e.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "one"})
	if got.Item.GroupID != group.ID || len(e.screen.catalog.Groups().Get()) != 1 {
		t.Errorf("group = %d among %v", got.Item.GroupID, e.screen.catalog.Groups().Get())
	}
}

func TestImportTracksUpdatesOnlyForSoftwareThePersonHas(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("one", "The one", release("v1.0.0", 1, ""))
	tt := e.openImportPage()
	e.screen.importPage.text = "https://github.com/acme/one"
	click(t, tt, "Already installed on this PC: track updates only")
	tt.Frame()
	requireNoText(t, tt, "Install everything after importing")
	click(t, tt, "Import")
	e.settle()

	got, _ := e.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "one"})
	if got.InstalledVersion() != "v1.0.0" {
		t.Errorf("installed version = %q", got.InstalledVersion())
	}
}

func TestAnImportThatHitsTheRequestLimitSaysSoAndKeepsWhatItAdded(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("one", "The one", release("v1.0.0", 1, ""))
	e.source.publish("two", "The two", release("v1.0.0", 1, ""))
	e.source.fail("two", &source.Error{Kind: source.KindRateLimited})
	tt := e.openImportPage()
	e.screen.importPage.text = "https://github.com/acme/one https://github.com/acme/two"
	click(t, tt, "Import")
	e.settle()
	tt.Frame()

	if !textContaining(tt, "Imported 1 app · stopped early: GitHub rate limit reached.") {
		t.Errorf("result missing from %q", tt.Texts())
	}
	if _, ok := e.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "one"}); !ok {
		t.Error("what was added before the limit was lost")
	}
}

func TestAnImportThatFindsNothingOrIsTooNewExplainsWhy(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.openImportPage()
	for text, want := range map[string]string{
		"some words with no repository":                             "No repositories were found in that text.",
		`{"format":"release-sync-library","version":42,"items":[]}`: "That file was exported by a newer version of Release Sync. Update Release Sync to import it.",
	} {
		e.screen.importPage.text = text
		click(t, tt, "Import")
		e.settle()
		requireText(t, tt, want)
	}
}

func TestAFileThatIsNotTextIsRefused(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.openImportPage()
	path := filepath.Join(t.TempDir(), "program.exe")
	if err := os.WriteFile(path, []byte{'M', 'Z', 0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	e.screen.loadImportFile(path)
	e.settle()
	requireText(t, tt, "That file isn't a text file.")
	if _, err := readImportFile(path); !errors.Is(err, errNotText) {
		t.Errorf("readImportFile = %v", err)
	}
}

func TestImportCanInstallEverythingItAdded(t *testing.T) {
	f := newInstallFixture(t)
	for _, name := range []string{"one", "two"} {
		f.source.publish(name, "The "+name, f.release("v1.0.0", 1, name+"-win-x64.zip", map[string][]byte{name + "/" + name + ".exe": gui(name)}))
	}
	f.screen.navigate("import")
	f.tt.Frame()
	f.screen.importPage.text = "https://github.com/acme/one https://github.com/acme/two"
	click(t, f.tt, "Install everything after importing")
	click(t, f.tt, "Import")
	f.finish()

	for _, name := range []string{"one", "two"} {
		got, ok := f.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: name})
		if in, installed := got.Installed(); !ok || !installed || in.Method != install.MethodPortable {
			t.Errorf("%s after the import: %+v, %v", name, got.Install, ok)
		}
	}
}
