package main

import (
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

// releaseWith is a release with exactly these files, each 2 MB.
func releaseWith(tag string, daysAfter int, files ...string) source.Release {
	r := release(tag, daysAfter, "")
	r.Assets = nil
	for _, name := range files {
		r.Assets = append(r.Assets, source.Asset{Name: name, Size: 2 << 20})
	}
	return r
}

func TestAnItemWithNothingForThisPCSaysSo(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", releaseWith("v1.0.0", 1, "tool-linux-x64.tar.gz", "tool_1.0.0_amd64.deb", "tool-arm64-setup.exe"))
	entry := e.track("tool", catalog.AddOptions{})
	tt := e.open()
	requireText(t, tt, "No download for this PC")
	requireNoText(t, tt, "v1.0.0 available")
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	requireText(t, tt, "None of the cached releases has a download for this PC.", "Download")
	// The drawer's file list needs a file; nothing to pick from.
	requireNoText(t, tt, "Will install:")
}

func TestAddDialogShowsTheFileThatWouldBeInstalledAndWhy(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "A tool", releaseWith("v1.1.0", 5, "tool-1.1.0-x64.msi", "tool-1.1.0-x64-setup.exe", "tool-1.1.0-arm64-setup.exe", "tool-1.1.0-x64.zip", "tool-1.1.0-x64-setup.exe.sha256"),
		releaseWith("v1.0.0", 1, "tool-1.0.0-x64-setup.exe"))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Will install: tool-1.1.0-x64-setup.exe · 2.0 MB", "Setup program built for x64.", "You prefer installers over portable downloads.",
		"Version", "File", "Automatic (best match for this PC)", "Install as", "Automatic")
	requireNoText(t, tt, "Several different downloads match", "no download for this PC")
}

func TestAddDialogFileAndVersionChoices(t *testing.T) {
	prefs := defaultPreferences()
	prefs.InstallAfterAdding = false // This test is about the choice that is kept, not about installing it.
	e := newEnv(t, prefs)
	e.source.publish("tool", "", releaseWith("v2.0.0", 5, "tool-linux-x64.tar.gz"),
		releaseWith("v1.1.0", 3, "tool-1.1.0-x64-setup.exe", "tool-1.1.0-win-x64.zip"),
		releaseWith("v1.0.0", 1, "tool-1.0.0-x64-setup.exe"))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	// The newest release with a download is selected, and the newest is marked.
	requireText(t, tt, "Latest version: v1.1.0", "Will install: tool-1.1.0-x64-setup.exe · 2.0 MB")

	click(t, tt, "Choose a version")
	requireText(t, tt, "v2.0.0 · no download for this PC")
	click(t, tt, "v1.0.0")
	requireText(t, tt, "Will install: tool-1.0.0-x64-setup.exe · 2.0 MB")

	click(t, tt, "Choose a version")
	click(t, tt, "v1.1.0")
	click(t, tt, "Choose a file")
	click(t, tt, "tool-1.1.0-win-x64.zip · 2.0 MB")
	requireText(t, tt, "Will install: tool-1.1.0-win-x64.zip · 2.0 MB", "You chose this file.")

	// Confirming keeps the choice for the next releases.
	click(t, tt, "Add")
	e.settle()
	entry, ok := e.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "tool"})
	if !ok || entry.Intent == nil || !entry.Intent.Explicit || entry.Intent.Kind != selection.ArtifactArchive {
		t.Fatalf("stored choice = %+v", entry.Intent)
	}
	if offer, _ := entry.LatestInstallable(); offer.Pick().Asset.Name != "tool-1.1.0-win-x64.zip" {
		t.Errorf("offered %q", offer.Pick().Asset.Name)
	}
}

func TestAddDialogWarnsOfNoDownloadAndAmbiguity(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("empty", "", releaseWith("v1.0.0", 1, "empty-linux.tar.gz"))
	e.source.publish("suite", "", releaseWith("v1.0.0", 1, "editor-x64-setup.exe", "viewer-x64-setup.exe"))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/empty")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "This release has no download for this PC. You can still track it.")
	requireNoText(t, tt, "Will install:")
	click(t, tt, "Add") // Tracking is still allowed.
	e.settle()
	requireText(t, tt, "No download for this PC")

	typeInto(t, tt, "Search or paste a repository link", "acme/suite")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Several different downloads match. Check the file.", "Will install: editor-x64-setup.exe")
}

func TestAddDialogInstallAsAndTheSwitchToAnApp(t *testing.T) {
	prefs := defaultPreferences()
	prefs.InstallAfterAdding = false // The release is an installer, which would now be installed.
	e := newEnv(t, prefs)
	e.source.publish("tool", "", releaseWith("v1.0.0", 1, "tool-x64.msi"))
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Will install: tool-x64.msi · 2.0 MB")
	requireNoText(t, tt, "Install it as an app?")
	click(t, tt, "Choose what to install this as")
	click(t, tt, "Command-line tool")
	requireText(t, tt, "This project only publishes an installer. Install it as an app?", "This release has no download for this PC.")
	click(t, tt, "Install as an app")
	requireText(t, tt, "Will install: tool-x64.msi · 2.0 MB")
	requireNoText(t, tt, "Install it as an app?")
	click(t, tt, "Add")
	e.settle()
	if entry, _ := e.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "tool"}); entry.Item.Kind != selection.ItemApp {
		t.Errorf("kind = %q", entry.Item.Kind)
	}
}

func TestDrawerShowsTheDownloadEachVersionAndTheNewestWithoutOne(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", releaseWith("v2.0.0", 5, "tool-linux-x64.tar.gz"),
		releaseWith("v1.1.0", 3, "tool-1.1.0-x64-setup.exe", "tool-1.1.0-win-x64.zip"),
		releaseWith("v1.0.0", 1, "tool-1.0.0-arm64-setup.exe"))
	entry := e.track("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	tt := e.open()
	tt.SetSize(windowWidth, 1600)
	requireText(t, tt, "v1.0.0 → v1.1.0", "Update available")
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	requireText(t, tt, "v2.0.0 has no download for this PC yet. v1.1.0 is the newest one that does.",
		"Will install: tool-1.1.0-x64-setup.exe · 2.0 MB", "Setup program built for x64.",
		"All versions", "No download for this PC", "tool-1.1.0-x64-setup.exe · 2.0 MB")
	// The arm64-only release has nothing for an x64 PC.
	if got := countText(tt, "No download for this PC"); got != 2 {
		t.Errorf("versions without a download: %d", got)
	}
}

func TestPickingAFileInTheDrawerKeepsItAndAutomaticClearsIt(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", releaseWith("v1.0.0", 1, "tool-1.0.0-x64-setup.exe", "tool-1.0.0-win-x64.zip"))
	entry := e.track("tool", catalog.AddOptions{})
	tt := e.open()
	tt.SetSize(windowWidth, 1600)
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	click(t, tt, "Choose a file")
	click(t, tt, "tool-1.0.0-win-x64.zip · 2.0 MB")
	e.settle()
	requireText(t, tt, "Will install: tool-1.0.0-win-x64.zip · 2.0 MB", "You chose this file.")
	if got, _ := e.screen.catalog.Get(entry.Item.ID); got.Intent == nil || got.Intent.Kind != selection.ArtifactArchive {
		t.Fatalf("intent = %+v", got.Intent)
	}
	messages := messages(e)
	if len(messages) == 0 || messages[0] != "Chose tool-1.0.0-win-x64.zip for acme/tool" {
		t.Errorf("activity = %q", messages)
	}

	click(t, tt, "Choose a file")
	click(t, tt, "Automatic (best match for this PC)")
	e.settle()
	requireText(t, tt, "Will install: tool-1.0.0-x64-setup.exe · 2.0 MB")
	if got, _ := e.screen.catalog.Get(entry.Item.ID); got.Intent != nil {
		t.Errorf("Automatic kept %+v", got.Intent)
	}
}

func TestAFileThatNoLongerExistsNeedsAttention(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", releaseWith("v1.0.0", 1, "tool-1.0.0-x64-setup.exe", "tool-1.0.0-win-x64.zip"))
	entry := e.track("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.0.0"})
	chosen := selection.IntentOf(entry.Resolve(entry.Releases[0]).Candidates[1], true)
	if err := e.screen.catalog.SetIntent(t.Context(), entry.Item.ID, &chosen); err != nil {
		t.Fatal(err)
	}
	e.source.publish("tool", "", releaseWith("v1.1.0", 2, "tool-1.1.0-x64-setup.exe"), releaseWith("v1.0.0", 1, "tool-1.0.0-x64-setup.exe", "tool-1.0.0-win-x64.zip"))
	tt := e.open()
	tt.SetSize(windowWidth, 1600)
	e.screen.checkItem(entry.Item.ID, true)
	e.settle()
	requireText(t, tt, "Needs attention")
	requireNoText(t, tt, "Update available")
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	requireText(t, tt, "The download for tool changed in v1.1.0. Check the file before updating.", "Will install: tool-1.1.0-x64-setup.exe · 2.0 MB")
}

func TestDrawerKindAndMethodChoices(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", releaseWith("v1.0.0", 1, "tool-1.0.0-x64-setup.exe", "tool-1.0.0-win-x64.zip"))
	entry := e.track("tool", catalog.AddOptions{})
	tt := e.open()
	tt.SetSize(windowWidth, 1600)
	e.screen.openItem(entry.Item.ID)
	tt.Frame()
	requireText(t, tt, "Install method", "Install as")

	click(t, tt, "Choose the install method")
	click(t, tt, "Portable")
	e.settle()
	requireText(t, tt, "Will install: tool-1.0.0-win-x64.zip · 2.0 MB")
	if got, _ := e.screen.catalog.Get(entry.Item.ID); got.Item.Method != selection.MethodPortable {
		t.Errorf("method = %q", got.Item.Method)
	}

	// A tool has no install method.
	click(t, tt, "Choose what to install this as")
	click(t, tt, "Command-line tool")
	e.settle()
	requireNoText(t, tt, "Install method")
	if got, _ := e.screen.catalog.Get(entry.Item.ID); got.Item.Kind != selection.ItemTool {
		t.Errorf("kind = %q", got.Item.Kind)
	}
}

func TestInstallationSettingsChangeTheChosenFile(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "", releaseWith("v1.0.0", 1, "tool-1.0.0-x64-setup.exe", "tool-1.0.0-arm64-setup.exe", "tool-1.0.0-win-x64-portable.zip", "tool-1.0.0-win-arm64-portable.zip"))
	entry := e.track("tool", catalog.AddOptions{})
	file := func() string {
		got, _ := e.screen.catalog.Get(entry.Item.ID)
		offer, _ := got.LatestInstallable()
		return offer.Pick().Asset.Name
	}
	arch := machineArchitecture()
	want := map[string]string{selection.ArchX64: "tool-1.0.0-x64-setup.exe", selection.ArchARM64: "tool-1.0.0-arm64-setup.exe"}
	if got := file(); got != want[arch] {
		t.Fatalf("on %s: %s", arch, got)
	}

	tt := e.open()
	tt.SetSize(windowWidth, 2400)
	click(t, tt, "Settings")
	click(t, tt, "Installation")
	requireText(t, tt, "Preferred architecture", "Prefer installers over portable archives", "Auto")
	other := selection.ArchARM64
	label := "ARM64"
	if arch == selection.ArchARM64 {
		other, label = selection.ArchX64, "x64"
	}
	click(t, tt, "Choose the preferred architecture")
	click(t, tt, label)
	e.settle()
	if got := e.screen.prefs.Get().PreferredArchitecture; got != other {
		t.Fatalf("architecture = %q", got)
	}
	if got := file(); got != want[other] {
		t.Errorf("on %s: %s", other, got)
	}

	click(t, tt, "Toggle preferring installers over portable archives")
	e.settle()
	if e.screen.prefs.Get().PreferInstallers {
		t.Error("the switch did not turn the preference off")
	}
	if got := file(); got != "tool-1.0.0-win-"+other+"-portable.zip" {
		t.Errorf("preferring portable: %s", got)
	}
}

func TestMachinePolicy(t *testing.T) {
	p := defaultPreferences()
	got := machinePolicy(p)
	if got.Target.OS != selection.OSWindows || got.Target.Arch != machineArchitecture() || !got.PreferInstallers {
		t.Errorf("auto = %+v", got)
	}
	p.PreferredArchitecture, p.PreferInstallers = selection.ArchX86, false
	got = machinePolicy(p)
	if got.Target.Arch != selection.ArchX86 || got.PreferInstallers {
		t.Errorf("explicit = %+v", got)
	}
}

func TestFormatSize(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 900: "900 B", 1536: "1.5 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB"} {
		if got := formatSize(in); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", in, got, want)
		}
	}
}
