package selection

import "slices"

// signal is one line of a score table. Each carries its points for an App that
// prefers installers and for one that prefers portable downloads; a Tool's
// table gives both the same.
type signal struct {
	name       string
	installers int
	portable   int
	holds      func(s scoring) bool
}

// scoring is what a signal looks at.
type scoring struct {
	c        Candidate
	p        parsed
	wanted   string
	everyone bool
}

// The score tables. A weight change is a one-line diff to review against the
// golden corpus.
var (
	appSignals = []signal{
		{"architecture stated and exact", 15, 15, exactArch},
		{"operating system known", 30, 30, func(s scoring) bool { return len(s.p.facts.os) > 0 }},
		{"msi", 28, 5, func(s scoring) bool { return s.c.Kind == ArtifactMSI }},
		{"setup program", 25, 5, func(s scoring) bool { return s.c.Kind == ArtifactSetupExe }},
		{"msix", 8, 8, func(s scoring) bool { return s.c.Kind == ArtifactMSIX }},
		{"archive or standalone program", 5, 25, func(s scoring) bool {
			return s.c.Kind == ArtifactArchive || s.c.Kind == ArtifactStandaloneExe
		}},
		{"names an installer", 10, 0, func(s scoring) bool { return s.p.has(installerTokens...) }},
		{"names a portable build", -5, 10, func(s scoring) bool { return s.p.has("portable") }},
		{"product is the repository", 15, 15, func(s scoring) bool { return s.c.productMatch }},
		{"scope agrees with the setting", 6, 6, func(s scoring) bool {
			if s.everyone {
				return s.p.has(scopeEveryone...)
			}
			return s.p.has(scopeUser...)
		}},
	}
	toolSignals = []signal{
		{"architecture stated and exact", 15, 15, exactArch},
		{"operating system known", 30, 30, func(s scoring) bool { return len(s.p.facts.os) > 0 }},
		{"standalone program", 10, 10, func(s scoring) bool { return s.c.Kind == ArtifactStandaloneExe }},
		{"archive", 5, 5, func(s scoring) bool { return s.c.Kind == ArtifactArchive }},
		{"msvc build", 5, 5, func(s scoring) bool { return slices.Contains(s.c.ABI, "msvc") }},
		{"no build variant", 5, 5, func(s scoring) bool { return s.c.Variant == "" }},
		{"product is the repository", 15, 15, func(s scoring) bool { return s.c.productMatch }},
	}
)

func exactArch(s scoring) bool { return s.wanted != "" && slices.Contains(s.c.Arch, s.wanted) }

func score(c Candidate, p parsed, req Request) (total int, applied []string) {
	table := appSignals
	if req.Kind == ItemTool {
		table = toolSignals
	}
	s := scoring{
		c: c, p: p, wanted: req.Policy.Target.Arch, everyone: req.Policy.InstallForEveryone,
	}
	for _, sig := range table {
		if !sig.holds(s) {
			continue
		}
		points := sig.portable
		if req.Policy.PreferInstallers {
			points = sig.installers
		}
		total += points
		applied = append(applied, sig.name)
	}
	return total, applied
}
