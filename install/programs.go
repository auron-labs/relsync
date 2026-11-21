package install

import (
	"cmp"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/aaronflorey/release-sync/platform"
)

// Identity is what an Item is called, for finding its entry among the
// installed programs.
type Identity struct {
	Owner, Repo, Title string
}

// Timing is how often, and for how long, the list of installed programs is
// read after a setup program or an uninstaller ends: some hand the work to
// another process and end before the list changes. A zero field takes its
// default.
type Timing struct {
	Interval time.Duration
	// Appear is how long to wait for a new entry, Vanish for a removed one.
	Appear, Vanish time.Duration
}

func (t Timing) withDefaults() Timing {
	t.Interval = cmp.Or(t.Interval, time.Second)
	t.Appear = cmp.Or(t.Appear, 15*time.Second)
	t.Vanish = cmp.Or(t.Vanish, 30*time.Second)
	return t
}

// matchKey reduces a name to its letters and digits, lower-cased.
func matchKey(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

// MatchScore says how well an entry matches an Item, 0 for not at all: a name
// equal to the repository's or the Item's is 100; one containing the
// repository's is 70, and one containing the Item's 65; a publisher matching
// the owner adds 20, and an install location containing the repository's name
// adds 10. A name is also contained in a longer one, if it has at least four
// letters.
func MatchScore(p platform.Program, id Identity) int {
	name, repo, title := matchKey(p.Name), matchKey(id.Repo), matchKey(id.Title)
	contains := func(whole, part string) bool {
		return len(part) >= 3 && (strings.Contains(whole, part) || len(whole) >= 4 && strings.Contains(part, whole))
	}
	score := 0
	switch {
	case name != "" && (name == repo || name == title):
		score = 100
	case contains(name, repo):
		score = 70
	case contains(name, title):
		score = 65
	}
	if score == 0 {
		return 0
	}
	publisher, owner := matchKey(p.Publisher), matchKey(id.Owner)
	if len(publisher) > 2 && len(owner) > 2 && (strings.Contains(publisher, owner) || strings.Contains(owner, publisher)) {
		score += 20
	}
	if repo != "" && strings.Contains(matchKey(p.InstallLocation), repo) {
		score += 10
	}
	return score
}

// FindNew finds the entry a setup program made: among those that are new or
// whose version changed since before, the only one, or the best match to the
// Item.
func FindNew(before, after []platform.Program, id Identity) (platform.Program, bool) {
	known := make(map[string]string, len(before))
	for _, p := range before {
		known[p.Key] = p.Version
	}
	var fresh []platform.Program
	for _, p := range after {
		if version, ok := known[p.Key]; !ok || version != p.Version {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) == 0 {
		return platform.Program{}, false
	}
	slices.SortFunc(fresh, func(a, b platform.Program) int {
		return cmp.Or(cmp.Compare(MatchScore(b, id), MatchScore(a, id)), strings.Compare(a.Key, b.Key))
	})
	return fresh[0], true
}

// Entry finds the entry with the key.
func Entry(programs []platform.Program, key string) (platform.Program, bool) {
	i := slices.IndexFunc(programs, func(p platform.Program) bool { return p.Key == key })
	if i < 0 {
		return platform.Program{}, false
	}
	return programs[i], true
}

var iconIndex = regexp.MustCompile(`^\s*-?\d+$`)

// IconPath is the file a DisplayIcon value names, without the quotes and the
// ",index" the system appends.
func IconPath(icon string) string {
	icon = strings.TrimSpace(icon)
	if i := strings.LastIndex(icon, ","); i > 0 && iconIndex.MatchString(icon[i+1:]) {
		icon = icon[:i]
	}
	return strings.Trim(strings.TrimSpace(icon), `"`)
}

// mainSearchDepth is how many folders down an install location is searched for
// its program; it is a large folder that is walked.
const mainSearchDepth = 3

// MainProgram finds the program an installed entry is started by, with the
// folder it is relative to: the entry's icon file when that is a program,
// otherwise the best program in its install location by the rule that finds a
// Portable app's. Both are empty when there is none.
func MainProgram(p platform.Program, repo string) (folder, mainExe string) {
	if exe := IconPath(p.DisplayIcon); strings.HasSuffix(strings.ToLower(exe), ".exe") && !isUninstaller(exe) && fileExists(exe) {
		if rel, ok := below(p.InstallLocation, exe); ok {
			return p.InstallLocation, rel
		}
		return filepath.Dir(exe), filepath.Base(exe)
	}
	if p.InstallLocation == "" {
		return "", ""
	}
	if main, ok := MainExecutable(p.InstallLocation, programsBelow(p.InstallLocation), repo); ok {
		return p.InstallLocation, main
	}
	return p.InstallLocation, ""
}

// AdoptEntry is the install record of software that is on the machine
// already, bound to its entry in the installed programs. It names no release
// file: the application did not install it.
func AdoptEntry(p platform.Program, repo string) Record {
	rec := Record{Method: MethodInstaller, UninstallKey: p.Key, Scope: p.Scope, EntryVersion: p.Version}
	rec.Folder, rec.MainExe = MainProgram(p, repo)
	return rec
}

// AdoptLocated is the install record of software the system does not list,
// whose program the user pointed at.
func AdoptLocated(program string) Record {
	return Record{Method: MethodInstaller, Folder: filepath.Dir(program), MainExe: filepath.Base(program)}
}

// AdoptPortable is the install record of a portable program that is in a folder
// of the user's. Its files are not listed, so an update replaces the files it
// ships and a removal deletes nothing.
func AdoptPortable(folder, mainExe string) Record {
	return Record{Method: MethodPortable, Folder: folder, MainExe: mainExe}
}

// IsAuxiliary reports a program file that belongs to no one's idea of the
// application itself: an installer, uninstaller, updater, crash handler or
// helper.
func IsAuxiliary(file string) bool {
	name := strings.ToLower(filepath.Base(file))
	return strings.Contains(name, "install") || slices.ContainsFunc(notMain, func(s string) bool { return strings.Contains(name, s) })
}

func isUninstaller(file string) bool {
	name := strings.ToLower(filepath.Base(file))
	return strings.Contains(name, "unins")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// below is path relative to dir, written with "/", when path is a file inside it.
func below(dir, path string) (string, bool) {
	if dir == "" {
		return "", false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// programsBelow lists the .exe files in dir, a few folders deep, relative to it.
func programsBelow(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err //nolint:wrapcheck // The walk reports it, and a path it found is always below dir.
		}
		if d.IsDir() {
			if strings.Count(filepath.ToSlash(rel), "/") >= mainSearchDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".exe") {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files
}

// waitFor reads the list of installed programs until found says it has what it
// waits for, or the wait is over, and returns the last list it read. A failure
// to read stops the wait.
func waitFor(ctx context.Context, p platform.Platform, wait, interval time.Duration, found func([]platform.Program) bool) ([]platform.Program, error) {
	deadline := time.Now().Add(wait)
	for {
		programs, err := p.Programs(ctx)
		if err != nil {
			return nil, err //nolint:wrapcheck // The platform says what it was reading.
		}
		if found(programs) || !time.Now().Before(deadline) {
			return programs, nil
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return programs, ctx.Err() //nolint:wrapcheck // The caller adds what it was waiting for.
		}
	}
}
