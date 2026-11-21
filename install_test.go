package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/install/installtest"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
)

// installFixture is a screen whose releases are served from a local host, with a
// fake platform and folders of its own under a temporary directory.
type installFixture struct {
	*env
	host *releaseHost
	fake *platform.Fake
	// apps, tools and menu are the portable apps folder, the tools folder and
	// the Start menu folder.
	apps, tools, menu string
	tt                *ui.Tester

	mu            sync.Mutex
	shown, opened []string
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	root := t.TempDir()
	prefs := defaultPreferences()
	prefs.DownloadFolder = filepath.Join(root, "downloads")
	prefs.PortableFolder = filepath.Join(root, "apps")
	prefs.ToolsFolder = filepath.Join(root, "tools")
	// The tests of installing are about checksums and signatures; those of the
	// antivirus scan and build records turn their checks on for themselves.
	prefs.ScanDownloads, prefs.CheckProvenance = false, false
	e := newEnv(t, prefs)
	f := &installFixture{
		env: e, host: newReleaseHost(t), apps: prefs.PortableFolder, tools: prefs.ToolsFolder, menu: filepath.Join(root, "menu"),
		fake: &platform.Fake{Result: platform.Signature{State: platform.SignatureNotChecked}},
	}
	f.fake.Known = platform.Folders{StartMenu: f.menu}
	e.screen.fetcher = fetch.NewClient(fetch.Options{
		StallTimeout: 5 * time.Second, Backoff: func(int) time.Duration { return time.Millisecond }, ProgressInterval: time.Nanosecond,
	})
	e.screen.platform = f.fake
	e.screen.installTiming = install.Timing{Interval: time.Millisecond, Appear: 20 * time.Millisecond, Vanish: 20 * time.Millisecond}
	e.screen.showFile = func(path string) {
		f.mu.Lock()
		f.shown = append(f.shown, path)
		f.mu.Unlock()
	}
	e.screen.reveal = func(path string) error {
		f.mu.Lock()
		f.opened = append(f.opened, path)
		f.mu.Unlock()
		return nil
	}
	f.tt = e.open()
	f.tt.SetSize(windowWidth, 2400)
	return f
}

func (f *installFixture) revealed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.shown...)
}

// release is a release whose one file, called asset, is a zip of files.
func (f *installFixture) release(tag string, daysAfter int, asset string, files map[string][]byte) source.Release {
	f.t.Helper()
	body, err := installtest.Zip(files)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.releaseOf(tag, daysAfter, asset, body)
}

// releaseOf is a release whose one file, called asset, has the given bytes.
func (f *installFixture) releaseOf(tag string, daysAfter int, asset string, body []byte) source.Release {
	return source.Release{
		Tag: tag, Name: tag, Published: releaseDay.AddDate(0, 0, daysAfter), Assets: []source.Asset{f.host.add(asset, body)},
	}
}

// refresh checks an Item again, as the check button does, so it sees what was published.
func (f *installFixture) refresh(id int64) catalog.Entry {
	f.t.Helper()
	if _, err := f.screen.catalog.Check(f.t.Context(), id, true); err != nil {
		f.t.Fatal(err)
	}
	e, _ := f.screen.catalog.Get(id)
	return e
}

// show opens an Item's drawer.
func (f *installFixture) show(e catalog.Entry) {
	f.t.Helper()
	f.screen.openItem(e.Item.ID)
	f.tt.Frame()
}

// waitFor frames until the text shows.
func (f *installFixture) waitFor(text string) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.tt.Frame()
		if f.tt.HasText(text) {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %q in %q", text, f.tt.Texts())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// answer waits for a question and clicks one of its buttons.
func (f *installFixture) answer(question, button string) {
	f.t.Helper()
	f.waitFor(question)
	click(f.t, f.tt, button)
	f.tt.Frame()
}

// finish waits until no operation is running, showing frames so questions
// can open and close.
func (f *installFixture) finish() {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.tt.Frame()
		// The job subscriber writes inflight under the environment's lock, not the fixture's.
		f.env.mu.Lock()
		running := len(f.inflight)
		f.env.mu.Unlock()
		if running == 0 && len(f.screen.operations.Get()) == 0 {
			f.tt.Frame()
			return
		}
		if time.Now().After(deadline) {
			f.env.mu.Lock()
			defer f.env.mu.Unlock()
			f.t.Fatalf("still running: %v, operations %+v, texts %q", f.inflight, f.screen.operations.Get(), f.tt.Texts())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// install installs the Item's latest release, as the Install button does.
func (f *installFixture) install(e catalog.Entry) {
	f.t.Helper()
	latest, ok := f.refresh(e.Item.ID).LatestInstallable()
	if !ok {
		f.t.Fatal("nothing to install")
	}
	if !f.screen.startInstall(e.Item.ID, latest.Release.Tag, nil) {
		f.t.Fatal("the install did not start")
	}
	f.finish()
}

func (f *installFixture) entry(e catalog.Entry) catalog.Entry {
	got, ok := f.screen.catalog.Get(e.Item.ID)
	if !ok {
		f.t.Fatalf("%s is no longer tracked", e.FullName())
	}
	return got
}

func (f *installFixture) lastActivity() activity.Entry { return f.screen.activity.Entries().Get()[0] }

// files lists what is below dir, relative and slash-separated.
func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hasLabel reports a control or text that is exactly text, which HasText does
// not: it matches parts of texts.
func hasLabel(tt *ui.Tester, text string) bool {
	_, ok := tt.Find(text)
	return ok
}

func gui(extra string) []byte     { return installtest.GUIExe(extra) }
func console(extra string) []byte { return installtest.ConsoleExe(extra) }

func TestInstallingAPortableAppPutsItInItsFolderWithAShortcutAndALaunchButton(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "An app", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{
		"app-1.0/app.exe": gui("v1"), "app-1.0/data/a.dat": []byte("a"),
	}))
	entry := f.track("app", catalog.AddOptions{})
	f.show(entry)
	requireText(t, f.tt, "Install")

	click(t, f.tt, "Install")
	f.finish()

	folder := filepath.Join(f.apps, "app")
	if got := filesIn(t, folder); !slices.Equal(got, []string{"app.exe", "data/a.dat"}) {
		t.Fatalf("app folder = %v", got)
	}
	link := filepath.Join(f.menu, appName, "app.lnk")
	if shortcut, ok := f.fake.Shortcuts()[link]; !ok || shortcut.Target != filepath.Join(folder, "app.exe") {
		t.Errorf("shortcuts = %+v", f.fake.Shortcuts())
	}
	got := f.entry(entry)
	in, ok := got.Installed()
	if !ok || in.Tag != "v1.0.0" || in.Method != install.MethodPortable || in.Folder != folder || !in.Dedicated || in.MainExe != "app.exe" || in.Shortcut != link {
		t.Errorf("install = %+v, %v", in, ok)
	}
	if files, err := f.screen.catalog.InstallFiles(t.Context(), entry.Item.ID); err != nil || len(files) != 2 {
		t.Errorf("recorded files = %+v, %v", files, err)
	}
	if got.Item.Kind != selection.ItemApp || got.Intent == nil || got.Intent.Explicit {
		t.Errorf("kind %q, intent %+v: the file installed should be kept as the choice, not as one the person made", got.Item.Kind, got.Intent)
	}
	if last := f.lastActivity(); last.Level != activity.Success || last.Message != "Installed acme/app v1.0.0" {
		t.Errorf("activity = %+v", last)
	}
	requireText(t, f.tt, "app installed: v1.0.0", "Launch", "See file location", "Uninstall from this PC", "Installed in", folder, "Up to date")
	if hasLabel(f.tt, "Install") {
		t.Error("the Install button is still offered")
	}

	click(t, f.tt, "Launch")
	f.finish()
	if got := f.fake.Launched(); !slices.Equal(got, []string{filepath.Join(folder, "app.exe")}) {
		t.Errorf("launched = %v", got)
	}
	click(t, f.tt, "See file location")
	f.finish()
	if got := f.revealed(); !slices.Equal(got, []string{filepath.Join(folder, "app.exe")}) {
		t.Errorf("shown = %v", got)
	}
}

func TestAPortableAppWithNoProgramOffersOpenFolder(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "An app", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"readme.txt": []byte("hi"), "data.bin": []byte("d")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	f.show(entry)
	requireText(t, f.tt, "Open folder")
	if hasLabel(f.tt, "Launch") {
		t.Error("an app with no program has a Launch button")
	}
	click(t, f.tt, "Open folder")
	f.finish()
	f.mu.Lock()
	opened := slices.Clone(f.opened)
	f.mu.Unlock()
	if !slices.Equal(opened, []string{filepath.Join(f.apps, "app")}) {
		t.Errorf("opened = %v", opened)
	}
}

func TestInstallingAToolPutsItsCommandOnThePathAndTellsYou(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("tool", "A tool", f.release("v1.0.0", 1, "tool-v1.0.0-windows-x64.zip", map[string][]byte{
		"tool-v1.0.0-windows-x64/tool.exe": console("t"), "tool-v1.0.0-windows-x64/README.md": []byte("docs"),
	}))
	entry := f.track("tool", catalog.AddOptions{Kind: selection.ItemTool})
	f.show(entry)

	click(t, f.tt, "Install")
	f.finish()

	if got := filesIn(t, f.tools); !slices.Equal(got, []string{"tool.exe"}) {
		t.Fatalf("tools folder = %v", got)
	}
	if got := f.fake.UserPath(); !slices.Equal(got, []string{f.tools}) {
		t.Errorf("PATH = %v", got)
	}
	got := f.entry(entry)
	in, _ := got.Installed()
	if in.Method != install.MethodTool || in.Folder != f.tools || !slices.Equal(in.Commands, []string{"tool"}) || in.SHA256 == "" {
		t.Errorf("install = %+v", in)
	}
	if got.Intent == nil || !slices.Equal(got.Intent.ArchiveMembers, []string{"tool.exe"}) {
		t.Errorf("intent = %+v: the chosen programs join it", got.Intent)
	}
	requireText(t, f.tt, "tool installed: v1.0.0", "Open a new terminal to run tool.", "Commands", "See file location")
	if hasLabel(f.tt, "Launch") || len(f.fake.Shortcuts()) != 0 {
		t.Errorf("a tool has no Launch button or shortcut: %v", f.fake.Shortcuts())
	}
	click(t, f.tt, "See file location")
	f.finish()
	if got := f.revealed(); !slices.Equal(got, []string{filepath.Join(f.tools, "tool.exe")}) {
		t.Errorf("shown = %v", got)
	}

	// The PATH is told about once: installing again adds no second notice.
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.finish()
	f.tt.Frame()
	if n := countText(f.tt, "Open a new terminal to run tool."); n != 1 {
		t.Errorf("the notice shows %d times", n)
	}
}

func TestTheKindIsSettledFromTheProgramWhenItWasGuessed(t *testing.T) {
	f := newInstallFixture(t)
	// An archive with no hints is guessed to be an app, but its program is a console one.
	f.source.publish("tool", "", f.release("v1.0.0", 1, "tool-win-x64.zip", map[string][]byte{"tool.exe": console("t")}))
	entry := f.track("tool", catalog.AddOptions{})
	if entry.Item.Kind != "" {
		t.Fatalf("kind = %q", entry.Item.Kind)
	}
	f.install(entry)
	got := f.entry(entry)
	if got.Item.Kind != selection.ItemTool {
		t.Errorf("kind = %q, want it stored as a tool", got.Item.Kind)
	}
	if in, _ := got.Installed(); in.Method != install.MethodTool || !slices.Equal(filesIn(t, f.tools), []string{"tool.exe"}) {
		t.Errorf("install = %+v, tools folder %v", in, filesIn(t, f.tools))
	}
	if len(filesIn(t, f.apps)) != 0 {
		t.Errorf("apps folder = %v", filesIn(t, f.apps))
	}
}

func TestAKindTheUserChoseIsNotSettled(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": console("t")}))
	entry := f.track("app", catalog.AddOptions{Kind: selection.ItemApp})
	f.install(entry)
	if in, _ := f.entry(entry).Installed(); in.Method != install.MethodPortable {
		t.Errorf("install = %+v: the person said it is an app", in)
	}
}

func TestAnUpdateKeepsTheProgramsOwnFilesReplacesShippedOnesAndLeavesNoOrphans(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{
		"app.exe": gui("v1"), "lib/old.dll": []byte("old"), "lib/keep.dll": []byte("keep-1"), "plugins/gone.txt": []byte("gone"),
	}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	folder := filepath.Join(f.apps, "app")
	write(t, filepath.Join(folder, "settings.json"), "{}")
	write(t, filepath.Join(folder, "saves", "slot1.sav"), "save")

	f.source.publish("app", "", f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{
		"app.exe": gui("v2"), "lib/keep.dll": []byte("keep-2"), "lib/new.dll": []byte("new"),
	}), f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	f.refresh(entry.Item.ID)

	click(t, f.tt, "Updates")
	requireText(t, f.tt, "Update everything", "Update")
	click(t, f.tt, "Update")
	f.finish()

	want := []string{"app.exe", "lib/keep.dll", "lib/new.dll", "saves/slot1.sav", "settings.json"}
	if got := filesIn(t, folder); !slices.Equal(got, want) {
		t.Fatalf("app folder = %v, want %v", got, want)
	}
	if got := readFile(t, filepath.Join(folder, "lib", "keep.dll")); got != "keep-2" {
		t.Errorf("keep.dll = %q", got)
	}
	files, err := f.screen.catalog.InstallFiles(t.Context(), entry.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	recorded := make([]string, 0, len(files))
	for _, file := range files {
		recorded = append(recorded, file.Path)
	}
	if !slices.Equal(recorded, []string{"app.exe", "lib/keep.dll", "lib/new.dll"}) {
		t.Errorf("recorded files = %v: dropped files and the program's own must not be listed", recorded)
	}
	if got := f.entry(entry); got.InstalledVersion() != "v2.0.0" || got.HasUpdate() {
		t.Errorf("installed %q, update %v", got.InstalledVersion(), got.HasUpdate())
	}
	if last := f.lastActivity(); last.Message != "Updated acme/app to v2.0.0" {
		t.Errorf("activity = %+v", last)
	}
	requireText(t, f.tt, "app updated to v2.0.0")
	if entries, _ := os.ReadDir(f.apps); len(entries) != 1 {
		t.Errorf("the apps folder holds %v, want only the app", entries)
	}
}

func TestUpdateEverythingCountsWhatNeedsAttention(t *testing.T) {
	f := newInstallFixture(t)
	for _, name := range []string{"one", "two"} {
		f.source.publish(name, "", f.release("v1.0.0", 1, name+"-1-win-x64.zip", map[string][]byte{name + ".exe": gui("v1")}))
		f.install(f.track(name, catalog.AddOptions{}))
	}
	good := f.release("v2.0.0", 5, "one-2-win-x64.zip", map[string][]byte{"one.exe": gui("v2")})
	f.source.publish("one", "", good)
	// The file of the second release is announced but the host does not have it.
	f.source.publish("two", "", source.Release{
		Tag: "v2.0.0", Name: "v2.0.0", Published: releaseDay.AddDate(0, 0, 5),
		Assets: []source.Asset{{Name: "two-2-win-x64.zip", Size: 10, URL: f.host.URL + "/missing.zip"}},
	})
	one, _ := f.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "one"})
	two, _ := f.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "two"})
	f.refresh(one.Item.ID)
	f.refresh(two.Item.ID)

	click(t, f.tt, "Updates")
	click(t, f.tt, "Update everything")
	f.finish()

	requireText(t, f.tt, "1 updated, 1 need attention")
	if got := f.entry(one).InstalledVersion(); got != "v2.0.0" {
		t.Errorf("one = %q", got)
	}
	failed := f.entry(two)
	if failed.InstalledVersion() != "v1.0.0" || failed.Item.InstallError != "The file could not be found on the server." {
		t.Errorf("two: installed %q, error %q", failed.InstalledVersion(), failed.Item.InstallError)
	}
	if last := f.lastActivity(); last.Message != "1 updated, 1 need attention" || last.Level != activity.Warning {
		t.Errorf("activity = %+v", last)
	}
	f.show(failed)
	requireText(t, f.tt, "The file could not be found on the server.")
}

func TestUpdateEverythingWithNothingToUpdateSaysSo(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("1")}))
	f.install(f.track("app", catalog.AddOptions{}))
	f.screen.updateAll()
	requireText(t, f.tt, "Everything is up to date")
}

func TestGoingBackToAnOlderVersionAsksFirst(t *testing.T) {
	f := newInstallFixture(t)
	v1 := f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")})
	v2 := f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")})
	f.source.publish("app", "", v2, v1)
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	f.show(entry)
	folder := filepath.Join(f.apps, "app")
	requireText(t, f.tt, "Reinstall this version", "Roll back to this version")

	// Declined: nothing is downloaded or changed.
	click(t, f.tt, "Roll back to this version")
	f.answer("Go back to v1.0.0?", "Cancel")
	f.finish()
	if got := readFile(t, filepath.Join(folder, "app.exe")); got != string(gui("v2")) {
		t.Fatal("a declined rollback changed the app")
	}
	if last := f.lastActivity(); last.Message != "Canceled rolling acme/app back to v1.0.0" || last.Level != activity.Info {
		t.Errorf("activity = %+v", last)
	}
	if f.entry(entry).Item.InstallError != "" {
		t.Error("declining is not a failure")
	}

	// Accepted.
	click(t, f.tt, "Roll back to this version")
	f.waitFor("app v2.0.0 is installed. v1.0.0 is an older version.")
	f.answer("Go back to v1.0.0?", "Go back")
	f.finish()
	if got := readFile(t, filepath.Join(folder, "app.exe")); got != string(gui("v1")) {
		t.Error("the app was not rolled back")
	}
	if got := f.entry(entry).InstalledVersion(); got != "v1.0.0" {
		t.Errorf("installed = %q", got)
	}
	requireText(t, f.tt, "app rolled back to v1.0.0")
	if last := f.lastActivity(); last.Message != "Rolled acme/app back to v1.0.0" {
		t.Errorf("activity = %+v", last)
	}
}

func TestReinstallingTheInstalledVersionRestoresItsFiles(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	f.show(entry)
	exe := filepath.Join(f.apps, "app", "app.exe")
	write(t, exe, "damaged")

	click(t, f.tt, "Reinstall this version")
	f.finish()
	if got := readFile(t, exe); got != string(gui("v1")) {
		t.Error("the file was not restored")
	}
	requireText(t, f.tt, "app reinstalled: v1.0.0")
}

func TestAProgramThatIsRunningIsClosedWithYourAgreementAndOpenedAgain(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	folder := filepath.Join(f.apps, "app")
	exe := filepath.Join(folder, "app.exe")
	f.source.publish("app", "", f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}))
	f.refresh(entry.Item.ID)
	f.fake.Running = []platform.Process{{ID: 41, Path: exe}}

	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.waitFor("app is running")
	requireText(t, f.tt, "Close it and update", "Open it again afterwards", "Waiting for you")
	if got := readFile(t, exe); got != string(gui("v1")) {
		t.Fatal("the app was changed before the person agreed")
	}
	click(t, f.tt, "Close it and update")
	f.finish()

	if got := f.fake.Closed(); len(got) != 1 || got[0].ID != 41 {
		t.Errorf("closed = %+v", got)
	}
	if got := readFile(t, exe); got != string(gui("v2")) {
		t.Error("the app was not updated")
	}
	if got := f.fake.Launched(); !slices.Equal(got, []string{exe}) {
		t.Errorf("launched = %v: the box to open it again was ticked", got)
	}
}

func TestAProgramThatIsRunningIsNotOpenedAgainWhenTheBoxIsUnticked(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	f.source.publish("app", "", f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}))
	f.refresh(entry.Item.ID)
	f.fake.Running = []platform.Process{{ID: 41, Path: filepath.Join(f.apps, "app", "app.exe")}}

	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.waitFor("app is running")
	click(t, f.tt, "Open it again afterwards")
	f.answer("app is running", "Close it and update")
	f.finish()
	if got := f.fake.Launched(); len(got) != 0 {
		t.Errorf("launched = %v", got)
	}
}

func TestDecliningToCloseAProgramLeavesItAlone(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	exe := filepath.Join(f.apps, "app", "app.exe")
	f.source.publish("app", "", f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}))
	f.refresh(entry.Item.ID)
	f.fake.Running = []platform.Process{{ID: 41, Path: exe}}

	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.answer("app is running", "Cancel")
	f.finish()

	if len(f.fake.Closed()) != 0 || readFile(t, exe) != string(gui("v1")) || f.entry(entry).InstalledVersion() != "v1.0.0" {
		t.Error("declining changed something")
	}
	if last := f.lastActivity(); last.Message != "Canceled updating acme/app to v2.0.0" {
		t.Errorf("activity = %+v", last)
	}
	// The staging folder is gone too.
	if entries, _ := os.ReadDir(f.apps); len(entries) != 1 {
		t.Errorf("the apps folder holds %v", entries)
	}
}

func TestCancelingWhileDownloadingLeavesTheMachineUntouched(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.show(entry)
	gate := make(chan struct{})
	f.host.setGate(gate)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})

	click(t, f.tt, "Install")
	f.waitFor("Starting…")
	click(t, f.tt, "Cancel")
	f.finish()

	if len(filesIn(t, f.apps)) != 0 || f.entry(entry).InstalledVersion() != "" || len(f.fake.Shortcuts()) != 0 {
		t.Error("a canceled install changed the machine")
	}
	if last := f.lastActivity(); last.Message != "Canceled installing acme/app v1.0.0" {
		t.Errorf("activity = %+v", last)
	}
	requireText(t, f.tt, "Install")
}

// A running program makes the first update wait for an answer, which holds the
// one place installs are applied in; the second waits behind it.
func TestCancelingWhileWaitingInTheQueueAndWhileWaitingForYou(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("one", "", f.release("v1.0.0", 1, "one-1-win-x64.zip", map[string][]byte{"one.exe": gui("v1")}))
	f.source.publish("two", "", f.release("v1.0.0", 1, "two-1-win-x64.zip", map[string][]byte{"two.exe": gui("v1")}))
	one := f.track("one", catalog.AddOptions{})
	two := f.track("two", catalog.AddOptions{})
	f.install(one)
	f.source.publish("one", "", f.release("v2.0.0", 5, "one-2-win-x64.zip", map[string][]byte{"one.exe": gui("v2")}))
	f.refresh(one.Item.ID)
	f.fake.Running = []platform.Process{{ID: 41, Path: filepath.Join(f.apps, "one", "one.exe")}}

	f.screen.startInstall(one.Item.ID, "v2.0.0", nil)
	f.waitFor("one is running")
	if !f.screen.startInstall(two.Item.ID, "v1.0.0", nil) {
		t.Fatal("the second install did not start")
	}
	// The second is queued behind the first once it has downloaded.
	deadline := time.Now().Add(5 * time.Second)
	for !f.screen.lanes.Queued(applyJob(two.Item.ID)) {
		if time.Now().After(deadline) {
			t.Fatal("the second install never queued")
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.screen.navigate("library")
	f.tt.Frame()
	requireText(t, f.tt, "Waiting in queue…", "Waiting for you")

	f.screen.cancelOperation(two.Item.ID)
	f.tt.Frame()
	if _, working := f.screen.opOf(two.Item.ID); working {
		t.Fatal("the queued install is still listed")
	}
	if last := f.lastActivity(); last.Message != "Canceled installing acme/two v1.0.0" {
		t.Errorf("activity = %+v", last)
	}

	// Canceling while the question is open takes the question away.
	f.screen.cancelOperation(one.Item.ID)
	f.finish()
	f.tt.Frame()
	requireNoText(t, f.tt, "one is running")
	if len(f.fake.Closed()) != 0 || f.entry(one).InstalledVersion() != "v1.0.0" || len(filesIn(t, filepath.Join(f.apps, "two"))) != 0 {
		t.Error("a canceled operation changed the machine")
	}
}

func TestOnceTheMachineIsBeingChangedTheOperationCannotBeCanceled(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.show(entry)
	f.screen.putOp(operation{itemID: entry.Item.ID, kind: opInstall, tag: "v1.0.0", stage: stageApplying})
	requireText(t, f.tt, "Installing…", "Cancel")
	f.screen.changeOp(entry.Item.ID, func(o *operation) { o.committing = true })
	f.tt.Frame()
	requireText(t, f.tt, "Installing…")
	if hasLabel(f.tt, "Cancel") {
		t.Error("Cancel is offered while the files are being replaced")
	}
	f.screen.cancelOperation(entry.Item.ID)
	if _, working := f.screen.opOf(entry.Item.ID); !working {
		t.Error("the operation was canceled mid-way")
	}
}

func TestAnUnsignedProgramIsInstalledOnlyWhenYouSayYes(t *testing.T) {
	f := newInstallFixture(t)
	f.fake.Result = platform.Signature{State: platform.SignatureUnsigned}
	f.source.publish("tool", "", f.releaseOf("v1.0.0", 1, "tool-x64.exe", console("t")))
	entry := f.track("tool", catalog.AddOptions{Kind: selection.ItemTool})

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.answer("Install tool-x64.exe?", "Cancel")
	f.finish()
	if len(filesIn(t, f.tools)) != 0 || f.entry(entry).InstalledVersion() != "" {
		t.Fatal("a program was installed against the answer")
	}

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.waitFor("It isn't digitally signed.")
	f.answer("Install tool-x64.exe?", "Install anyway")
	f.finish()
	if got := filesIn(t, f.tools); !slices.Equal(got, []string{"tool.exe"}) {
		t.Errorf("tools folder = %v: the standalone program takes its command name from its file", got)
	}
}

func TestABadChecksumStopsTheInstall(t *testing.T) {
	f := newInstallFixture(t)
	body, _ := installtest.Zip(map[string][]byte{"app.exe": gui("v1")})
	zip := f.host.add("app-win-x64.zip", body)
	sums := f.host.add("checksums.txt", []byte(sha([]byte("other"))+"  app-win-x64.zip\n"))
	f.source.publish("app", "", source.Release{Tag: "v1.0.0", Name: "v1.0.0", Published: releaseDay, Assets: []source.Asset{zip, sums}})
	entry := f.track("app", catalog.AddOptions{})
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.finish()
	got := f.entry(entry)
	want := "The download's checksum doesn't match the one the publisher posted. The file was deleted."
	if got.Item.InstallError != want || got.InstalledVersion() != "" || len(filesIn(t, f.apps)) != 0 {
		t.Errorf("error %q, installed %q", got.Item.InstallError, got.InstalledVersion())
	}
	if last := f.lastActivity(); last.Level != activity.Error || !strings.Contains(last.Message, want) {
		t.Errorf("activity = %+v", last)
	}
}

func TestUninstallingFromTheDefaultLocationDeletesTheFolderAndKeepsTheItem(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	folder := filepath.Join(f.apps, "app")
	write(t, filepath.Join(folder, "saves", "slot1.sav"), "save")
	f.show(entry)

	click(t, f.tt, "Uninstall from this PC")
	requireText(t, f.tt, "Uninstall app from this PC?", "This deletes "+folder+", including anything app saved there.")
	click(t, f.tt, "Uninstall")
	f.finish()

	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the folder is still there: %v", err)
	}
	if len(f.fake.Shortcuts()) != 0 {
		t.Errorf("shortcuts = %v", f.fake.Shortcuts())
	}
	got := f.entry(entry)
	if got.Install != nil || got.Status(false) != catalog.StatusNotInstalled {
		t.Errorf("install = %+v, status %v", got.Install, got.Status(false))
	}
	if files, _ := f.screen.catalog.InstallFiles(t.Context(), entry.Item.ID); len(files) != 0 {
		t.Errorf("files are still recorded: %v", files)
	}
	requireText(t, f.tt, "app uninstalled", "Not installed", "Install")
	if last := f.lastActivity(); last.Message != "Uninstalled acme/app" || last.Level != activity.Success {
		t.Errorf("activity = %+v", last)
	}
}

func TestUninstallingFromACustomFolderDeletesOnlyWhatWasInstalled(t *testing.T) {
	f := newInstallFixture(t)
	custom := filepath.Join(t.TempDir(), "mine")
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1"), "lib/a.dll": []byte("a")}))
	entry := f.track("app", catalog.AddOptions{InstallFolder: custom})
	f.install(entry)
	if got := filesIn(t, custom); !slices.Equal(got, []string{"app.exe", "lib/a.dll"}) {
		t.Fatalf("custom folder = %v", got)
	}
	write(t, filepath.Join(custom, "notes.txt"), "mine")
	f.show(entry)

	click(t, f.tt, "Uninstall from this PC")
	requireText(t, f.tt, "This deletes the files app installed in "+custom+". Other files in the folder stay.")
	click(t, f.tt, "Uninstall")
	f.finish()

	if got := filesIn(t, custom); !slices.Equal(got, []string{"notes.txt"}) {
		t.Errorf("custom folder = %v", got)
	}
	requireText(t, f.tt, "app uninstalled. 1 other file was left in its folder.")
	if len(f.fake.Shortcuts()) != 0 {
		t.Errorf("shortcuts = %v", f.fake.Shortcuts())
	}
}

func TestACustomFolderThatIsNotEmptyGetsASubfolder(t *testing.T) {
	f := newInstallFixture(t)
	custom := filepath.Join(t.TempDir(), "mine")
	write(t, filepath.Join(custom, "notes.txt"), "mine")
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{InstallFolder: custom})
	f.install(entry)
	if in, _ := f.entry(entry).Installed(); in.Folder != filepath.Join(custom, "app") || in.Dedicated {
		t.Errorf("install = %+v", in)
	}
	if got := filesIn(t, custom); !slices.Equal(got, []string{"app/app.exe", "notes.txt"}) {
		t.Errorf("custom folder = %v", got)
	}
}

func TestAFolderThatIsTooBroadIsRefusedAndNothingIsChanged(t *testing.T) {
	f := newInstallFixture(t)
	broad := t.TempDir()
	write(t, filepath.Join(broad, "keep.txt"), "mine")
	f.fake.Known.Protected = []string{broad}
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{InstallFolder: broad})

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.finish()

	want := "That install folder is too broad to manage safely. Choose a dedicated folder for this app."
	if got := f.entry(entry); got.Item.InstallError != want || got.InstalledVersion() != "" {
		t.Errorf("error %q, installed %q", got.Item.InstallError, got.InstalledVersion())
	}
	if got := filesIn(t, broad); !slices.Equal(got, []string{"keep.txt"}) {
		t.Errorf("the folder holds %v", got)
	}
	f.show(entry)
	requireText(t, f.tt, want)
}

func TestTwoOwnersOfOneRepositoryNameGetDifferentFolders(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("acme")}))
	f.source.publishOwned("other", "app", "", f.release("v1.0.0", 1, "app-other-win-x64.zip", map[string][]byte{"app.exe": gui("other")}))
	first := f.track("app", catalog.AddOptions{})
	second := f.trackOwned("other", "app", catalog.AddOptions{})
	f.install(first)
	f.install(second)

	a, _ := f.entry(first).Installed()
	b, _ := f.entry(second).Installed()
	if a.Folder != filepath.Join(f.apps, "app") || b.Folder != filepath.Join(f.apps, "app-other") {
		t.Fatalf("folders = %q and %q", a.Folder, b.Folder)
	}
	if readFile(t, filepath.Join(a.Folder, "app.exe")) != string(gui("acme")) || readFile(t, filepath.Join(b.Folder, "app.exe")) != string(gui("other")) {
		t.Error("the two apps share files")
	}
	// An update stays in the folder it was installed in.
	f.source.publishOwned("other", "app", "", f.release("v2.0.0", 5, "app-other-2-win-x64.zip", map[string][]byte{"app.exe": gui("other2")}))
	f.install(second)
	if got, _ := f.entry(second).Installed(); got.Folder != b.Folder || got.Tag != "v2.0.0" {
		t.Errorf("after the update: %+v", got)
	}
}

func TestStoppingTrackingCanDeleteTheInstalledFilesToo(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	folder := filepath.Join(f.apps, "app")
	f.show(entry)

	click(t, f.tt, "Stop tracking (keeps it installed)")
	requireText(t, f.tt, "Stop tracking app?", "Also delete the files app installed")
	click(t, f.tt, "Also delete the files app installed")
	click(t, f.tt, "Remove from library")
	f.finish()

	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the folder is still there: %v", err)
	}
	if _, ok := f.screen.catalog.Get(entry.Item.ID); ok {
		t.Error("the Item is still tracked")
	}
	if len(f.fake.Shortcuts()) != 0 {
		t.Errorf("shortcuts = %v", f.fake.Shortcuts())
	}
}

func TestStoppingTrackingKeepsTheInstalledFilesByDefault(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	f.show(entry)

	click(t, f.tt, "Stop tracking (keeps it installed)")
	click(t, f.tt, "Remove from library")
	f.finish()
	if got := filesIn(t, filepath.Join(f.apps, "app")); !slices.Equal(got, []string{"app.exe"}) {
		t.Errorf("app folder = %v", got)
	}
	if _, ok := f.screen.catalog.Get(entry.Item.ID); ok {
		t.Error("the Item is still tracked")
	}
}

func TestTheRemoveQuestionIsSkippedOnlyWhenTurnedOffAndNothingWouldBeDeleted(t *testing.T) {
	f := newInstallFixture(t)
	f.screen.prefs.Update(func(p *preferences) { p.ConfirmRemove = false })
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	f.source.publish("other", "", f.release("v1.0.0", 1, "other-win-x64.zip", map[string][]byte{"other.exe": gui("v1")}))
	installed := f.track("app", catalog.AddOptions{})
	plain := f.track("other", catalog.AddOptions{})
	f.install(installed)

	// Nothing to delete: removed at once.
	f.show(plain)
	click(t, f.tt, "Stop tracking")
	f.finish()
	if _, ok := f.screen.catalog.Get(plain.Item.ID); ok {
		t.Error("the Item that has nothing installed was not removed without a question")
	}

	// With files to offer to delete, the question stays; uninstalling needs no confirmation.
	f.show(installed)
	click(t, f.tt, "Stop tracking (keeps it installed)")
	requireText(t, f.tt, "Also delete the files app installed")
	click(t, f.tt, "Cancel")
	f.tt.Frame()
	click(t, f.tt, "Uninstall from this PC")
	f.finish()
	if got := f.entry(installed); got.Install != nil {
		t.Errorf("install = %+v: uninstalling should not have asked", got.Install)
	}
}

func TestAnAppInstalledWithoutMembersIsUpdatedInPlaceAfterBeingMarkedInstalled(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v0.9.0"})
	if _, managed := entry.Installed(); managed {
		t.Fatal("a version the person says they have is not a managed install")
	}
	f.install(entry)
	if in, managed := f.entry(entry).Installed(); !managed || in.Tag != "v1.0.0" {
		t.Errorf("install = %+v, %v", in, managed)
	}
}

func TestAddingWithInstallAfterAddingInstallsTheRelease(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "An app", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	typeInto(t, f.tt, "Search or paste a repository link", "acme/app")
	click(t, f.tt, "Add")
	f.settle()
	requireText(t, f.tt, "Install after adding", "Install location", "Default location")
	click(t, f.tt, "Add")
	f.settle()
	f.finish()

	if got := filesIn(t, filepath.Join(f.apps, "app")); !slices.Equal(got, []string{"app.exe"}) {
		t.Errorf("app folder = %v", got)
	}
	entry, _ := f.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "app"})
	if entry.InstalledVersion() != "v1.0.0" {
		t.Errorf("installed = %q", entry.InstalledVersion())
	}
}

func TestAddingWithInstallAfterAddingUntickedOnlyTracks(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "An app", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	typeInto(t, f.tt, "Search or paste a repository link", "acme/app")
	click(t, f.tt, "Add")
	f.settle()
	click(t, f.tt, "Install after adding")
	click(t, f.tt, "Add")
	f.settle()
	f.finish()
	if got := filesIn(t, f.apps); len(got) != 0 {
		t.Errorf("apps folder = %v", got)
	}
	if entry, ok := f.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "app"}); !ok || entry.InstalledVersion() != "" {
		t.Errorf("entry = %+v, %v", entry.Install, ok)
	}
}

func TestTheInstallOnAddSettingIsTheDialogsDefault(t *testing.T) {
	f := newInstallFixture(t)
	f.screen.prefs.Update(func(p *preferences) { p.InstallAfterAdding = false })
	f.source.publish("app", "An app", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	typeInto(t, f.tt, "Search or paste a repository link", "acme/app")
	click(t, f.tt, "Add")
	f.settle()
	click(t, f.tt, "Add")
	f.settle()
	f.finish()
	if got := filesIn(t, f.apps); len(got) != 0 {
		t.Errorf("apps folder = %v", got)
	}
}

func suite(version string, programs ...string) map[string][]byte {
	files := map[string][]byte{"suite-" + version + "/README.md": []byte("docs")}
	for _, p := range programs {
		files["suite-"+version+"/bin/"+p+".exe"] = console(p + version)
	}
	return files
}

func TestWhichProgramsToAddIsAskedOnceAndRememberedForUpdates(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("suite", "", f.release("v1.0.0", 1, "suite-1.0.0-windows-x64.zip", suite("1.0.0", "alpha", "beta")))
	entry := f.track("suite", catalog.AddOptions{Kind: selection.ItemTool})

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.waitFor("Which programs should be added to PATH?")
	requireText(t, f.tt, "bin/alpha.exe", "bin/beta.exe")
	click(t, f.tt, "bin/alpha.exe") // Nothing is ticked until the person chooses.
	click(t, f.tt, "Add to PATH")
	f.finish()
	if got := filesIn(t, f.tools); !slices.Equal(got, []string{"alpha.exe"}) {
		t.Fatalf("tools folder = %v", got)
	}
	got := f.entry(entry)
	if got.Intent == nil || !slices.Equal(got.Intent.ArchiveMembers, []string{"bin/alpha.exe"}) {
		t.Fatalf("intent = %+v", got.Intent)
	}

	// The next release keeps the programs without asking, though its wrapper folder is named for the new version.
	f.source.publish("suite", "", f.release("v2.0.0", 5, "suite-2.0.0-windows-x64.zip", suite("2.0.0", "alpha", "beta", "gamma")))
	f.refresh(entry.Item.ID)
	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.finish()
	if got := filesIn(t, f.tools); !slices.Equal(got, []string{"alpha.exe"}) {
		t.Errorf("tools folder after the update = %v", got)
	}
	if v := readFile(t, filepath.Join(f.tools, "alpha.exe")); v != string(console("alpha2.0.0")) {
		t.Error("alpha was not updated")
	}
	if got := f.entry(entry).InstalledVersion(); got != "v2.0.0" {
		t.Errorf("installed = %q", got)
	}
}

func TestAProgramMissingFromALaterReleaseIsAskedAboutAgain(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("suite", "", f.release("v1.0.0", 1, "suite-1.0.0-windows-x64.zip", suite("1.0.0", "alpha", "beta")))
	entry := f.track("suite", catalog.AddOptions{Kind: selection.ItemTool})
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.waitFor("Which programs should be added to PATH?")
	click(t, f.tt, "bin/alpha.exe")
	click(t, f.tt, "bin/beta.exe")
	click(t, f.tt, "Add to PATH")
	f.finish()
	if got := filesIn(t, f.tools); !slices.Equal(got, []string{"alpha.exe", "beta.exe"}) {
		t.Fatalf("tools folder = %v", got)
	}

	f.source.publish("suite", "", f.release("v2.0.0", 5, "suite-2.0.0-windows-x64.zip", suite("2.0.0", "beta", "gamma")))
	f.refresh(entry.Item.ID)
	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.waitFor("v2.0.0 no longer has bin/alpha.exe. Choose again.")
	click(t, f.tt, "bin/gamma.exe") // The program that is still there is ticked already.
	click(t, f.tt, "Add to PATH")
	f.finish()
	// The commands are what was chosen now: alpha is gone, beta kept, gamma new, and no orphans.
	if got := filesIn(t, f.tools); !slices.Equal(got, []string{"beta.exe", "gamma.exe"}) {
		t.Errorf("tools folder = %v", got)
	}
	if in, _ := f.entry(entry).Installed(); !slices.Equal(in.Commands, []string{"beta", "gamma"}) {
		t.Errorf("commands = %v", in.Commands)
	}
}

func TestDecliningToChooseProgramsInstallsNothing(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("suite", "", f.release("v1.0.0", 1, "suite-1.0.0-windows-x64.zip", suite("1.0.0", "alpha", "beta")))
	entry := f.track("suite", catalog.AddOptions{Kind: selection.ItemTool})
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.answer("Which programs should be added to PATH?", "Cancel")
	f.finish()
	if len(filesIn(t, f.tools)) != 0 || f.entry(entry).InstalledVersion() != "" || len(f.fake.UserPath()) != 0 {
		t.Error("declining changed the machine")
	}
}

func TestACommandAnotherItemHasBlocksTheInstallAndNamesIt(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("tool", "", f.release("v1.0.0", 1, "tool-win-x64.zip", map[string][]byte{"tool.exe": console("one")}))
	f.source.publish("fork", "", f.release("v1.0.0", 1, "fork-win-x64.zip", map[string][]byte{"tool.exe": console("two")}))
	first := f.track("tool", catalog.AddOptions{Kind: selection.ItemTool})
	second := f.track("fork", catalog.AddOptions{Kind: selection.ItemTool})
	f.install(first)

	f.screen.startInstall(second.Item.ID, "v1.0.0", nil)
	f.finish()
	want := "The command tool already belongs to acme/tool."
	if got := f.entry(second); got.Item.InstallError != want || got.InstalledVersion() != "" {
		t.Errorf("error %q, installed %q", got.Item.InstallError, got.InstalledVersion())
	}
	if got := readFile(t, filepath.Join(f.tools, "tool.exe")); got != string(console("one")) {
		t.Error("the first tool's command was replaced")
	}
	f.show(second)
	requireText(t, f.tt, want)
}

func TestAFileThatIsNotOursIsReplacedOnlyWhenYouAgree(t *testing.T) {
	f := newInstallFixture(t)
	write(t, filepath.Join(f.tools, "tool.exe"), "somebody else's")
	f.source.publish("tool", "", f.release("v1.0.0", 1, "tool-win-x64.zip", map[string][]byte{"tool.exe": console("mine")}))
	entry := f.track("tool", catalog.AddOptions{Kind: selection.ItemTool})

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.waitFor("Replace the existing tool?")
	requireText(t, f.tt, "The tools folder already has tool.exe, and tool did not put it there.")
	click(t, f.tt, "Cancel")
	f.finish()
	if got := readFile(t, filepath.Join(f.tools, "tool.exe")); got != "somebody else's" || f.entry(entry).InstalledVersion() != "" {
		t.Fatal("the file was replaced against the answer")
	}

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.answer("Replace the existing tool?", "Replace")
	f.finish()
	if got := readFile(t, filepath.Join(f.tools, "tool.exe")); got != string(console("mine")) {
		t.Error("the file was not replaced")
	}
	// Our own file is replaced by an update without asking.
	f.source.publish("tool", "", f.release("v2.0.0", 5, "tool-2-win-x64.zip", map[string][]byte{"tool.exe": console("mine2")}))
	f.refresh(entry.Item.ID)
	f.screen.startInstall(entry.Item.ID, "v2.0.0", nil)
	f.finish()
	if got := readFile(t, filepath.Join(f.tools, "tool.exe")); got != string(console("mine2")) {
		t.Error("the update did not replace the tool")
	}
}

func TestUninstallingAToolRemovesItsCommandsAndLeavesThePath(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("tool", "", f.release("v1.0.0", 1, "tool-win-x64.zip", map[string][]byte{"tool.exe": console("one")}))
	entry := f.track("tool", catalog.AddOptions{Kind: selection.ItemTool})
	f.install(entry)
	f.show(entry)
	click(t, f.tt, "Uninstall from this PC")
	requireText(t, f.tt, "This removes the commands tool. Your PATH is left as it is.")
	click(t, f.tt, "Uninstall")
	f.finish()
	if got := filesIn(t, f.tools); len(got) != 0 {
		t.Errorf("tools folder = %v", got)
	}
	if got := f.fake.UserPath(); !slices.Equal(got, []string{f.tools}) {
		t.Errorf("PATH = %v", got)
	}
	if got := f.entry(entry); got.Install != nil {
		t.Errorf("install = %+v", got.Install)
	}
}

func TestUninstallingWhileTheProgramRunsAsksToCloseItAndDoesNotOfferToOpenItAgain(t *testing.T) {
	f := newInstallFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.install(entry)
	folder := filepath.Join(f.apps, "app")
	f.fake.Running = []platform.Process{{ID: 7, Path: filepath.Join(folder, "app.exe")}}

	f.screen.startUninstall(f.entry(entry), false)
	f.waitFor("app is running")
	requireText(t, f.tt, "It has to be closed before its files can be removed.", "Close it and uninstall")
	requireNoText(t, f.tt, "Open it again afterwards")
	click(t, f.tt, "Close it and uninstall")
	f.finish()
	if _, err := os.Stat(folder); !os.IsNotExist(err) || len(f.fake.Closed()) != 1 || len(f.fake.Launched()) != 0 {
		t.Errorf("folder error %v, closed %v, launched %v", err, f.fake.Closed(), f.fake.Launched())
	}
}
