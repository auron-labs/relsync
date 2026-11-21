package selection

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Asset is one file of a release.
type Asset struct {
	Name string
	Size int64
}

// Method is how an App is to be installed. A Tool has no choice.
type Method string

// The install methods an App can be limited to.
const (
	MethodAutomatic Method = ""
	MethodInstaller Method = "installer"
	MethodPortable  Method = "portable"
)

// Target is the machine a download is chosen for. An empty field accepts
// anything.
type Target struct {
	OS   string
	Arch string
}

// distance is how far a build made for arch is from this machine: 0 for a
// native build, 1 and up for builds that run under emulation, the closest
// first (x86 on x64; x64, then x86, on arm64). It reports false for a build
// that cannot run here.
func (t Target) distance(arch string) (int, bool) {
	switch {
	case t.Arch == "" || arch == t.Arch:
		return 0, true
	case t.Arch == ArchX64 && arch == ArchX86, t.Arch == ArchARM64 && arch == ArchX64:
		return 1, true
	case t.Arch == ArchARM64 && arch == ArchX86:
		return 2, true
	}
	return 0, false
}

// runs reports whether the machine can run a build made for arch.
func (t Target) runs(arch string) bool {
	_, ok := t.distance(arch)
	return ok
}

// Policy is what the machine and the user's settings contribute to a choice.
type Policy struct {
	Target Target
	// PreferInstallers favours installers over portable archives.
	PreferInstallers bool
	// InstallForEveryone favours machine-wide builds over per-user ones.
	InstallForEveryone bool
}

// Request is everything a resolution depends on: no network, clock or
// settings lookup happens inside it.
type Request struct {
	Assets []Asset
	// Repo is the repository's name, the product the Item is meant to install.
	Repo string
	// Kind is the Item's kind; the zero value is an App.
	Kind   ItemKind
	Method Method
	Policy Policy
	// Intent is the stored choice to keep making; nil when there is none.
	Intent *Intent
}

// Rule names the eligibility rule that excluded a file.
type Rule string

// The eligibility rules, in the order they are checked.
const (
	RuleNone         Rule = ""
	RuleOS           Rule = "operating-system"
	RuleArchitecture Rule = "architecture"
	RuleKind         Rule = "kind"
	RuleMethod       Rule = "install-method"
	RuleCPUVariant   Rule = "cpu-variant"
)

// Candidate is a file that could be installed, with the facts its name states.
type Candidate struct {
	Asset   Asset
	Kind    ArtifactKind
	Product string
	Variant string
	// OS, Arch, ABI and CPUVariant are what the name states, canonical and sorted.
	OS, Arch, ABI, CPUVariant []string
	Score                     int
	// Signals are the score signals that applied.
	Signals []string
	// Emulated says the build runs on this machine only under emulation.
	Emulated bool
	// emulation is how far the build is from native: 0 is native, and the
	// closest emulated architecture ranks first.
	emulation int
	// ByHand says the build is CPU-specific: offered, but never chosen
	// automatically.
	ByHand bool
	// MatchesIntent says the stored Intent describes this file.
	MatchesIntent bool

	productMatch bool
	factCount    int
}

// Decision records what became of one file: its class, the rule that excluded
// it, if one did, and its score.
type Decision struct {
	Asset    string
	Class    Class
	Kind     ArtifactKind
	Excluded Rule
	Score    int
	Signals  []string
	Chosen   bool
}

// Result is the outcome of choosing a file in one release.
type Result struct {
	// Candidates are the files that could be installed, best first. Builds
	// that are only offered by hand follow the others.
	Candidates []Candidate
	// Ambiguous says several different downloads match equally well.
	Ambiguous bool
	// IntentBroken says the stored Intent describes none of the files: the
	// best match is offered, but nothing may be installed unattended.
	IntentBroken bool
	// OfferApp says a Tool has no download but would as an App.
	OfferApp bool
	// Decisions has one entry per file, in the order the files came.
	Decisions []Decision
}

// Pick is the file to install, if there is one.
func (r Result) Pick() (Candidate, bool) {
	if len(r.Candidates) == 0 || r.Candidates[0].ByHand {
		return Candidate{}, false
	}
	return r.Candidates[0], true
}

// Explain is the decision record as text, for the log.
func (r Result) Explain() string {
	var b strings.Builder
	for _, d := range r.Decisions {
		fmt.Fprintf(&b, "%s: %s", d.Asset, d.Class)
		if d.Kind != "" {
			fmt.Fprintf(&b, " %s", d.Kind)
		}
		switch {
		case d.Excluded != RuleNone:
			fmt.Fprintf(&b, ", excluded by %s", d.Excluded)
		case d.Class == ClassCandidate:
			fmt.Fprintf(&b, ", score %d (%s)", d.Score, strings.Join(d.Signals, ", "))
		}
		if d.Chosen {
			b.WriteString(", chosen")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Resolve chooses the file to install from one release's files. It first
// removes every file that cannot run here, then ranks what is left.
func Resolve(req Request) Result {
	res := resolve(req)
	if req.Kind == ItemTool {
		if _, ok := res.Pick(); !ok {
			asApp := req
			asApp.Kind, asApp.Method, asApp.Intent = ItemApp, MethodAutomatic, nil
			_, res.OfferApp = resolve(asApp).Pick()
		}
	}
	return res
}

func resolve(req Request) Result {
	res := Result{Decisions: make([]Decision, len(req.Assets))}
	var eligible, byHand []Candidate
	position := map[string]int{}
	for i, asset := range req.Assets {
		p := parseName(asset.Name)
		class, kind := classify(p, req.Kind)
		res.Decisions[i] = Decision{Asset: asset.Name, Class: class, Kind: kind}
		if class != ClassCandidate {
			continue
		}
		rule, emulation := check(p, kind, req)
		res.Decisions[i].Excluded = rule
		if rule != RuleNone && rule != RuleCPUVariant {
			continue
		}
		c := newCandidate(asset, p, kind, emulation, req)
		c.ByHand = rule == RuleCPUVariant
		res.Decisions[i].Score, res.Decisions[i].Signals = c.Score, c.Signals
		position[asset.Name] = i
		if c.ByHand {
			byHand = append(byHand, c)
		} else {
			eligible = append(eligible, c)
		}
	}
	slices.SortStableFunc(eligible, compare)
	slices.SortStableFunc(byHand, compare)
	res.Candidates = slices.Concat(eligible, byHand)
	if len(eligible) == 0 {
		return res
	}
	if req.Intent != nil {
		res.IntentBroken = !eligible[0].MatchesIntent
	}
	top := eligible[0]
	if req.Intent == nil || !top.MatchesIntent {
		res.Ambiguous = ambiguousProduct(eligible) || ambiguousVariant(eligible)
	}
	res.Decisions[position[top.Asset.Name]].Chosen = true
	return res
}

// check applies the eligibility rules to a candidate and reports the first
// one it fails. A file that only fails the CPU rule is offered by hand.
func check(p parsed, kind ArtifactKind, req Request) (rule Rule, emulation int) {
	target := req.Policy.Target
	if target.OS != "" && len(p.facts.os) > 0 && !slices.Contains(p.facts.os, target.OS) {
		return RuleOS, 0
	}
	emulation, runs := nearest(target, p.facts.arch)
	switch {
	case !runs:
		return RuleArchitecture, 0
	case req.Kind == ItemTool && kind != ArtifactStandaloneExe && kind != ArtifactArchive:
		return RuleKind, emulation
	case req.Kind != ItemTool && !acceptsMethod(req.Method, kind):
		return RuleMethod, emulation
	case len(p.facts.cpu) > 0 && !allowsCPU(req.Intent, p.facts.cpu):
		return RuleCPUVariant, emulation
	}
	return RuleNone, emulation
}

// nearest is how far the closest of the architectures a name states is from
// the target; a name that states none is as good as native.
func nearest(target Target, archs []string) (distance int, runs bool) {
	if len(archs) == 0 {
		return 0, true
	}
	for _, arch := range archs {
		if d, ok := target.distance(arch); ok && (!runs || d < distance) {
			distance, runs = d, true
		}
	}
	return distance, runs
}

func acceptsMethod(method Method, kind ArtifactKind) bool {
	switch method {
	case MethodInstaller:
		return kind.IsInstaller()
	case MethodPortable:
		return !kind.IsInstaller()
	case MethodAutomatic:
	}
	return true
}

// allowsCPU reports that the user picked this CPU-specific build by hand.
func allowsCPU(intent *Intent, cpu []string) bool {
	return intent != nil && intent.CPUVariant != "" && slices.Contains(cpu, intent.CPUVariant)
}

func newCandidate(asset Asset, p parsed, kind ArtifactKind, emulation int, req Request) Candidate {
	c := Candidate{
		Asset: asset, Kind: kind, Product: p.product, Variant: p.variant, Emulated: emulation > 0, emulation: emulation,
		OS: p.facts.os, Arch: p.facts.arch, ABI: p.facts.abi, CPUVariant: p.facts.cpu,
		factCount: p.facts.count(),
	}
	repoKey := productKey(req.Repo)
	c.productMatch = c.Product == "" || productKey(c.Product) == repoKey
	c.Score, c.Signals = score(c, p, req)
	if req.Intent != nil {
		c.MatchesIntent = req.Intent.agrees(c)
	}
	return c
}

// compare orders candidates best first: the stored choice, the intended
// product, native before emulated (the closest architecture first), score,
// more stated facts, a shorter name and finally the name itself, so the order
// never depends on the order the files came in.
func compare(a, b Candidate) int {
	return cmp.Or(
		boolFirst(a.MatchesIntent, b.MatchesIntent),
		boolFirst(a.productMatch, b.productMatch),
		cmp.Compare(a.emulation, b.emulation),
		cmp.Compare(b.Score, a.Score),
		cmp.Compare(b.factCount, a.factCount),
		cmp.Compare(len(a.Asset.Name), len(b.Asset.Name)),
		strings.Compare(a.Asset.Name, b.Asset.Name),
	)
}

func boolFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

// ambiguousProduct reports that the release holds several products and none is
// the one meant, so the best-ranked file is a guess.
func ambiguousProduct(eligible []Candidate) bool {
	if eligible[0].productMatch {
		return false
	}
	return slices.ContainsFunc(eligible, func(c Candidate) bool { return productKey(c.Product) != productKey(eligible[0].Product) })
}

// ambiguousVariant reports that the best file is matched as well by one that
// differs from it only by a build-variant token the resolver does not know,
// such as full against essentials.
func ambiguousVariant(eligible []Candidate) bool {
	top := eligible[0]
	return slices.ContainsFunc(eligible[1:], func(c Candidate) bool {
		return c.Variant != top.Variant && productKey(c.Product) == productKey(top.Product) &&
			c.MatchesIntent == top.MatchesIntent && c.productMatch == top.productMatch &&
			c.emulation == top.emulation && c.Score == top.Score && c.factCount == top.factCount
	})
}
