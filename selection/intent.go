package selection

import "strings"

// Intent is the version-independent description of a choice, kept and applied
// to every later release. Every field must agree for a file to match it.
type Intent struct {
	Product string
	Kind    ArtifactKind
	// Arch, ABI and CPUVariant are as the file name states them, empty when it
	// states none.
	Arch       string
	ABI        string
	CPUVariant string
	Variant    string
	// ArchiveMembers are the programs chosen inside an archive, as paths in it:
	// the commands of a Tool.
	ArchiveMembers []string
	// Explicit says the user chose the file, rather than it being the best match.
	Explicit bool
}

// IntentOf describes a candidate so that later releases can be matched to it.
func IntentOf(c Candidate, explicit bool) Intent {
	return Intent{
		Product: c.Product, Kind: c.Kind, Arch: joinFacts(c.Arch), ABI: joinFacts(c.ABI),
		CPUVariant: joinFacts(c.CPUVariant), Variant: c.Variant, Explicit: explicit,
	}
}

func joinFacts(values []string) string { return strings.Join(values, "+") }

// agrees reports whether a candidate is the file the Intent describes.
func (i Intent) agrees(c Candidate) bool {
	return productKey(i.Product) == productKey(c.Product) && i.Kind == c.Kind && i.Arch == joinFacts(c.Arch) &&
		i.ABI == joinFacts(c.ABI) && i.CPUVariant == joinFacts(c.CPUVariant) && i.Variant == c.Variant
}
