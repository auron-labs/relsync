package selection

import (
	"slices"
	"strings"
)

// Class says what a release file is, from its name alone.
type Class string

// The classes. Only a candidate can be installed.
const (
	ClassCandidate      Class = "candidate"
	ClassMetadata       Class = "metadata"
	ClassDelta          Class = "delta"
	ClassDebugOrSource  Class = "debug-or-source"
	ClassForeignPackage Class = "foreign-package"
	ClassOther          Class = "other"
)

// ArtifactKind is what a candidate is: how it gets onto the machine.
type ArtifactKind string

// The artifact kinds.
const (
	ArtifactMSI           ArtifactKind = "msi"
	ArtifactMSIX          ArtifactKind = "msix"
	ArtifactSetupExe      ArtifactKind = "setup-exe"
	ArtifactStandaloneExe ArtifactKind = "standalone-exe"
	ArtifactArchive       ArtifactKind = "archive"
)

// ItemKind is what an Item is: a desktop application or a command-line tool.
// The zero value is empty, which asks for detection where a choice is stored.
type ItemKind string

// The kinds of Item.
const (
	ItemApp  ItemKind = "app"
	ItemTool ItemKind = "tool"
)

var (
	metadataSuffixes = []string{
		".sha256", ".sha512", ".sha1", ".md5", ".sha", ".sha256sum", ".sha512sum", ".sig", ".minisig", ".pem", ".crt",
		".cer", ".asc", ".proof", ".gpgsig", ".b3", ".sbom", ".blockmap", ".bundle", ".json", ".yml", ".yaml",
		".intoto.jsonl",
	}
	metadataTokens = []string{
		"checksum", "checksums", "sha256sum", "sha256sums", "sha512sum", "sha512sums", "sha1sums", "md5sums", "sigstore",
		"intoto", "sbom", "spdx", "cyclonedx", "provenance", "attestation", "attest",
	}
	deltaSuffixes     = []string{".bsdiff", ".bspatch", ".patch", ".diff", ".delta", ".zsync", ".nupkg"}
	compressionSuffix = []string{".gz", ".xz", ".bz2", ".zst"}
	debugTokens       = []string{"symbols", "debug", "dbg", "pdb", "src", "source", "sources", "docs"}
	foreignSuffixes   = []string{".deb", ".rpm", ".apk", ".dmg", ".pkg", ".appimage", ".flatpak", ".snap"}
	archiveExts       = []string{".zip", ".7z", ".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.bz2", ".tbz2", ".tar.zst"}
	compressedExts    = []string{".gz", ".xz", ".bz2", ".zst"}
)

// classify reads a name into a class and, for a candidate, its Artifact kind.
// Matching is on tokens and suffixes, never on substrings, so a name holding
// resource or debugger is not taken for a source or debug file.
func classify(p parsed, item ItemKind) (Class, ArtifactKind) {
	switch {
	case isMetadata(p):
		return ClassMetadata, ""
	case isDelta(p):
		return ClassDelta, ""
	case p.has(debugTokens...):
		return ClassDebugOrSource, ""
	case isForeignPackage(p):
		return ClassForeignPackage, ""
	}
	if kind := artifactKindOf(p, item); kind != "" {
		return ClassCandidate, kind
	}
	return ClassOther, ""
}

func hasAnySuffix(name string, suffixes []string) bool {
	return slices.ContainsFunc(suffixes, func(s string) bool { return strings.HasSuffix(name, s) })
}

func isMetadata(p parsed) bool {
	return hasAnySuffix(p.base, metadataSuffixes) || p.has(metadataTokens...)
}

func isDelta(p parsed) bool {
	if p.base == "releases" || strings.HasPrefix(p.base, "releases-") {
		return true // A Squirrel update feed.
	}
	name := p.base
	for _, c := range compressionSuffix {
		name = strings.TrimSuffix(name, c)
	}
	return hasAnySuffix(name, deltaSuffixes)
}

func isForeignPackage(p parsed) bool {
	return hasAnySuffix(p.base, foreignSuffixes) || strings.Contains(p.base, ".pkg.tar")
}

func artifactKindOf(p parsed, item ItemKind) ArtifactKind {
	switch {
	case p.ext == ".msi":
		return ArtifactMSI
	case p.ext == ".msix", p.ext == ".msixbundle", p.ext == ".appx", p.ext == ".appxbundle":
		return ArtifactMSIX
	case p.ext == ".exe":
		return exeKind(p, item)
	case slices.Contains(archiveExts, p.ext):
		return ArtifactArchive
	case item == ItemTool && slices.Contains(compressedExts, p.ext):
		return ArtifactArchive
	}
	return ""
}

// exeKind is an .exe's kind. On an App one that says nothing is provisionally
// a setup program: only its bytes can tell, which a later phase inspects.
func exeKind(p parsed, item ItemKind) ArtifactKind {
	switch {
	case item == ItemTool:
		return ArtifactStandaloneExe
	case p.has(installerTokens...):
		return ArtifactSetupExe
	case p.has("portable"):
		return ArtifactStandaloneExe
	}
	return ArtifactSetupExe
}

// IsInstaller reports a kind that runs the publisher's setup, as opposed to
// one that is simply put in place.
func (k ArtifactKind) IsInstaller() bool {
	return k == ArtifactMSI || k == ArtifactSetupExe || k == ArtifactMSIX
}
