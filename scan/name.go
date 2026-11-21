package scan

import (
	"regexp"
	"strings"

	"github.com/aaronflorey/release-sync/selection"
)

var (
	bracketed       = regexp.MustCompile(`\(.*?\)|\[.*?\]`)
	installerWords  = regexp.MustCompile(`(?i)\b(x64|x86|64-bit|32-bit|amd64|win64|win32|setup|installer)\b`)
	trailingVersion = regexp.MustCompile(`\s+v?\d+([._-]\d+)*.*$`)
	runsOfSpace     = regexp.MustCompile(`\s+`)

	// systemNames and systemPublishers match programs that are Windows
	// components, drivers, runtimes, SDKs and the software of hardware vendors:
	// not worth looking up on GitHub.
	systemNames = regexp.MustCompile(`(?i)redistributable|runtime|driver|visual c\+\+|\.net |windows sdk|webview|framework|microsoft edge|` +
		`update for|security update|windows (software|app)|intel\(r\)|nvidia|realtek|amd |vulkan|directx|office|onedrive|teams`)
	systemPublishers = regexp.MustCompile(`(?i)microsoft|nvidia|intel|advanced micro|realtek|dell|hewlett|lenovo|asus|logitech|corsair|razer|qualcomm|adobe`)

	folderNameEnd = regexp.MustCompile(`(?i)^(.+?)(?:[-_ .](?:v?\d|windows|win|x64|amd64|x86|portable|release|standard)|$)`)
	versionInText = regexp.MustCompile(`(?i)v?(\d+(?:\.\d+){1,3})`)
)

// CleanName reduces a program's display name to what to search for: bracketed
// text, architecture and installer words and a trailing version are removed,
// so "7-Zip 26.02 (x64)" becomes "7-Zip".
func CleanName(display string) string {
	s := bracketed.ReplaceAllString(display, " ")
	s = installerWords.ReplaceAllString(s, " ")
	s = trailingVersion.ReplaceAllString(s, "")
	return strings.TrimSpace(runsOfSpace.ReplaceAllString(s, " "))
}

// SystemLike reports a candidate that is a Windows, Microsoft or driver
// component, a runtime or a hardware vendor's software.
func SystemLike(c Candidate) bool {
	return systemNames.MatchString(c.Name) || systemPublishers.MatchString(c.Publisher)
}

// nameFromFolder is what a program is called by the folder it is unpacked in:
// the words before a version, an architecture or "portable". It is empty when
// there is less than two letters of it.
func nameFromFolder(folder string) string {
	m := folderNameEnd.FindStringSubmatch(folder)
	if m == nil {
		return ""
	}
	name := strings.TrimSpace(strings.NewReplacer("-", " ", "_", " ").Replace(m[1]))
	if len(name) < 2 {
		return ""
	}
	return name
}

// versionIn is the first version number of two to four parts in text.
func versionIn(text string) string {
	if m := versionInText.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// nameKey reduces a name to its letters and digits, lower-cased.
func nameKey(name string) string { return selection.ProductKey(name) }
