package platform

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Rebase is path moved from the folder from to the folder to. It reports false
// when path is neither from nor below it. Folders compare without regard to
// case, with either kind of separator, as on Windows; what follows the folder
// in path is kept as it is, so an icon location such as `C:\app\a.exe,0` keeps
// its index.
func Rebase(path, from, to string) (string, bool) {
	from, to = strings.TrimRight(from, `\/`), strings.TrimRight(to, `\/`)
	if from == "" {
		return "", false
	}
	if strings.EqualFold(strings.TrimRight(path, `\/`), from) {
		return to, true
	}
	if len(path) > len(from) && (path[len(from)] == '\\' || path[len(from)] == '/') && strings.EqualFold(path[:len(from)], from) {
		return to + path[len(from):], true
	}
	return "", false
}

// link is a shortcut file and the paths a move of its target can change.
type link struct {
	Path    string `json:"path"`
	Target  string `json:"target"`
	Workdir string `json:"workdir"`
	Icon    string `json:"icon"`
}

// retargets reads the JSON array of shortcuts the Windows script prints and
// returns those that lead into from, with their paths moved to to. A shortcut
// leads into from by its target.
func retargets(out []byte, from, to string) ([]link, error) {
	var links []link
	if err := json.Unmarshal(out, &links); err != nil {
		return nil, fmt.Errorf("read the shortcuts: %w", err)
	}
	var changes []link
	for _, l := range links {
		target, ok := Rebase(l.Target, from, to)
		if !ok {
			continue
		}
		moved := link{Path: l.Path, Target: target, Workdir: l.Workdir, Icon: l.Icon}
		if w, ok := Rebase(l.Workdir, from, to); ok {
			moved.Workdir = w
		}
		if i, ok := Rebase(l.Icon, from, to); ok {
			moved.Icon = i
		}
		changes = append(changes, moved)
	}
	return changes, nil
}
