package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

// autoFixture is an install fixture with a desktop that shows nothing, and the
// releases it has published.
type autoFixture struct {
	*installFixture
	desktop *background.Fake
}

func newAutoFixture(t *testing.T) *autoFixture {
	t.Helper()
	f := &autoFixture{installFixture: newInstallFixture(t), desktop: &background.Fake{}}
	f.screen.desktop = f.desktop
	return f
}

// installed tracks a repository, installs its release and returns the Item.
func (f *autoFixture) installed(name string, options catalog.AddOptions, first source.Release) catalog.Entry {
	f.t.Helper()
	f.source.publish(name, "", first)
	entry := f.track(name, options)
	f.install(entry)
	return f.entry(entry)
}

// update publishes a newer release above the old ones and checks the Item.
func (f *autoFixture) update(e catalog.Entry, newer source.Release) {
	f.t.Helper()
	f.source.publish(e.Item.Repo, "", append([]source.Release{newer}, e.Releases...)...)
	f.refresh(e.Item.ID)
}

func (f *autoFixture) automatic(e catalog.Entry) {
	f.t.Helper()
	if err := f.screen.catalog.SetAutoUpdate(f.t.Context(), e.Item.ID, true); err != nil {
		f.t.Fatal(err)
	}
}

// pass runs what follows a scheduled check.
func (f *autoFixture) pass() {
	f.t.Helper()
	if err := f.screen.handleFoundUpdates(f.t.Context(), false); err != nil {
		f.t.Fatal(err)
	}
}

func (f *autoFixture) titles() []string {
	notices := f.desktop.Notices()
	out := make([]string, len(notices))
	for i, n := range notices {
		out[i] = n.Title + " | " + n.Body
	}
	return out
}

func (f *autoFixture) hasActivity(text string) bool {
	return slices.Contains(f.activityMessages(), text)
}

func tool(version string) map[string][]byte {
	return map[string][]byte{"tool-" + version + "-windows-x64/tool.exe": console(version)}
}

func TestAMixedRunUpdatesSomeItemsAndAnnouncesTheRestOnce(t *testing.T) {
	f := newAutoFixture(t)
	toolV1 := f.release("v1.0.0", 1, "tool-v1.0.0-windows-x64.zip", tool("v1.0.0"))
	appV1 := f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")})
	otherV1 := f.release("v1.0.0", 1, "other-1-win-x64.zip", map[string][]byte{"other.exe": gui("v1")})
	tl := f.installed("tool", catalog.AddOptions{Kind: selection.ItemTool}, toolV1)
	app := f.installed("app", catalog.AddOptions{}, appV1)
	other := f.installed("other", catalog.AddOptions{}, otherV1)
	f.automatic(tl)
	f.automatic(other)
	// The repository of "other" was only guessed from the program's name.
	if _, err := f.db.SQL.ExecContext(t.Context(), `UPDATE items SET link_guessed = 1 WHERE id = ?`, other.Item.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.screen.catalog.SetHoldUpdates(t.Context(), other.Item.ID, false); err != nil {
		t.Fatal(err) // Publishes the Item again with the change above.
	}
	f.update(tl, f.release("v2.0.0", 5, "tool-v2.0.0-windows-x64.zip", tool("v2.0.0")))
	f.update(app, f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}))
	f.update(other, f.release("v2.0.0", 5, "other-2-win-x64.zip", map[string][]byte{"other.exe": gui("v2")}))

	f.pass()

	if got := f.entry(tl).InstalledVersion(); got != "v2.0.0" {
		t.Errorf("the tool is at %q, want it updated", got)
	}
	if got := f.entry(app).InstalledVersion(); got != "v1.0.0" {
		t.Errorf("the app that does not update automatically is at %q", got)
	}
	if got := f.entry(other).InstalledVersion(); got != "v1.0.0" {
		t.Errorf("the app of an unconfirmed link is at %q", got)
	}
	if !f.hasActivity("acme/other wasn't updated automatically: confirm it follows the right project.") {
		t.Errorf("activity = %q", f.activityMessages())
	}
	want := []string{"2 updates available | app, other", "1 update installed | tool"}
	if got := f.titles(); !slices.Equal(got, want) {
		t.Errorf("notifications = %q, want %q", got, want)
	}
	for _, e := range []catalog.Entry{app, other} {
		if got := f.entry(e).Item.NotifiedVersion; got != "v2.0.0" {
			t.Errorf("%s notified of %q", e.FullName(), got)
		}
	}

	// A version is announced once: the next check finds nothing new to say.
	before := len(f.activityMessages())
	f.pass()
	if got := f.titles(); !slices.Equal(got, want) || len(f.activityMessages()) != before {
		t.Errorf("a second pass said more: notifications %q, activity %q", got, f.activityMessages())
	}

	// The next version is a new announcement.
	f.update(app, f.release("v3.0.0", 9, "app-3-win-x64.zip", map[string][]byte{"app.exe": gui("v3")}))
	f.pass()
	if got := f.titles(); len(got) != 3 || got[2] != "Update available | app" {
		t.Errorf("notifications = %q", got)
	}
}

func TestInstallingAutomaticallyForEveryItemIsTheGlobalSetting(t *testing.T) {
	f := newAutoFixture(t)
	app := f.installed("app", catalog.AddOptions{}, f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	f.update(app, f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}))
	f.screen.prefs.Update(func(p *preferences) { p.AutoInstall = true })

	f.pass()

	if got := f.entry(app).InstalledVersion(); got != "v2.0.0" {
		t.Errorf("installed %q", got)
	}
	if got := f.titles(); !slices.Equal(got, []string{"1 update installed | app"}) {
		t.Errorf("notifications = %q", got)
	}
}

func TestAHeldItemAndASkippedVersionAreNeverUpdatedOrAnnounced(t *testing.T) {
	f := newAutoFixture(t)
	held := f.installed("held", catalog.AddOptions{}, f.release("v1.0.0", 1, "held-1-win-x64.zip", map[string][]byte{"held.exe": gui("v1")}))
	skipped := f.installed("skipped", catalog.AddOptions{}, f.release("v1.0.0", 1, "skipped-1-win-x64.zip", map[string][]byte{"skipped.exe": gui("v1")}))
	f.screen.prefs.Update(func(p *preferences) { p.AutoInstall = true })
	if err := f.screen.catalog.SetHoldUpdates(t.Context(), held.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.screen.catalog.SetSkippedVersion(t.Context(), skipped.Item.ID, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	f.update(held, f.release("v2.0.0", 5, "held-2-win-x64.zip", map[string][]byte{"held.exe": gui("v2")}))
	f.update(skipped, f.release("v2.0.0", 5, "skipped-2-win-x64.zip", map[string][]byte{"skipped.exe": gui("v2")}))

	f.pass()

	for _, e := range []catalog.Entry{held, skipped} {
		if got := f.entry(e).InstalledVersion(); got != "v1.0.0" {
			t.Errorf("%s is at %q", e.FullName(), got)
		}
	}
	if got := f.titles(); len(got) != 0 {
		t.Errorf("notifications = %q", got)
	}
}

func TestARunningProgramIsNotClosedAndItsUpdateWaitsForTheNextCheckUnannounced(t *testing.T) {
	f := newAutoFixture(t)
	app := f.installed("app", catalog.AddOptions{}, f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	f.automatic(app)
	exe := filepath.Join(f.apps, "app", "app.exe")
	f.fake.Running = []platform.Process{{ID: 7, Path: exe}}
	f.update(app, f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}))
	before := len(f.activityMessages())

	f.pass()

	if got := readFile(t, exe); got != string(gui("v1")) {
		t.Error("a running app was updated")
	}
	if got := f.fake.Closed(); len(got) != 0 {
		t.Errorf("closed = %+v: unattended updates never close a program", got)
	}
	if got := f.titles(); len(got) != 0 || len(f.activityMessages()) != before {
		t.Errorf("a running program was announced or recorded: %q, %q", got, f.activityMessages())
	}
	if got := f.entry(app).Item.NotifiedVersion; got != "" {
		t.Errorf("notified of %q: the update must still be new at the next check", got)
	}

	f.fake.Running = nil
	f.pass()
	if got := f.entry(app).InstalledVersion(); got != "v2.0.0" {
		t.Errorf("after the program closed the app is at %q", got)
	}
	if got := f.titles(); !slices.Equal(got, []string{"1 update installed | app"}) {
		t.Errorf("notifications = %q", got)
	}
}

func TestADownloadThatNeedsAnAnswerIsLeftForThePersonWithItsFindings(t *testing.T) {
	f := newAutoFixture(t)
	first := f.installed("tool", catalog.AddOptions{Kind: selection.ItemTool}, f.releaseOf("v1.0.0", 1, "tool-x64.exe", console("1")))
	f.automatic(first)
	f.fake.Result = platform.Signature{State: platform.SignatureUnsigned}
	f.update(first, f.releaseOf("v2.0.0", 5, "tool-x64.exe", console("2")))

	f.pass()

	if got := f.entry(first).InstalledVersion(); got != "v1.0.0" {
		t.Errorf("an unsigned update was installed unattended: %q", got)
	}
	var found string
	for _, m := range f.activityMessages() {
		if strings.HasPrefix(m, "Skipped automatic install of acme/tool: ") {
			found = m
		}
	}
	if found == "" || !strings.Contains(found, "digitally signed") {
		t.Errorf("activity = %q", f.activityMessages())
	}
	if got := f.titles(); !slices.Equal(got, []string{"Update available | tool"}) {
		t.Errorf("notifications = %q: what was left is announced as an ordinary update", got)
	}
	if ops := f.screen.operations.Get(); len(ops) != 0 {
		t.Errorf("operations left behind: %+v", ops)
	}
}

// installerCase is an Item installed by a setup program, with the release that updates it.
type installerCase struct {
	name string
	// scope is whom the installed copy is for; first and again are the setup
	// programs of the two releases, built with toolkit.
	scope        platform.Scope
	first, again string
	toolkit      string
	// licence makes the update's MSI ask for a license to be accepted.
	licence bool
	// wantInstalled says the update is installed unattended; otherwise it is left,
	// and wantActivity is the reason recorded.
	wantInstalled bool
	wantActivity  string
}

func TestAnUnattendedInstallerNeverRaisesThePromptOrShowsItsWindow(t *testing.T) {
	for _, c := range []installerCase{
		{
			name: "an MSI installed for the user is updated quietly", scope: platform.ScopeUser,
			first: "app-1.0.0-x64.msi", again: "app-2.0.0-x64.msi", wantInstalled: true,
		},
		{
			name: "a setup program of a known toolkit installed for the user is updated quietly", scope: platform.ScopeUser,
			first: "app-1.0.0-x64-setup.exe", again: "app-2.0.0-x64-setup.exe", toolkit: nsisSetup, wantInstalled: true,
		},
		{
			name: "an app installed for everyone would raise the prompt", scope: platform.ScopeMachine,
			first: "app-1.0.0-x64.msi", again: "app-2.0.0-x64.msi",
			wantActivity: "acme/app wasn't updated automatically: the update needs administrator permission.",
		},
		{
			name: "a setup program of no known toolkit needs its window", scope: platform.ScopeUser,
			first: "app-1.0.0-x64-setup.exe", again: "app-2.0.0-x64-setup.exe",
			wantActivity: "acme/app wasn't updated automatically: its installer can't run without a window.",
		},
		{
			name: "an installer that asks for a license is not answered for the person", scope: platform.ScopeUser,
			first: "app-1.0.0-x64.msi", again: "app-2.0.0-x64.msi", licence: true,
			wantActivity: "acme/app wasn't updated automatically: its installer asks for a license to be accepted.",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newAutoFixture(t)
			app := f.setupApp()
			app.scope = c.scope
			isMSI := strings.HasSuffix(c.first, ".msi")
			updating := false
			f.msiApp(app, func(cmd platform.Command, _ int) (int, string) {
				if updating && c.licence && !strings.Contains(cmd.Args, "ACCEPTLICENSE=YES") {
					return 1603, "Product: app -- Please accept the license agreement by passing ACCEPTLICENSE=YES during installation.\n"
				}
				return 0, ""
			})
			if !isMSI {
				f.fake.RunFunc = app.run
			}
			body := func(version string) []byte {
				if isMSI {
					return []byte("msi " + version)
				}
				return gui(c.toolkit)
			}
			entry := f.installed("app", catalog.AddOptions{Kind: selection.ItemApp}, f.releaseOf("v1.0.0", 1, c.first, body("1")))
			f.automatic(entry)
			app.version, updating = "2.0.0", true
			f.update(entry, f.releaseOf("v2.0.0", 5, c.again, body("2")))
			commandsBefore := len(f.fake.Commands())

			f.pass()

			got := f.entry(entry).InstalledVersion()
			commands := f.fake.Commands()[commandsBefore:]
			if c.wantInstalled {
				if got != "v2.0.0" || len(commands) != 1 {
					t.Fatalf("installed %q after %d commands, want v2.0.0 after one", got, len(commands))
				}
				if cmd := commands[0]; cmd.Elevated || !cmd.NoPrompt {
					t.Errorf("command = %+v: an unattended installer must not be elevated and must not prompt", cmd)
				}
				return
			}
			if got != "v1.0.0" {
				t.Errorf("installed %q, want it left at v1.0.0", got)
			}
			for _, cmd := range commands {
				if cmd.Elevated || !cmd.NoPrompt {
					t.Errorf("command = %+v: the installer that was started is not allowed to prompt", cmd)
				}
			}
			if !f.hasActivity(c.wantActivity) {
				t.Errorf("activity = %q, want %q", f.activityMessages(), c.wantActivity)
			}
			if got := f.titles(); !slices.Equal(got, []string{"Update available | app"}) {
				t.Errorf("notifications = %q", got)
			}
			if in, _ := f.entry(entry).Installed(); in.Tag != "v1.0.0" {
				t.Errorf("install = %+v", in)
			}
		})
	}
}

func TestAManualCheckOrTurningNotificationsOffShowsNoNotificationButCountsTheVersionAsAnnounced(t *testing.T) {
	for _, c := range []struct {
		name   string
		manual bool
		notify bool
	}{{"manual check", true, true}, {"notifications off", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			f := newAutoFixture(t)
			app := f.installed("app", catalog.AddOptions{}, f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
			f.update(app, f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}))
			f.screen.prefs.Update(func(p *preferences) { p.NotifyUpdates = c.notify })

			if err := f.screen.handleFoundUpdates(t.Context(), c.manual); err != nil {
				t.Fatal(err)
			}

			if got := f.titles(); len(got) != 0 {
				t.Errorf("notifications = %q", got)
			}
			if got := f.entry(app).Item.NotifiedVersion; got != "v2.0.0" {
				t.Errorf("notified of %q", got)
			}
		})
	}
}

func TestANotificationNamesFourItemsAndCountsTheRest(t *testing.T) {
	entries := func(names ...string) []catalog.Entry {
		out := make([]catalog.Entry, len(names))
		for i, n := range names {
			out[i] = catalog.Entry{Item: catalog.Item{Repo: n}}
		}
		return out
	}
	one := availableNotice(entries("tool"))
	if one.Title != "Update available" || one.Body != "tool" {
		t.Errorf("one update = %+v", one)
	}
	four := availableNotice(entries("delta", "alpha", "Charlie", "bravo"))
	if four.Title != "4 updates available" || four.Body != "alpha, bravo, Charlie, delta" {
		t.Errorf("four updates = %+v", four)
	}
	six := availableNotice(entries("f", "e", "d", "c", "b", "a"))
	if six.Title != "6 updates available" || six.Body != "a, b, c, d and 2 more" {
		t.Errorf("six updates = %+v", six)
	}
	if got := installedNotice(entries("a", "b")); got.Title != "2 updates installed" || got.Body != "a, b" {
		t.Errorf("installed = %+v", got)
	}
}

func TestAnAutomaticUpdateIsRecordedLikeAnyOtherWithoutATitleToast(t *testing.T) {
	f := newAutoFixture(t)
	app := f.installed("app", catalog.AddOptions{}, f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	f.automatic(app)
	f.update(app, f.release("v2.0.0", 5, "app-2-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}))
	f.pass()
	if last := f.lastActivity(); last.Level != activity.Success || last.Message != "Updated acme/app to v2.0.0" {
		t.Errorf("activity = %+v", last)
	}
	f.tt.Frame()
	if f.tt.HasText("app updated") {
		t.Error("an unattended update showed a toast to nobody")
	}
}
