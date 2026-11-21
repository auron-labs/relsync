package background

import (
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

var published = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func releaseOf(tag string, days int, assets ...string) source.Release {
	r := source.Release{Tag: tag, Published: published.AddDate(0, 0, days)}
	for _, a := range assets {
		r.Assets = append(r.Assets, source.Asset{Name: a, Size: 1 << 20})
	}
	return r
}

// entryFor is an Item installed at v1.0.0 whose repository has released v2.0.0
// with the given files. Its stored choice is whatever the file of v1.0.0 was.
func entryFor(t *testing.T, repo string, old, latest []string, in catalog.Install) catalog.Entry {
	t.Helper()
	e := catalog.Entry{
		Item:     catalog.Item{ID: 1, Owner: "acme", Repo: repo, Kind: selection.ItemApp},
		Releases: []source.Release{releaseOf("v2.0.0", 30, latest...), releaseOf("v1.0.0", 1, old...)},
		Policy:   selection.Policy{Target: selection.Target{OS: selection.OSWindows, Arch: selection.ArchX64}, PreferInstallers: true},
	}
	pick, ok := e.Resolve(e.Releases[1]).Pick()
	if !ok {
		t.Fatalf("nothing to pick from %v", old)
	}
	intent := selection.IntentOf(pick, false)
	e.Intent = &intent
	in.Tag = "v1.0.0"
	e.Install = &in
	return e
}

func TestJudgeIsOneTableOverTheItemItsInstallAndTheSelection(t *testing.T) {
	user := catalog.Install{Method: install.MethodInstaller, Scope: platform.ScopeUser, UninstallKey: "k"}
	for _, c := range []struct {
		name    string
		mutate  func(e *catalog.Entry)
		old     []string
		latest  []string
		in      catalog.Install
		running bool
		want    Reason
	}{
		{
			name: "an MSI installed for the user", old: []string{"app-x64.msi"}, latest: []string{"app-x64.msi"}, in: user,
		},
		{
			name: "a portable app", old: []string{"app-win-x64.zip"}, latest: []string{"app-win-x64.zip"},
			in: catalog.Install{Method: install.MethodPortable, Folder: `C:\apps\app`},
		},
		{
			name: "a tool", old: []string{"tool-windows-x64.zip"}, latest: []string{"tool-windows-x64.zip"},
			in: catalog.Install{Method: install.MethodTool}, mutate: func(e *catalog.Entry) { e.Item.Kind = selection.ItemTool },
		},
		{
			name: "an unconfirmed link", old: []string{"app-x64.msi"}, latest: []string{"app-x64.msi"}, in: user,
			mutate: func(e *catalog.Entry) { e.Item.LinkGuessed = true }, want: LinkUnconfirmed,
		},
		{
			name: "a running portable app", old: []string{"app-win-x64.zip"}, latest: []string{"app-win-x64.zip"},
			in: catalog.Install{Method: install.MethodPortable, Folder: `C:\apps\app`}, running: true, want: ProgramRunning,
		},
		{
			name: "a running tool is not held back", old: []string{"tool-windows-x64.zip"}, latest: []string{"tool-windows-x64.zip"},
			in: catalog.Install{Method: install.MethodTool}, running: true, mutate: func(e *catalog.Entry) { e.Item.Kind = selection.ItemTool },
		},
		{
			name: "a stored choice that matches nothing in the new release", old: []string{"app-x64.msi"}, latest: []string{"app-x64-setup.exe"},
			in: user, want: NeedsAttention,
		},
		{
			name: "no stored choice", old: []string{"app-x64.msi"}, latest: []string{"app-x64.msi"}, in: user,
			mutate: func(e *catalog.Entry) { e.Intent = nil }, want: NeedsAttention,
		},
		{
			name: "an installer for everyone", old: []string{"app-x64.msi"}, latest: []string{"app-x64.msi"},
			in: catalog.Install{Method: install.MethodInstaller, Scope: platform.ScopeMachine, UninstallKey: "k"}, want: NeedsElevation,
		},
		{
			name: "an installer the system does not list", old: []string{"app-x64.msi"}, latest: []string{"app-x64.msi"},
			in: catalog.Install{Method: install.MethodInstaller}, want: NeedsElevation,
		},
		{
			name: "a portable app whose release is now a setup program", old: []string{"app-win-x64.zip"}, latest: []string{"app-win-x64.msi"},
			in: catalog.Install{Method: install.MethodPortable, Folder: `C:\apps\app`}, want: NeedsAttention,
		},
		{
			name: "a release that is not newer", old: []string{"app-x64.msi"}, latest: []string{"app-x64.msi"}, in: user,
			mutate: func(e *catalog.Entry) { e.Install.Tag = "v2.0.0" }, want: NotNewer,
		},
		{
			name: "an install this application did not make", old: []string{"app-x64.msi"}, latest: []string{"app-x64.msi"},
			in: catalog.Install{}, want: NotManaged,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := entryFor(t, "app", c.old, c.latest, c.in)
			if c.mutate != nil {
				c.mutate(&e)
			}
			if got := Judge(Facts{Entry: e, Running: c.running}); got != c.want {
				t.Errorf("Judge = %v, want %v", got, c.want)
			}
		})
	}
}

func TestOnlyARunningProgramAndAReleaseThatIsNotNewerAreNotAnnounced(t *testing.T) {
	for r := Eligible; r <= LicenseQuestion; r++ {
		want := r != ProgramRunning && r != NotNewer
		if got := r.Announce(); got != want {
			t.Errorf("%v.Announce() = %v, want %v", r, got, want)
		}
	}
}

func TestAnInstallerRunsQuietlyOnlyWhenItHasAWayTo(t *testing.T) {
	for _, c := range []struct {
		kind      selection.ArtifactKind
		framework install.Framework
		args      string
		want      bool
	}{
		{selection.ArtifactMSI, install.FrameworkUnknown, "", true},
		{selection.ArtifactMSIX, install.FrameworkUnknown, "", true},
		{selection.ArtifactSetupExe, install.FrameworkNSIS, "", true},
		{selection.ArtifactSetupExe, install.FrameworkInno, "", true},
		{selection.ArtifactSetupExe, install.FrameworkUnknown, "/quiet", true},
		{selection.ArtifactSetupExe, install.FrameworkUnknown, "", false},
		{selection.ArtifactStandaloneExe, install.FrameworkNSIS, "", false},
	} {
		if got := RunsQuietly(c.kind, c.framework, c.args); got != c.want {
			t.Errorf("RunsQuietly(%s, %q, %q) = %v, want %v", c.kind, c.framework, c.args, got, c.want)
		}
	}
}

func TestAVersionIsAnnouncedOnceAndHeldSkippedOrHiddenOnesNeverAre(t *testing.T) {
	user := catalog.Install{Method: install.MethodInstaller, Scope: platform.ScopeUser}
	fresh := entryFor(t, "fresh", []string{"app-x64.msi"}, []string{"app-x64.msi"}, user)
	told := entryFor(t, "told", []string{"app-x64.msi"}, []string{"app-x64.msi"}, user)
	told.Item.ID, told.Item.NotifiedVersion = 2, "v2.0.0"
	held := entryFor(t, "held", []string{"app-x64.msi"}, []string{"app-x64.msi"}, user)
	held.Item.ID, held.Item.HoldUpdates = 3, true
	skipped := entryFor(t, "skipped", []string{"app-x64.msi"}, []string{"app-x64.msi"}, user)
	skipped.Item.ID, skipped.Item.SkippedVersion = 4, "v2.0.0"
	told.Item.NotifiedVersion = "v2.0.0"
	// The version after the one announced is announced again.
	newer := entryFor(t, "newer", []string{"app-x64.msi"}, []string{"app-x64.msi"}, user)
	newer.Item.ID, newer.Item.NotifiedVersion = 5, "v1.5.0"
	upToDate := entryFor(t, "current", []string{"app-x64.msi"}, []string{"app-x64.msi"}, user)
	upToDate.Item.ID, upToDate.Install.Tag = 6, "v2.0.0"
	hidden := entryFor(t, "hidden", []string{"app-x64.msi"}, []string{"app-x64.msi"}, user)
	hidden.Item.ID, hidden.Item.Hidden = 7, true

	got := Unannounced([]catalog.Entry{fresh, told, held, skipped, newer, upToDate, hidden})
	repos := make([]string, len(got))
	for i, e := range got {
		repos[i] = e.Item.Repo
	}
	if len(repos) != 2 || repos[0] != "fresh" || repos[1] != "newer" {
		t.Errorf("unannounced = %v, want fresh and newer", repos)
	}
}
