package selection

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

var (
	x64Windows   = Policy{Target: Target{OS: OSWindows, Arch: ArchX64}, PreferInstallers: true}
	arm64Windows = Policy{Target: Target{OS: OSWindows, Arch: ArchARM64}, PreferInstallers: true}
	x86Windows   = Policy{Target: Target{OS: OSWindows, Arch: ArchX86}, PreferInstallers: true}
)

func portable(p Policy) Policy {
	p.PreferInstallers = false
	return p
}

func assets(names ...string) []Asset {
	list := make([]Asset, len(names))
	for i, n := range names {
		list[i] = Asset{Name: n, Size: int64(1000 + i)}
	}
	return list
}

func resolveNames(policy Policy, repo string, kind ItemKind, names ...string) Result {
	return Resolve(Request{Assets: assets(names...), Repo: repo, Kind: kind, Policy: policy})
}

func pickName(t *testing.T, r Result) string {
	t.Helper()
	c, ok := r.Pick()
	if !ok {
		return ""
	}
	return c.Asset.Name
}

func TestEligibilityComesBeforePreference(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		kind   ItemKind
		files  []string
		want   string
		rule   Rule // the rule that excludes the first file, when nothing is picked
	}{
		{"an arm64 build is never chosen on x64", x64Windows, ItemApp, []string{"app-arm64-setup.exe"}, "", RuleArchitecture},
		{"the x64 build is chosen over an arm64 one", x64Windows, ItemApp, []string{"app-arm64-setup.exe", "app-x64-setup.exe"}, "app-x64-setup.exe", ""},
		{"a Linux-only release yields nothing", x64Windows, ItemApp, []string{"app-linux-x64.tar.gz", "app-x86_64.AppImage", "app_1.0_amd64.deb"}, "", RuleOS},
		{"a macOS archive is ineligible", x64Windows, ItemTool, []string{"tool-darwin-amd64.tar.gz"}, "", RuleOS},
		{"a wrong-OS file never wins by name", x64Windows, ItemApp, []string{"app-linux-x64-setup.exe", "app.zip"}, "app.zip", ""},
		{"x86 runs on x64 under emulation", x64Windows, ItemApp, []string{"app-x86-setup.exe"}, "app-x86-setup.exe", ""},
		{"x64 runs on arm64 under emulation", arm64Windows, ItemApp, []string{"app-x64-setup.exe"}, "app-x64-setup.exe", ""},
		{"x86 runs on arm64 under emulation", arm64Windows, ItemApp, []string{"app-x86-setup.exe"}, "app-x86-setup.exe", ""},
		{"x64 does not run on x86", x86Windows, ItemApp, []string{"app-x64-setup.exe"}, "", RuleArchitecture},
		{"arm64 does not run on x86", x86Windows, ItemApp, []string{"app-arm64-setup.exe"}, "", RuleArchitecture},
		{"a 32-bit ARM build is ineligible", x64Windows, ItemApp, []string{"app-armv7-setup.exe"}, "", RuleArchitecture},
		{"a file naming no architecture is eligible", x64Windows, ItemApp, []string{"app-setup.exe"}, "app-setup.exe", ""},
		{"a Tool accepts a standalone program", x64Windows, ItemTool, []string{"tool-windows-amd64.exe"}, "tool-windows-amd64.exe", ""},
		{"a Tool does not accept an MSI", x64Windows, ItemTool, []string{"tool-x64.msi"}, "", RuleKind},
		{"a Tool does not accept MSIX", x64Windows, ItemTool, []string{"tool-x64.msix"}, "", RuleKind},
		{"a CPU-specific build is never chosen alone", x64Windows, ItemTool, []string{"tool-windows-x64-avx2.zip"}, "", RuleCPUVariant},
		{"a CPU-specific build loses to the baseline", x64Windows, ItemTool, []string{"tool-windows-x64-avx2.zip", "tool-windows-x64.zip"}, "tool-windows-x64.zip", ""},
		{"debug files and metadata are never candidates", x64Windows, ItemApp, []string{"app-x64-symbols.zip", "app-x64.zip.sha256"}, "", ""},
	}
	for _, tt := range tests {
		res := resolveNames(tt.policy, "app", tt.kind, tt.files...)
		if got := pickName(t, res); got != tt.want {
			t.Errorf("%s: picked %q, want %q\n%s", tt.name, got, tt.want, res.Explain())
		}
		if tt.want == "" && tt.rule != "" && res.Decisions[0].Excluded != tt.rule {
			t.Errorf("%s: first file excluded by %q, want %q", tt.name, res.Decisions[0].Excluded, tt.rule)
		}
	}
}

func TestEmulatedBuildsRankAfterNativeOnes(t *testing.T) {
	res := resolveNames(arm64Windows, "app", ItemApp, "app-x64-setup.exe", "app-arm64.zip", "app-arm64-setup.exe", "app-x86-setup.exe")
	order := make([]string, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		order = append(order, c.Asset.Name)
	}
	want := []string{"app-arm64-setup.exe", "app-arm64.zip", "app-x64-setup.exe", "app-x86-setup.exe"}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if res.Candidates[0].Emulated || !res.Candidates[2].Emulated {
		t.Errorf("emulated flags = %v %v", res.Candidates[0].Emulated, res.Candidates[2].Emulated)
	}
}

func TestTheClosestEmulatedArchitectureRanksFirst(t *testing.T) {
	for _, tt := range []struct {
		policy Policy
		want   string
	}{
		{arm64Windows, "tool-1.0-x64.zip"}, // Both run under emulation; x64 is closer than the shorter i686.
		{x64Windows, "tool-1.0-i686.zip"},  // Only x86 runs on x64.
	} {
		names := []string{"tool-1.0-i686.zip", "tool-1.0-x64.zip"}
		if tt.policy == x64Windows {
			names = []string{"tool-1.0-i686.zip", "tool-1.0-arm64.zip"}
		}
		if got := pickName(t, resolveNames(tt.policy, "tool", ItemTool, names...)); got != tt.want {
			t.Errorf("%v: picked %q, want %q", tt.policy.Target, got, tt.want)
		}
	}
}

func TestInstallerOrPortablePreference(t *testing.T) {
	files := []string{"app-1.0-x64.msi", "app-1.0-x64-setup.exe", "app-1.0-win-x64-portable.zip", "app-1.0-x64.msix"}
	tests := []struct {
		name   string
		policy Policy
		files  []string
		want   string
	}{
		{"installers are preferred by default", x64Windows, files, "app-1.0-x64-setup.exe"},
		{"an MSI beats MSIX", x64Windows, []string{files[0], files[3]}, "app-1.0-x64.msi"},
		{"an MSI beats a setup program that does not say so", x64Windows, []string{files[0], "app-1.0-x64.exe"}, "app-1.0-x64.msi"},
		{"portable is preferred when asked", portable(x64Windows), files, "app-1.0-win-x64-portable.zip"},
		{"installers are used when portable is preferred but missing", portable(x64Windows), files[:2], "app-1.0-x64.msi"},
		{"portable is used when no installer exists", x64Windows, files[2:3], "app-1.0-win-x64-portable.zip"},
	}
	for _, tt := range tests {
		if got := pickName(t, resolveNames(tt.policy, "app", ItemApp, tt.files...)); got != tt.want {
			t.Errorf("%s: picked %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestInstallMethodNarrowsTheCandidates(t *testing.T) {
	files := assets("app-x64.msi", "app-x64-setup.exe", "app-x64-portable.zip", "app-x64-portable.exe")
	for _, tt := range []struct {
		method Method
		want   string
	}{
		{MethodInstaller, "app-x64-setup.exe"},
		{MethodPortable, "app-x64-portable.exe"}, // Level on score, the name decides.
	} {
		res := Resolve(Request{Assets: files, Repo: "app", Policy: x64Windows, Method: tt.method})
		if got := pickName(t, res); got != tt.want {
			t.Errorf("%s picked %q, want %q", tt.method, got, tt.want)
		}
		for _, c := range res.Candidates {
			if (tt.method == MethodInstaller) != c.Kind.IsInstaller() {
				t.Errorf("%s accepted %s (%s)", tt.method, c.Asset.Name, c.Kind)
			}
		}
	}
	// The method cannot bring back a file that cannot run here.
	res := Resolve(Request{Assets: assets("app-arm64-setup.exe"), Repo: "app", Policy: x64Windows, Method: MethodInstaller})
	if len(res.Candidates) != 0 {
		t.Errorf("candidates = %v", res.Candidates)
	}
}

func TestInstallForEveryoneChoosesTheMatchingScope(t *testing.T) {
	files := assets("app-1.0-x64-user-setup.exe", "app-1.0-x64-system-setup.exe")
	user := Resolve(Request{Assets: files, Repo: "app", Policy: x64Windows})
	everyone := Resolve(Request{Assets: files, Repo: "app", Policy: Policy{Target: x64Windows.Target, PreferInstallers: true, InstallForEveryone: true}})
	if pickName(t, user) != "app-1.0-x64-user-setup.exe" || pickName(t, everyone) != "app-1.0-x64-system-setup.exe" {
		t.Errorf("user %q, everyone %q", pickName(t, user), pickName(t, everyone))
	}
}

func TestToolScoring(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{"a standalone program beats an archive", []string{"tool-windows-amd64.zip", "tool-windows-amd64.exe"}, "tool-windows-amd64.exe"},
		{"a stated operating system beats none", []string{"tool-amd64.zip", "tool-windows-amd64.zip"}, "tool-windows-amd64.zip"},
		{"msvc beats gnu", []string{"tool-x86_64-pc-windows-gnu.zip", "tool-x86_64-pc-windows-msvc.zip"}, "tool-x86_64-pc-windows-msvc.zip"},
		{"the plain build beats a variant", []string{"tool-windows-amd64-nogpu.zip", "tool-windows-amd64.zip"}, "tool-windows-amd64.zip"},
	}
	for _, tt := range tests {
		if got := pickName(t, resolveNames(x64Windows, "tool", ItemTool, tt.files...)); got != tt.want {
			t.Errorf("%s: picked %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTieBreakersAreDeterministic(t *testing.T) {
	names := []string{"app-x64.zip", "app-x64.7z", "app-x64-b.zip", "app-x64-a.zip", "app-win-x64.zip"}
	first := resolveNames(portable(x64Windows), "app", ItemApp, names...)
	want := make([]string, 0, len(first.Candidates))
	for _, c := range first.Candidates {
		want = append(want, c.Asset.Name)
	}
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // A fixed seed makes the shuffles repeatable.
	for range 50 {
		shuffled := slices.Clone(names)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		shuffledResult := resolveNames(portable(x64Windows), "app", ItemApp, shuffled...)
		got := make([]string, 0, len(shuffledResult.Candidates))
		for _, c := range shuffledResult.Candidates {
			got = append(got, c.Asset.Name)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("order depends on the input: %v then %v", want, got)
		}
	}
	// More stated facts first, then the shorter name, then the name itself.
	if !slices.Equal(want, []string{"app-win-x64.zip", "app-x64.7z", "app-x64.zip", "app-x64-a.zip", "app-x64-b.zip"}) {
		t.Errorf("order = %v", want)
	}
}

func TestTheProductMeantIsChosenAndOthersAreAmbiguous(t *testing.T) {
	res := resolveNames(x64Windows, "tool", ItemApp, "helper-x64-setup.exe", "tool-x64.zip", "tool-x64-setup.exe")
	if got := pickName(t, res); got != "tool-x64-setup.exe" || res.Ambiguous {
		t.Errorf("picked %q, ambiguous %v", got, res.Ambiguous)
	}
	res = resolveNames(x64Windows, "suite", ItemApp, "editor-x64-setup.exe", "viewer-x64-setup.exe")
	if _, ok := res.Pick(); !ok || !res.Ambiguous {
		t.Errorf("several products, none the repository: pick %q ambiguous %v", pickName(t, res), res.Ambiguous)
	}
	if res = resolveNames(x64Windows, "suite", ItemApp, "editor-x64-setup.exe"); res.Ambiguous {
		t.Error("one product is not ambiguous")
	}
	if res = resolveNames(x64Windows, "App-Name", ItemApp, "app.name-x64-setup.exe"); res.Ambiguous || res.Candidates[0].Signals == nil {
		t.Errorf("product match ignores separators and case: %+v", res)
	}
}

func TestUnrecognisedVariantsAreAmbiguous(t *testing.T) {
	res := resolveNames(x64Windows, "ffmpeg", ItemTool, "ffmpeg-release-full.7z", "ffmpeg-release-essentials.zip", "ffmpeg-release-full.zip")
	if _, ok := res.Pick(); !ok || !res.Ambiguous {
		t.Errorf("full against essentials: pick %q ambiguous %v", pickName(t, res), res.Ambiguous)
	}
	res = resolveNames(x64Windows, "tool", ItemTool, "tool-windows-x64.zip", "tool-windows-x64.7z")
	if res.Ambiguous {
		t.Error("two packagings of one build are not ambiguous")
	}
}

func TestNothingToInstall(t *testing.T) {
	for _, files := range [][]string{nil, {"checksums.txt", "app-1.0.tar.gz.sig"}, {"source.zip"}} {
		res := resolveNames(x64Windows, "app", ItemApp, files...)
		if _, ok := res.Pick(); ok || res.Ambiguous || res.IntentBroken {
			t.Errorf("%v: %+v", files, res)
		}
	}
}

func TestAToolWithOnlyAnInstallerOffersTheSwitch(t *testing.T) {
	res := resolveNames(x64Windows, "tool", ItemTool, "tool-x64.msi", "tool-linux-amd64.tar.gz")
	if _, ok := res.Pick(); ok || !res.OfferApp {
		t.Errorf("pick %q, offer app %v", pickName(t, res), res.OfferApp)
	}
	res = resolveNames(x64Windows, "tool", ItemTool, "tool-linux-amd64.tar.gz")
	if res.OfferApp {
		t.Error("nothing installable as an app either")
	}
	if res = resolveNames(x64Windows, "tool", ItemApp, "tool-x64.msi"); res.OfferApp {
		t.Error("an app has no switch to offer")
	}
}

func TestIntentKeepsTheSameKindOfFile(t *testing.T) {
	v1 := resolveNames(x64Windows, "app", ItemApp, "app-1.0-x64.msi", "app-1.0-x64-setup.exe", "app-1.0-x64-portable.zip")
	if got := pickName(t, v1); got != "app-1.0-x64-setup.exe" {
		t.Fatalf("picked %s", got)
	}
	i := slices.IndexFunc(v1.Candidates, func(c Candidate) bool { return c.Kind == ArtifactArchive })
	intent := IntentOf(v1.Candidates[i], true)

	// The same kind of file in the next release, named differently.
	v2 := Resolve(Request{Assets: assets("app-1.1-x64.msi", "App_1.1_x64_Setup.exe", "App-1.1-x64-Portable.zip"), Repo: "app", Policy: x64Windows, Intent: &intent})
	if got := pickName(t, v2); got != "App-1.1-x64-Portable.zip" || v2.IntentBroken || !v2.Candidates[0].MatchesIntent {
		t.Errorf("picked %q, broken %v", got, v2.IntentBroken)
	}

	// The portable archive is gone: the best match is offered, not trusted.
	v3 := Resolve(Request{Assets: assets("app-1.2-x64.msi", "app-1.2-x64-setup.exe"), Repo: "app", Policy: x64Windows, Intent: &intent})
	if got := pickName(t, v3); got != "app-1.2-x64-setup.exe" || !v3.IntentBroken {
		t.Errorf("picked %q, broken %v", got, v3.IntentBroken)
	}

	// A different architecture is a different file.
	v4 := Resolve(Request{Assets: assets("app-1.3-arm64-portable.zip", "app-1.3-x64.msi"), Repo: "app", Policy: x64Windows, Intent: &intent})
	if !v4.IntentBroken || pickName(t, v4) != "app-1.3-x64.msi" {
		t.Errorf("picked %q, broken %v", pickName(t, v4), v4.IntentBroken)
	}

	// No intent, nothing to break.
	if v5 := Resolve(Request{Assets: assets("app-1.2-x64.msi"), Repo: "app", Policy: x64Windows}); v5.IntentBroken {
		t.Error("broken without an intent")
	}
}

func TestIntentIgnoresReleaseSpecificWords(t *testing.T) {
	v1 := resolveNames(x64Windows, "app", ItemApp, "app-v1.0.0-beta.1-x64-installer.exe")
	pick, _ := v1.Pick()
	intent := IntentOf(pick, false)
	v2 := Resolve(Request{Assets: assets("App_2.4.1_x64_setup.exe"), Repo: "app", Policy: x64Windows, Intent: &intent})
	if v2.IntentBroken {
		t.Errorf("version and pre-release words made the intent unresolvable: %+v", intent)
	}
}

func TestACPUSpecificBuildCanBePickedByHand(t *testing.T) {
	files := assets("tool-windows-x64.zip", "tool-windows-x64-avx2.zip")
	res := Resolve(Request{Assets: files, Repo: "tool", Kind: ItemTool, Policy: x64Windows})
	if got := pickName(t, res); got != "tool-windows-x64.zip" || len(res.Candidates) != 2 || !res.Candidates[1].ByHand {
		t.Fatalf("picked %q of %+v", got, res.Candidates)
	}
	intent := IntentOf(res.Candidates[1], true)
	res = Resolve(Request{Assets: files, Repo: "tool", Kind: ItemTool, Policy: x64Windows, Intent: &intent})
	if got := pickName(t, res); got != "tool-windows-x64-avx2.zip" || res.IntentBroken {
		t.Errorf("by-hand pick = %q, broken %v", got, res.IntentBroken)
	}
	// Offered, but never alone.
	res = Resolve(Request{Assets: assets("tool-windows-x64-avx2.zip"), Repo: "tool", Kind: ItemTool, Policy: x64Windows})
	if _, ok := res.Pick(); ok || len(res.Candidates) != 1 {
		t.Errorf("%+v", res)
	}
}

func TestTheDecisionRecordCoversEveryFile(t *testing.T) {
	res := resolveNames(x64Windows, "app", ItemApp, "app-x64-setup.exe", "app-x64-setup.exe.sha256", "app-arm64-setup.exe", "notes.txt", "app-linux.deb")
	want := []struct {
		class    Class
		excluded Rule
		chosen   bool
	}{
		{ClassCandidate, RuleNone, true},
		{ClassMetadata, RuleNone, false},
		{ClassCandidate, RuleArchitecture, false},
		{ClassOther, RuleNone, false},
		{ClassForeignPackage, RuleNone, false},
	}
	for i, w := range want {
		d := res.Decisions[i]
		if d.Class != w.class || d.Excluded != w.excluded || d.Chosen != w.chosen {
			t.Errorf("decision %d = %+v, want %+v", i, d, w)
		}
	}
	if d := res.Decisions[0]; d.Score != 95 || len(d.Signals) != 5 {
		t.Errorf("score %d from %v", d.Score, d.Signals)
	}
	explained := res.Explain()
	for _, part := range []string{"app-x64-setup.exe: candidate setup-exe, score 95", ", chosen", "app-arm64-setup.exe: candidate setup-exe, excluded by architecture", "app-x64-setup.exe.sha256: metadata"} {
		if !strings.Contains(explained, part) {
			t.Errorf("record lacks %q:\n%s", part, explained)
		}
	}
}

func TestGuessKind(t *testing.T) {
	tests := []struct {
		name   string
		files  []string
		topics []string
		repo   string
		want   ItemKind
	}{
		{"an installer means an app", []string{"tool-x64.msi", "tool-windows-amd64.zip"}, []string{"cli"}, "tool", ItemApp},
		{"a named setup program means an app", []string{"Tool-Setup.exe"}, nil, "tool", ItemApp},
		{"cli topic", []string{"tool.zip"}, []string{"go", "cli"}, "tool", ItemTool},
		{"command-line topic", []string{"tool.zip"}, []string{"command-line-tool"}, "tool", ItemTool},
		{"tui topic", []string{"tool.zip"}, []string{"tui"}, "tool", ItemTool},
		{"desktop topic", []string{"tool.zip"}, []string{"electron"}, "tool", ItemApp},
		{"compiler target triples", []string{"tool-x86_64-pc-windows-msvc.zip", "tool-aarch64-apple-darwin.tar.gz"}, nil, "tool", ItemTool},
		{"a bare executable", []string{"Tool.exe", "tool-linux"}, nil, "tool", ItemTool},
		{"an executable named otherwise is not bare", []string{"tool-desktop.exe"}, nil, "tool", ItemApp},
		{"nothing to go on", []string{"tool-1.0.zip"}, nil, "tool", ItemApp},
		{"no files", nil, nil, "tool", ItemApp},
	}
	for _, tt := range tests {
		if got := GuessKind(assets(tt.files...), tt.topics, tt.repo); got != tt.want {
			t.Errorf("%s: guessed %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestHasDownload(t *testing.T) {
	tests := []struct {
		name     string
		releases [][]Asset
		want     bool
	}{
		{"an installer", [][]Asset{assets("app-x64-setup.exe")}, true},
		{"a program", [][]Asset{assets("tool.exe")}, true},
		{"an archive that names Windows", [][]Asset{assets("tool-win-x64.zip")}, true},
		{"an archive that names no system", [][]Asset{assets("tool-1.0.zip", "tool-1.0.tar.gz")}, false},
		{"only another system", [][]Asset{assets("tool-linux-x64.tar.gz", "tool.dmg")}, false},
		{"only another architecture", [][]Asset{assets("tool-arm64-setup.exe")}, false},
		{"a CPU-specific build alone", [][]Asset{assets("tool-win-x64-avx2.zip")}, false},
		{"an older release of the newest four", [][]Asset{assets("src.zip"), assets("a.txt"), assets("b.txt"), assets("tool-x64.msi")}, true},
		{"a fifth release is too old", [][]Asset{assets("a.txt"), assets("b.txt"), assets("c.txt"), assets("d.txt"), assets("tool-x64.msi")}, false},
		{"no releases", nil, false},
	}
	for _, tt := range tests {
		if got := HasDownload(tt.releases, "tool", x64Windows); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}
