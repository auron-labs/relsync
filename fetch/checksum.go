package fetch

import (
	"path"
	"regexp"
	"strings"

	"github.com/aaronflorey/release-sync/source"
)

// ownSidecarSuffixes are the names a checksum file for one asset goes by, in
// the order they are tried.
var ownSidecarSuffixes = []string{".sha256", ".sha256sum", ".sha256.txt", ".sha-256"}

// checksumWords mark a file as holding checksums when they appear in its name.
var checksumWords = []string{"sha256", "sha-256", "checksum"}

// signatureSuffixes mark a file as a signature, whatever else its name says.
var signatureSuffixes = []string{".sig", ".asc", ".minisig", ".pem", ".crt", ".cert", ".sigstore"}

var (
	hexDigest = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)
	// bsdLine is "SHA256 (file name) = digest".
	bsdLine = regexp.MustCompile(`(?i)^\s*SHA-?256\s*\((.+)\)\s*=\s*([0-9a-f]{64})\s*$`)
	// gnuLine is "digest  file name", with "*" before the name for a binary read.
	gnuLine = regexp.MustCompile(`^\s*([0-9a-fA-F]{64})[ \t]+\*?(.+?)\s*$`)
)

// sidecarCandidates lists the files of a release that may hold the asset's
// checksum, best first: files named after the asset, then any other checksum
// list. Signatures are never candidates.
func sidecarCandidates(assetName string, siblings []source.Asset) []source.Asset {
	var out []source.Asset
	taken := map[string]bool{}
	add := func(a source.Asset) {
		if !taken[a.Name] {
			taken[a.Name] = true
			out = append(out, a)
		}
	}
	for _, suffix := range ownSidecarSuffixes {
		for _, a := range siblings {
			if strings.EqualFold(a.Name, assetName+suffix) {
				add(a)
			}
		}
	}
	for _, a := range siblings {
		if a.Name != assetName && !isSignature(a.Name) && mentionsChecksum(a.Name) {
			add(a)
		}
	}
	return out
}

func mentionsChecksum(name string) bool {
	lower := strings.ToLower(name)
	for _, word := range checksumWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func isSignature(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range signatureSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return strings.Contains(lower, ".sigstore")
}

// findDigest reads the checksum of assetName from the text of the checksum
// file sidecarName. A line counts when it names the asset; a file named after
// the asset that holds a single line counts whatever the line says.
func findDigest(text, assetName, sidecarName string) (digest string, ok bool) {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	for _, line := range lines {
		if name, digest, found := splitChecksumLine(line); found && sameFile(name, assetName) {
			return strings.ToLower(digest), true
		}
	}
	namedAfterAsset := strings.HasPrefix(strings.ToLower(sidecarName), strings.ToLower(assetName)+".")
	if namedAfterAsset && len(lines) == 1 {
		if digest := hexDigest.FindString(lines[0]); digest != "" {
			return strings.ToLower(digest), true
		}
	}
	return "", false
}

// splitChecksumLine reads one line of a checksum list in the GNU or BSD style.
func splitChecksumLine(line string) (name, digest string, ok bool) {
	if m := bsdLine.FindStringSubmatch(line); m != nil {
		return m[1], m[2], true
	}
	if m := gnuLine.FindStringSubmatch(line); m != nil {
		return m[2], m[1], true
	}
	return "", "", false
}

// sameFile compares a name from a checksum list, which may carry the folder
// the publisher built in, with an asset's name.
func sameFile(listed, assetName string) bool {
	listed = strings.ReplaceAll(listed, `\`, "/")
	return strings.EqualFold(path.Base(listed), assetName)
}
