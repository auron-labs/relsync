package scan

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
)

const (
	// foldersLimit is how many sub-folders of a folder the user added are looked in.
	foldersLimit = 400
	// innerFolders is how many folders below a sub-folder are looked in when it
	// holds no program of its own.
	innerFolders = 5
)

// Covered is what something else already accounts for, so a program there is
// not a portable program of the user's.
type Covered struct {
	// Folders are folders the system, a setup program or the Library owns;
	// anything below one is covered.
	Folders []string
	// Programs are program files that are covered by themselves.
	Programs []string
}

// CoverPrograms adds the installed programs' install locations and icon files.
func (c *Covered) CoverPrograms(programs []platform.Program) {
	for _, p := range programs {
		if p.InstallLocation != "" {
			c.Folders = append(c.Folders, p.InstallLocation)
		}
		if exe := install.IconPath(p.DisplayIcon); strings.HasSuffix(strings.ToLower(exe), ".exe") {
			c.Programs = append(c.Programs, exe)
		}
	}
}

// covers reports a program file that is one of the covered programs or below a
// covered folder, ignoring case as Windows does.
func (c Covered) covers(exe string) bool {
	lower := strings.ToLower(exe)
	if slices.ContainsFunc(c.Programs, func(p string) bool { return strings.ToLower(p) == lower }) {
		return true
	}
	return slices.ContainsFunc(c.Folders, func(dir string) bool {
		return dir != "" && strings.HasPrefix(lower, strings.ToLower(strings.TrimRight(dir, `\/`))+string(filepath.Separator))
	})
}

// portable builds the candidates of portable programs, from program files and
// the names they are known by, leaving out those that are covered.
type portable struct {
	platform platform.Platform
	covered  Covered
	seen     map[string]bool
	found    []Candidate
}

// add makes a candidate of the program file named, unless it is not a program,
// is covered or was seen already. name is what a shortcut calls it, empty when
// the program's folder names it; home is the folder whose name says the
// program's name and version.
func (p *portable) add(exe, name, home string) {
	lower := strings.ToLower(exe)
	if !strings.HasSuffix(lower, ".exe") || install.IsAuxiliary(exe) || p.seen[lower] || p.covered.covers(exe) {
		return
	}
	if info, err := os.Stat(exe); err != nil || info.IsDir() {
		return
	}
	p.seen[lower] = true
	folder := filepath.Dir(exe)
	stem := strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))
	if name == "" || strings.EqualFold(name, stem) || strings.HasSuffix(strings.ToLower(name), ".exe") {
		name = cmp.Or(nameFromFolder(filepath.Base(home)), stem)
	}
	// A program that cannot be read still has a name, and a folder to be found by.
	info, _ := p.platform.VersionInfo(exe)
	version := versionIn(filepath.Base(home))
	if version == "" {
		if v := versionIn(info.ProductVersion); !strings.HasPrefix(v, "0.0.0") {
			version = v
		}
	}
	p.found = append(p.found, Candidate{
		Key: "folder:" + lower, Origin: OriginFolder, Name: name, Publisher: info.CompanyName, Version: version,
		Folder: folder, MainExe: filepath.Base(exe),
		Texts: []string{info.Comments, info.LegalCopyright, info.LegalTrademarks, info.ProductName, info.CompanyName},
	}.linkedByText())
}

// FromShortcuts makes a candidate of each program the Desktop and Start menu
// shortcuts start that nothing else accounts for. The name is the shortcut's.
func FromShortcuts(p platform.Platform, shortcuts []platform.Shortcut, covered Covered) []Candidate {
	pt := portable{platform: p, covered: covered, seen: map[string]bool{}}
	for _, s := range shortcuts {
		pt.add(s.Target, strings.TrimSuffix(filepath.Base(s.Path), filepath.Ext(s.Path)), filepath.Dir(s.Target))
	}
	return pt.found
}

// FromFolders makes a candidate of the program in each sub-folder of the
// folders the user added, up to 400 sub-folders each. In a sub-folder it is the
// program whose name the folder's name contains, else the largest, ignoring
// installers, uninstallers, updaters, crash handlers and helpers. The name
// comes from the folder.
func FromFolders(p platform.Platform, folders []string, covered Covered) []Candidate {
	pt := portable{platform: p, covered: covered, seen: map[string]bool{}}
	for _, folder := range folders {
		entries, err := os.ReadDir(folder)
		if err != nil {
			continue
		}
		subs := 0
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if subs++; subs > foldersLimit {
				break
			}
			sub := filepath.Join(folder, e.Name())
			if exe, ok := mainProgramOf(sub); ok {
				pt.add(exe, "", sub)
			}
		}
	}
	return pt.found
}

// mainProgramOf finds the program of a portable program's folder, looking in
// the folder and, when it holds none, a few folders in it.
func mainProgramOf(dir string) (string, bool) {
	programs := programsIn(dir)
	if len(programs) == 0 {
		inner, err := os.ReadDir(dir)
		if err != nil {
			return "", false
		}
		looked := 0
		for _, e := range inner {
			if !e.IsDir() {
				continue
			}
			if looked++; looked > innerFolders {
				break
			}
			programs = append(programs, programsIn(filepath.Join(dir, e.Name()))...)
		}
	}
	if len(programs) == 0 {
		return "", false
	}
	key := nameKey(filepath.Base(dir))
	named := func(f string) bool {
		name := nameKey(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)))
		return name != "" && strings.Contains(key, name)
	}
	if i := slices.IndexFunc(programs, named); i >= 0 {
		return programs[i], true
	}
	size := func(f string) int64 {
		if info, err := os.Stat(f); err == nil {
			return info.Size()
		}
		return 0
	}
	return slices.MaxFunc(programs, func(a, b string) int { return cmp.Compare(size(a), size(b)) }), true
}

// programsIn lists the program files directly in dir, except the helpers.
func programsIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".exe") && !install.IsAuxiliary(e.Name()) {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	return found
}
