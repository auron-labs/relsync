package main

import (
	"log"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// machinePolicy is how downloads are chosen on this PC under the settings.
// The application installs Windows software; other systems arrive later.
func machinePolicy(p preferences) selection.Policy {
	arch := p.PreferredArchitecture
	if arch == archAuto {
		arch = machineArchitecture()
	}
	return selection.Policy{
		Target:           selection.Target{OS: selection.OSWindows, Arch: arch},
		PreferInstallers: p.PreferInstallers, InstallForEveryone: p.InstallForEveryone,
	}
}

func machineArchitecture() string {
	switch runtime.GOARCH {
	case "arm64":
		return selection.ArchARM64
	case "386":
		return selection.ArchX86
	}
	return selection.ArchX64
}

// downloadView is one release of an Item and the file chosen from it.
type downloadView struct {
	entry   catalog.Entry
	release source.Release
	result  selection.Result
	// chosen is the name of the file the user picked, empty for the best match.
	chosen string
	// pick is the file that would be installed, if there is one.
	pick    selection.Candidate
	hasPick bool
}

// newDownloadView resolves a release. chosen names a file picked by the user,
// which stands in for the best match while it is one of the release's files.
func newDownloadView(e catalog.Entry, r source.Release, chosen string) downloadView {
	v := downloadView{entry: e, release: r, result: e.Resolve(r)}
	v.pick, v.hasPick = v.result.Pick()
	if i := slices.IndexFunc(v.result.Candidates, func(c selection.Candidate) bool { return c.Asset.Name == chosen }); i >= 0 {
		v.chosen, v.pick, v.hasPick = chosen, v.result.Candidates[i], true
	}
	return v
}

// storedChoice is the file the Item's stored choice names in a resolution, or
// "" when the best match is taken.
func storedChoice(e catalog.Entry, result selection.Result) string {
	if e.Intent == nil || !e.Intent.Explicit {
		return ""
	}
	for _, c := range result.Candidates {
		if c.MatchesIntent {
			return c.Asset.Name
		}
	}
	return ""
}

// option is one choice in a drop-down.
type option struct{ value, label string }

// dropdown is a labelled drop-down over options. It reports the value chosen
// and whether it differs from current.
func dropdown(c *ui.Context, label, current string, options []option) (choice string, changed bool) {
	choice = current
	shown := ""
	if i := slices.IndexFunc(options, func(o option) bool { return o.value == current }); i >= 0 {
		shown = options[i].label
	}
	sel := components.Select(c, &choice)
	sel.Trigger.Label(label).FillWidth().Children(func() {
		components.Text(c, shown).SingleLine().Ellipsis("…")
	})
	sel.Popup(func(ui.Element) {
		for _, o := range options {
			sel.Item(o.value).Children(func() { components.Text(c, o.label).SingleLine().Ellipsis("…") })
		}
	})
	return choice, choice != current
}

// labelled stacks a small caption over a control.
func labelled(c *ui.Context, caption string, control func()) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(t.Space(1)).Children(func() {
		components.Text(c, caption).FontSize(t.Rem(.85)).TextColor(t.TextMuted)
		control()
	})
}

// fileOptions lists "Automatic" and then every file that could be installed.
func fileOptions(v downloadView) []option {
	options := make([]option, 1, 1+len(v.result.Candidates))
	options[0] = option{"", locales.DownloadAutomatic()}
	for _, cand := range v.result.Candidates {
		options = append(options, option{cand.Asset.Name, locales.DownloadFileOption(cand.Asset.Name, formatSize(cand.Asset.Size))})
	}
	return options
}

// fileChooser is the drop-down of files; it reports the name chosen, "" for
// Automatic.
func fileChooser(c *ui.Context, v downloadView) (name string, changed bool) {
	var choice string
	labelled(c, locales.DownloadFile(), func() {
		choice, changed = dropdown(c, locales.ChooseFile(), v.chosen, fileOptions(v))
	})
	return choice, changed
}

// kindChooser is the "Install as" drop-down.
func kindChooser(c *ui.Context, current selection.ItemKind) (kind selection.ItemKind, changed bool) {
	var choice string
	labelled(c, locales.InstallAs(), func() {
		choice, changed = dropdown(c, locales.ChooseInstallAs(), string(current), []option{
			{"", locales.InstallAsAutomatic()},
			{string(selection.ItemApp), locales.InstallAsApp()},
			{string(selection.ItemTool), locales.InstallAsTool()},
		})
	})
	return selection.ItemKind(choice), changed
}

// methodChooser is the "Install method" drop-down.
func methodChooser(c *ui.Context, current selection.Method) (method selection.Method, changed bool) {
	var choice string
	labelled(c, locales.InstallMethod(), func() {
		choice, changed = dropdown(c, locales.ChooseInstallMethod(), string(current), []option{
			{string(selection.MethodAutomatic), locales.MethodAutomatic()},
			{string(selection.MethodInstaller), locales.MethodInstaller()},
			{string(selection.MethodPortable), locales.MethodPortable()},
		})
	})
	return selection.Method(choice), changed
}

// downloadNotes says which file would be installed and why, and warns of
// anything the user should check. none is the warning for a release with no
// download; onApp makes the Item an App when a Tool has only an installer.
func downloadNotes(c *ui.Context, v downloadView, none string, onApp func()) {
	t := c.Theme()
	warn := func(text string) { components.Text(c, text).TextColor(t.Warning).Wrap() }
	if v.hasPick {
		components.Text(c, locales.DownloadWillInstall(v.pick.Asset.Name, formatSize(v.pick.Asset.Size))).Bold().Wrap()
		components.Text(c, whyText(v)).TextColor(t.TextMuted).FontSize(t.Rem(.85)).Wrap()
	} else {
		warn(none)
	}
	if v.result.OfferApp {
		warn(locales.DownloadOfferApp())
		if components.Button(c, locales.DownloadOfferAppAction(), components.ButtonSecondary).Clicked() {
			onApp()
		}
	}
	if v.hasPick && v.result.Ambiguous && v.chosen == "" {
		warn(locales.DownloadAmbiguous())
	}
	if v.result.IntentBroken && v.chosen == "" {
		warn(locales.DownloadChanged(v.entry.Title(), v.release.Tag))
	}
}

// whyText explains the file that would be installed.
func whyText(v downloadView) string {
	if v.chosen != "" {
		return locales.WhyChosen()
	}
	kind := artifactName(v.pick.Kind)
	var text string
	switch {
	case len(v.pick.Arch) == 0:
		text = locales.WhyUnstated(kind)
	case v.pick.Emulated:
		text = locales.WhyEmulated(kind, archNames(v.pick.Arch))
	default:
		text = locales.WhyNative(kind, archNames(v.pick.Arch))
	}
	if v.entry.KindFor(v.release) == selection.ItemTool || !offersBoth(v.result) {
		return text
	}
	if v.entry.Policy.PreferInstallers {
		return text + " " + locales.WhyPrefersInstaller()
	}
	return text + " " + locales.WhyPrefersPortable()
}

// offersBoth reports installers and portable downloads among the candidates.
func offersBoth(r selection.Result) bool {
	var installer, portable bool
	for _, cand := range r.Candidates {
		if cand.ByHand {
			continue
		}
		installer, portable = installer || cand.Kind.IsInstaller(), portable || !cand.Kind.IsInstaller()
	}
	return installer && portable
}

func artifactName(kind selection.ArtifactKind) string {
	switch kind {
	case selection.ArtifactMSI:
		return locales.ArtifactMsi()
	case selection.ArtifactMSIX:
		return locales.ArtifactMsix()
	case selection.ArtifactSetupExe:
		return locales.ArtifactSetupExe()
	case selection.ArtifactStandaloneExe:
		return locales.ArtifactStandaloneExe()
	case selection.ArtifactArchive:
	}
	return locales.ArtifactArchive()
}

func archNames(archs []string) string {
	names := make([]string, len(archs))
	for i, a := range archs {
		switch a {
		case selection.ArchARM64:
			names[i] = locales.ArchitectureArm64()
		case selection.ArchX64:
			names[i] = locales.ArchitectureX64()
		case selection.ArchX86:
			names[i] = locales.ArchitectureX86()
		default:
			names[i] = a
		}
	}
	return strings.Join(names, ", ")
}

// formatSize writes a file size for people.
func formatSize(n int64) string {
	oneDecimal := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	switch {
	case n >= 1<<30:
		return locales.SizeGB(oneDecimal(float64(n) / (1 << 30)))
	case n >= 1<<20:
		return locales.SizeMB(oneDecimal(float64(n) / (1 << 20)))
	case n >= 1<<10:
		return locales.SizeKB(oneDecimal(float64(n) / (1 << 10)))
	}
	return locales.SizeBytes(strconv.FormatInt(n, 10))
}

// logSelection writes the decision record for the file an Item would install
// to the log, for diagnosing a surprising choice.
func (s *screen) logSelection(id int64) {
	e, ok := s.catalog.Get(id)
	if !ok {
		return
	}
	offer, found := e.LatestInstallable()
	if !found {
		log.Printf("selection for %s: no download for this PC", e.FullName())
		return
	}
	log.Printf("selection for %s in %s:\n%s", e.FullName(), offer.Release.Tag, offer.Result.Explain())
}
