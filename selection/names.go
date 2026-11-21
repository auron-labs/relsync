package selection

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// Operating systems and architectures as this package spells them.
const (
	OSWindows = "windows"
	OSLinux   = "linux"
	OSDarwin  = "darwin"

	ArchX64   = "x64"
	ArchARM64 = "arm64"
	ArchX86   = "x86"
)

// The token tables below are adapted from the shared release-asset resolver
// (see NOTICE.md), with the Windows spellings it did not read as facts. A
// token is one run of letters and digits of a lower-cased file name.
var (
	osAliases = map[string]string{
		"windows": OSWindows, "win": OSWindows, "win32": OSWindows, "win64": OSWindows, "winarm64": OSWindows, "w64": OSWindows, "msvc": OSWindows,
		"darwin": OSDarwin, "macos": OSDarwin, "macosx": OSDarwin, "osx": OSDarwin, "apple": OSDarwin, "mac": OSDarwin,
		"linux": OSLinux, "linux32": OSLinux, "linux64": OSLinux, "linuxarm64": OSLinux, "manylinux": OSLinux,
		// Distributions say Linux as surely as linux does.
		"ubuntu": OSLinux, "debian": OSLinux, "fedora": OSLinux, "centos": OSLinux, "rhel": OSLinux, "alpine": OSLinux,
		"android": "android", "freebsd": "freebsd", "openbsd": "openbsd", "netbsd": "netbsd", "dragonfly": "dragonfly",
	}
	archAliases = map[string]string{
		"amd64": ArchX64, "x86_64": ArchX64, "x64": ArchX64, "64bit": ArchX64,
		"arm64": ArchARM64, "aarch64": ArchARM64,
		"386": ArchX86, "i386": ArchX86, "i586": ArchX86, "i686": ArchX86, "x86": ArchX86, "32bit": ArchX86, "ia32": ArchX86,
		"armv7": "arm", "armv7l": "arm", "armv6": "arm", "armv6l": "arm", "arm": "arm", "armhf": "arm", "armel": "arm",
		"ppc64": "ppc64", "powerpc64": "ppc64", "ppc64le": "ppc64le", "powerpc64le": "ppc64le", "powerpc": "ppc", "s390x": "s390x",
		"riscv64": "riscv64", "riscv64gc": "riscv64", "mips": "mips", "mipsel": "mipsle", "mips64": "mips64",
		"mips64el": "mips64le", "mips64le": "mips64le", "loongarch64": "loong64", "loong64": "loong64",
	}
	// impliedArch are OS spellings that also name a word size. They only count
	// when the name states no architecture: win32 is also what Electron calls
	// every Windows build, as in app-win32-x64.zip.
	impliedArch = map[string]string{"win64": ArchX64, "win32": ArchX86, "winarm64": ArchARM64, "linuxarm64": ArchARM64}
	abiAliases  = map[string]string{"gnu": "glibc", "glibc": "glibc", "musl": "musl", "msvc": "msvc"}
	cpuAliases  = map[string]string{"avx512": "avx512", "avx2": "avx2", "avx": "avx", "neon": "neon"}

	installerTokens = []string{"setup", "installer", "install"}
	scopeUser       = []string{"user", "peruser"}
	scopeEveryone   = []string{"system", "machine", "allusers", "permachine"}

	// packagingTokens say how a file is packaged, which Artifact kind and the
	// scores already account for, so they are neither product nor variant.
	packagingTokens = tokenSet("setup", "installer", "install", "portable", "msi", "msix", "appx", "user", "peruser",
		"system", "machine", "allusers", "permachine")
	noiseTokens = tokenSet("pc", "unknown", "release", "latest", "stable", "final", "alpha", "beta", "rc", "preview",
		"pre", "dev", "nightly", "canary", "snapshot", "build", "bin", "binary", "bit")

	versionToken = regexp.MustCompile(`^v?\d+(?:[a-z]{0,2}\d*)?$`)
	// tokenRuns finds the runs of letters and digits splitTokens splits on.
	tokenRuns = regexp.MustCompile(`[A-Za-z0-9]+`)
)

// extensions are the file endings the resolver recognises, longest first.
var extensions = []string{
	".tar.gz", ".tar.xz", ".tar.bz2", ".tar.zst", ".tgz", ".txz", ".tbz2", ".zip", ".7z", ".gz", ".xz", ".bz2", ".zst",
	".exe", ".msi", ".msix", ".msixbundle", ".appx", ".appxbundle", ".nupkg",
	".appimage", ".deb", ".rpm", ".apk", ".dmg", ".pkg", ".flatpak", ".snap",
}

func tokenSet(tokens ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		set[t] = struct{}{}
	}
	return set
}

// facts are the target facts a name states, each canonical, sorted and unique.
type facts struct {
	os, arch, abi, cpu []string
}

// count is how many target facts the name states.
func (f facts) count() int { return len(f.os) + len(f.arch) + len(f.abi) + len(f.cpu) }

// parsed is what is read from one file name.
type parsed struct {
	// base is the lower-cased file name without any directory.
	base string
	// ext is the recognised extension with its dot, "" when there is none.
	ext string
	// tokens are the tokens of the whole name, including its extension.
	tokens []string
	// facts are what the name says of its target, and for Windows programs what
	// their extension says.
	facts   facts
	product string
	variant string
}

func parseName(name string) parsed {
	base := strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/")))
	ext := extensionOf(base)
	stem := strings.TrimSuffix(base, ext)
	p := parsed{base: base, ext: ext, tokens: splitTokens(base)}
	stemTokens := splitTokens(stem)
	p.facts = factsOf(stemTokens)
	if len(p.facts.os) == 0 && impliesWindows(ext) {
		p.facts.os = []string{OSWindows}
	}
	p.product, p.variant = productAndVariant(stemTokens)
	return p
}

// impliesWindows reports an extension only Windows software has. The name does
// not need to say Windows when its extension already does.
func impliesWindows(ext string) bool {
	switch ext {
	case ".exe", ".msi", ".msix", ".msixbundle", ".appx", ".appxbundle":
		return true
	}
	return false
}

func extensionOf(base string) string {
	for _, ext := range extensions {
		if strings.HasSuffix(base, ext) && len(base) > len(ext) {
			return ext
		}
	}
	return ""
}

func splitTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

func (p parsed) has(tokens ...string) bool {
	for _, t := range tokens {
		if slices.Contains(p.tokens, t) {
			return true
		}
	}
	return false
}

func factsOf(tokens []string) facts {
	var f facts
	for i, tok := range tokens {
		f.os = addUnique(f.os, osOf(tok))
		f.abi = addUnique(f.abi, abiAliases[tok])
		f.cpu = addUnique(f.cpu, cpuAliases[tok])
		if isMicroarchLevel(tok) && i > 0 && (tokens[i-1] == "64" || tokens[i-1] == "x64" || tokens[i-1] == "amd64") {
			f.cpu = addUnique(f.cpu, "x86-64-"+tok) // x86_64-v3 builds need AVX2-class CPUs.
		}
	}
	f.arch = archOf(tokens)
	slices.Sort(f.os)
	slices.Sort(f.arch)
	slices.Sort(f.abi)
	slices.Sort(f.cpu)
	return f
}

func isMicroarchLevel(tok string) bool { return tok == "v2" || tok == "v3" || tok == "v4" }

// osOf is the operating system a token names. A word size after a name, as in
// ubuntu64 or mac64, does not change it.
func osOf(tok string) string {
	if os, ok := osAliases[tok]; ok {
		return os
	}
	if len(tok) > 2 && (strings.HasSuffix(tok, "64") || strings.HasSuffix(tok, "32")) {
		return osAliases[tok[:len(tok)-2]]
	}
	return ""
}

// archOf reads architectures, joining the tokens that spell one together:
// x86_64 and x86-64 are x64, never x86 as well; 64-bit and 32-bit are word sizes.
func archOf(tokens []string) []string {
	var found []string
	for i, tok := range tokens {
		next := ""
		if i+1 < len(tokens) {
			next = tokens[i+1]
		}
		switch {
		case tok == "x86" && next == "64", tok == "64" && next == "bit":
			found = addUnique(found, ArchX64)
		case tok == "32" && next == "bit":
			found = addUnique(found, ArchX86)
		default:
			found = addUnique(found, archAliases[tok])
		}
	}
	if len(found) > 0 {
		return found
	}
	for _, tok := range tokens {
		found = addUnique(found, impliedArch[tok])
	}
	return found
}

func addUnique(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

// productAndVariant splits a name's tokens into the product, the leading part
// before the first version, target or packaging token, and the build variant,
// whatever else remains once versions, targets and packaging are dropped.
func productAndVariant(tokens []string) (product, variant string) {
	i := 0
	for i < len(tokens) && !isStopToken(tokens[i]) {
		i++
	}
	product = strings.Join(tokens[:i], "-")
	for _, alias := range []string{"-cli", "-client", "-mcp"} {
		product = strings.TrimSuffix(product, alias)
	}
	var rest []string
	for _, tok := range tokens[i:] {
		if !isStopToken(tok) {
			rest = append(rest, tok)
		}
	}
	return product, strings.Join(rest, "-")
}

// isStopToken reports a token that is not part of a product or variant name.
func isStopToken(tok string) bool {
	if versionToken.MatchString(tok) {
		return true
	}
	if _, ok := packagingTokens[tok]; ok {
		return true
	}
	if _, ok := noiseTokens[tok]; ok {
		return true
	}
	return isTargetToken(tok)
}

// isTargetToken reports a token that states an operating system, an
// architecture, an ABI or a CPU variant.
func isTargetToken(tok string) bool {
	isOS := osOf(tok) != ""
	_, isArch := archAliases[tok]
	_, isABI := abiAliases[tok]
	_, isCPU := cpuAliases[tok]
	return isOS || isArch || isABI || isCPU
}

// productKey is a product or repository name reduced to what must match: the
// letters and digits, so node.js, node-js and nodejs are one product.
func productKey(name string) string {
	return strings.Join(splitTokens(name), "")
}

// CommandName is the name a program is run by: its file name without the
// extension and without the version and target words releases add, so
// tool-v1.2.0-windows-amd64.exe is run as tool. A name that is nothing but
// such words is kept whole.
func CommandName(file string) string {
	name := path.Base(strings.ReplaceAll(file, "\\", "/"))
	if ext := extensionOf(strings.ToLower(name)); ext != "" {
		name = name[:len(name)-len(ext)]
	} else if ext := path.Ext(name); ext != "" {
		name = name[:len(name)-len(ext)]
	}
	for _, run := range tokenRuns.FindAllStringIndex(name, -1) {
		if run[0] > 0 && isStopToken(strings.ToLower(name[run[0]:run[1]])) {
			return strings.TrimRight(name[:run[0]], "-_. ")
		}
	}
	return name
}

// ProductKey reduces a product or program name to what must match when two
// names are compared: its letters and digits, so node.js, node-js and nodejs
// are one name.
func ProductKey(name string) string { return productKey(name) }

// NamesInstaller reports a file name that says it is a setup program: it has a
// setup, installer or install word.
func NamesInstaller(file string) bool { return parseName(file).has(installerTokens...) }
