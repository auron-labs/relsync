package install

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
)

const (
	// asideSuffix marks a program that was in use when it was replaced or
	// removed, and was renamed so its name is free. It is deleted when it can be.
	asideSuffix = ".rs-old"
	// newSuffix marks a program being written beside its destination.
	newSuffix = ".rs-new"
)

// removeFile deletes a file; tests replace it to leave a file behind the way a
// running program does on Windows.
var removeFile = os.Remove

// ToolMembers lists the staged files that could be commands: programs that can
// run on target, judged by their bytes and not their names, and batch scripts.
// Documentation, licences, completions and libraries are not offered.
func ToolMembers(st *Staged, target selection.Target) ([]selection.Member, error) {
	files, err := st.Files()
	if err != nil {
		return nil, err
	}
	members := make([]selection.Member, 0, len(files))
	for _, f := range files {
		if !couldBeCommand(f) {
			continue
		}
		err := selection.ValidateRunnablePayload(filepath.Join(st.Root, filepath.FromSlash(f)), f, target)
		members = append(members, selection.Member{Identity: f, Eligible: err == nil})
	}
	return members, nil
}

// notCommandFolders hold documentation and shell integration, never commands.
var notCommandFolders = []string{"doc", "docs", "man", "licenses", "completions", "completion"}

// couldBeCommand reports a file worth inspecting: a Windows program or batch
// script outside the folders of documentation and completions.
func couldBeCommand(file string) bool {
	switch strings.ToLower(path.Ext(file)) {
	case ".exe", ".bat", ".cmd":
	default:
		return false
	}
	dirs := strings.Split(strings.ToLower(path.Dir(file)), "/")
	return !slices.ContainsFunc(dirs, func(d string) bool { return slices.Contains(notCommandFolders, d) })
}

// ChoiceError says the programs to install as commands cannot be told from the
// download: the user must choose among Candidates. Missing lists the
// recorded programs a newer release no longer has.
type ChoiceError struct {
	Candidates []string
	// Kept are the recorded programs the release still has.
	Kept    []string
	Missing []string
}

func (e *ChoiceError) Error() string {
	if len(e.Missing) > 0 {
		return "programs no longer in the download: " + strings.Join(e.Missing, ", ")
	}
	return "which programs to add to PATH needs a choice"
}

// ChooseCommands decides which of the members become commands. Programs the
// Item installed before (recorded, as paths in the archive) are found again,
// ignoring a versioned wrapper folder; one that is gone is a ChoiceError, as is
// a first install where no program is clearly the one: the member named for the
// product or repository is the default, and so is a lone program.
func ChooseCommands(members []selection.Member, recorded []string, repo, product string) ([]string, error) {
	var eligible []string
	for _, m := range members {
		if m.Eligible {
			eligible = append(eligible, m.Identity)
		}
	}
	if len(eligible) == 0 {
		return nil, ErrNoProgram
	}
	if len(recorded) > 0 {
		return resolveRecorded(members, eligible, recorded, product)
	}
	var named []string
	for _, id := range eligible {
		key := selection.ProductKey(selection.CommandName(id))
		if key != "" && (key == selection.ProductKey(repo) || key == selection.ProductKey(product)) {
			named = append(named, id)
		}
	}
	switch {
	case len(named) == 1:
		return named, nil
	case len(eligible) == 1:
		return eligible, nil
	}
	return nil, &ChoiceError{Candidates: eligible}
}

func resolveRecorded(members []selection.Member, eligible, recorded []string, product string) ([]string, error) {
	var chosen, missing []string
	for _, r := range recorded {
		m, err := selection.ResolvePersistedMember(members, r, product)
		if err != nil {
			missing = append(missing, r)
			continue
		}
		if !slices.Contains(chosen, m.Identity) {
			chosen = append(chosen, m.Identity)
		}
	}
	if len(missing) > 0 {
		return nil, &ChoiceError{Candidates: eligible, Kept: chosen, Missing: missing}
	}
	return chosen, nil
}

// ToolRequest is what a Tool install needs to know.
type ToolRequest struct {
	// Dir is the tools folder.
	Dir string
	// Asset is the release file the staged files came from.
	Asset string
	// Members are the staged programs to install as commands, as paths in the
	// staged release.
	Members []string
	// Previous is the record being replaced, with its files; nil for a fresh install.
	Previous *Record
	// Owners maps the lower-case command names other Items have to the Item's name.
	Owners map[string]string
	// Replace lists lower-case command names whose existing file, one this
	// application did not install, the user agreed to replace.
	Replace  map[string]bool
	Platform platform.Platform
	// LookPath finds the file a command name resolves to on the PATH, as the
	// shell would; nil uses the running program's PATH.
	LookPath func(file string) (string, error)
}

// command is a staged program and where it goes.
type command struct {
	member, name, file string
}

func (r ToolRequest) commands() ([]command, error) {
	var out []command
	seen := map[string]bool{}
	for _, m := range r.Members {
		name := selection.CommandName(m)
		file := name + path.Ext(m)
		if seen[strings.ToLower(file)] {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateCommand, file)
		}
		seen[strings.ToLower(file)] = true
		out = append(out, command{member: m, name: name, file: file})
	}
	return out, nil
}

// ApplyTool writes the chosen programs into the tools folder and puts the
// folder on the PATH. Each program is written beside its destination and
// swapped in; one that is in use is renamed aside, so a Tool can be updated
// while it runs. Before anything is written it fails with CommandTakenError for
// a command another Item has and ForeignFileError for a file this application
// did not install. A failure puts back what was there.
func ApplyTool(ctx context.Context, st *Staged, req ToolRequest) (Outcome, error) {
	cmds, err := req.commands()
	if err != nil {
		return Outcome{}, err
	}
	if len(cmds) == 0 {
		return Outcome{}, ErrNoProgram
	}
	ours := map[string]bool{}
	if req.Previous != nil {
		for _, f := range req.Previous.Files {
			ours[strings.ToLower(f.Path)] = true
		}
	}
	for _, c := range cmds {
		if owner, taken := req.Owners[strings.ToLower(c.name)]; taken {
			return Outcome{}, &CommandTakenError{Command: c.name, Owner: owner}
		}
		_, statErr := os.Lstat(filepath.Join(req.Dir, c.file))
		if statErr == nil && !ours[strings.ToLower(c.file)] && !req.Replace[strings.ToLower(c.name)] {
			return Outcome{}, &ForeignFileError{Command: c.name, Path: filepath.Join(req.Dir, c.file)}
		}
	}
	if err := os.MkdirAll(req.Dir, 0o750); err != nil {
		return Outcome{}, fmt.Errorf("create %s: %w", req.Dir, err)
	}
	CleanLeftovers(req.Dir)
	written, err := writeBeside(ctx, st, req.Dir, cmds)
	if err != nil {
		return Outcome{}, err
	}
	if err := swapIn(req.Dir, cmds, written, req.Previous); err != nil {
		return Outcome{}, err
	}
	rec := Record{Method: MethodTool, Asset: req.Asset, Folder: req.Dir}
	for _, c := range cmds {
		rec.Files = append(rec.Files, File{Path: c.file, SHA256: written[c.file].digest, Command: c.name})
	}
	rec.SHA256 = rec.Files[0].SHA256
	out := Outcome{Record: rec}
	if out.PathAdded, err = req.Platform.AddToUserPath(req.Dir); err != nil {
		out.Warning = err
	}
	lookPath := req.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	out.Shadowed = shadowedCommands(cmds, req.Dir, lookPath)
	return out, nil
}

// Shadow is a command that resolves to a different program than the one just
// installed, because that program is earlier on the PATH.
type Shadow struct {
	Command string
	// Folder is where the program that wins is.
	Folder string
}

// shadowedCommands lists the commands that resolve to a file outside dir.
func shadowedCommands(cmds []command, dir string, lookPath func(string) (string, error)) []Shadow {
	var shadowed []Shadow
	for _, c := range cmds {
		found, err := lookPath(c.file)
		if err != nil {
			continue
		}
		if folder := filepath.Dir(found); !strings.EqualFold(filepath.Clean(folder), filepath.Clean(dir)) {
			shadowed = append(shadowed, Shadow{Command: c.name, Folder: folder})
		}
	}
	return shadowed
}

// piece is a program written beside its destination.
type piece struct {
	temp   string
	digest string
}

// writeBeside copies each program to a temporary file in dir, or removes all
// it wrote and fails.
func writeBeside(ctx context.Context, st *Staged, dir string, cmds []command) (map[string]piece, error) {
	written := map[string]piece{}
	discard := func() {
		for _, p := range written {
			_ = os.Remove(p.temp)
		}
	}
	for _, c := range cmds {
		if err := ctx.Err(); err != nil {
			discard()
			return nil, fmt.Errorf("install: %w", err)
		}
		temp := filepath.Join(dir, c.file+"."+randomSuffix()+newSuffix)
		digest, err := copyFile(filepath.Join(st.Root, filepath.FromSlash(c.member)), temp, 0o755)
		if err != nil {
			discard()
			return nil, err
		}
		written[c.file] = piece{temp: temp, digest: digest}
	}
	return written, nil
}

// swapIn replaces each command's file with the one written beside it, and
// removes the commands of the previous version that this one does not have.
func swapIn(dir string, cmds []command, written map[string]piece, previous *Record) (err error) {
	var asides []swap
	var placed []string
	defer func() {
		if err != nil {
			restored := restoreTools(placed, asides) == nil
			for _, p := range written {
				_ = os.Remove(p.temp)
			}
			err = &ReplaceError{Restored: restored, Err: err}
			return
		}
		for _, s := range asides {
			_ = removeFile(s.aside) // Fails while the program runs; CleanLeftovers tries again.
		}
	}()
	moveAside := func(file string) error {
		dst := filepath.Join(dir, file)
		if _, statErr := os.Lstat(dst); statErr != nil {
			return nil //nolint:nilerr // Nothing there to move.
		}
		aside := dst + "." + randomSuffix() + asideSuffix
		if err := renameFile(dst, aside); err != nil {
			return fmt.Errorf("move %s aside: %w", file, err)
		}
		asides = append(asides, swap{aside: aside, original: dst})
		return nil
	}
	for _, c := range cmds {
		if err := moveAside(c.file); err != nil {
			return err
		}
		dst := filepath.Join(dir, c.file)
		if err := renameFile(written[c.file].temp, dst); err != nil {
			return fmt.Errorf("put %s in place: %w", c.file, err)
		}
		placed = append(placed, dst)
	}
	if previous != nil {
		for _, f := range previous.Files {
			if slices.ContainsFunc(cmds, func(c command) bool { return strings.EqualFold(c.file, f.Path) }) {
				continue
			}
			if _, ok := local(dir, f.Path); !ok {
				continue
			}
			if err := moveAside(f.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

// restoreTools undoes the swaps of a failed install.
func restoreTools(placed []string, asides []swap) error {
	var errs []error
	for _, p := range slices.Backward(placed) {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	for _, s := range slices.Backward(asides) {
		if err := renameFile(s.aside, s.original); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RemoveTool deletes a Tool's command files. A program in use is renamed aside
// instead, and deleted later. The PATH is left as it is.
func RemoveTool(rec Record) (RemoveResult, error) {
	var res RemoveResult
	var errs []error
	for _, f := range rec.Files {
		file, ok := local(rec.Folder, f.Path)
		if !ok {
			continue
		}
		err := os.Remove(file)
		switch {
		case err == nil:
			res.Removed++
		case errors.Is(err, fs.ErrNotExist):
		default:
			// On Windows a program that runs cannot be deleted but can be renamed.
			aside := file + "." + randomSuffix() + asideSuffix
			if renameErr := renameFile(file, aside); renameErr != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", f.Path, err))
				continue
			}
			_ = removeFile(aside)
			res.Removed++
		}
	}
	return res, errors.Join(errs...)
}

// CleanLeftovers deletes the programs earlier installs renamed aside while they
// ran, and half-written ones, from the tools folder. Failing to delete one that
// still runs is expected.
func CleanLeftovers(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), asideSuffix) || strings.HasSuffix(e.Name(), newSuffix)) {
			_ = removeFile(filepath.Join(dir, e.Name()))
		}
	}
}

// randomSuffix is eight random hex characters, for names that must not collide.
func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
