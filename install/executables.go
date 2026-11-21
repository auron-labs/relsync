package install

import (
	"cmp"
	"debug/pe"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/selection"
)

// Windows subsystems of a program, as its header states them.
const (
	subsystemGUI     = 2
	subsystemConsole = 3
)

// ExecutableKind reads what a Windows program is from its header: a console
// program is a Tool, a graphical one an App. It returns "" for a file that is
// not a Windows program or states neither.
func ExecutableKind(file string) selection.ItemKind {
	f, err := pe.Open(filepath.Clean(file))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var subsystem uint16
	switch h := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		subsystem = h.Subsystem
	case *pe.OptionalHeader64:
		subsystem = h.Subsystem
	}
	switch subsystem {
	case subsystemConsole:
		return selection.ItemTool
	case subsystemGUI:
		return selection.ItemApp
	}
	return ""
}

// notMain are parts of a file name that mark a program as no application's
// main one: uninstallers, crash reporters, updaters, runtimes, setup programs
// and helpers.
var notMain = []string{"unins", "uninstall", "crash", "update", "vc_redist", "vcredist", "setup", "helper"}

// MainExecutable finds the program a Portable app is started by among its
// files (paths relative to root, written with "/"): a graphical program is
// preferred, then one named for the repository, then the shallowest, then the
// largest. It reports false when the files hold no program.
func MainExecutable(root string, files []string, repo string) (string, bool) {
	type candidate struct {
		path  string
		gui   bool
		named bool
		depth int
		size  int64
	}
	repoKey := selection.ProductKey(repo)
	var found []candidate
	for _, f := range files {
		name := strings.ToLower(path.Base(f))
		if !strings.HasSuffix(name, ".exe") || slices.ContainsFunc(notMain, func(s string) bool { return strings.Contains(name, s) }) {
			continue
		}
		var size int64
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			size = info.Size()
		}
		found = append(found, candidate{
			path: f, gui: ExecutableKind(filepath.Join(root, filepath.FromSlash(f))) == selection.ItemApp,
			named: repoKey != "" && strings.Contains(selection.ProductKey(strings.TrimSuffix(name, ".exe")), repoKey),
			depth: strings.Count(f, "/"), size: size,
		})
	}
	if len(found) == 0 {
		return "", false
	}
	slices.SortFunc(found, func(a, b candidate) int {
		return cmp.Or(
			boolFirst(a.gui, b.gui), boolFirst(a.named, b.named), cmp.Compare(a.depth, b.depth),
			cmp.Compare(b.size, a.size), strings.Compare(a.path, b.path),
		)
	})
	return found[0].path, true
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
