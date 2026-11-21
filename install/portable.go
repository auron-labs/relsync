package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/platform"
)

// PortableRequest is what a Portable install needs to know.
type PortableRequest struct {
	// Repo names the repository, for finding the main program.
	Repo string
	// Asset is the release file the staged files came from.
	Asset string
	// Folder is where the app lives, chosen by the caller: it is recorded and
	// never worked out again.
	Folder string
	// Dedicated says Folder was made for the Item in the apps folder.
	Dedicated bool
	// Previous is the record being replaced, nil for a fresh install.
	Previous *Record
	// ShortcutPath is where to make a Start menu shortcut, empty for none.
	ShortcutPath string
	// ShortcutText is the shortcut's description.
	ShortcutText string
	Platform     platform.Platform
}

// renameFile moves a file or folder; tests replace it to make a step fail.
var renameFile = os.Rename

// ApplyPortable puts the staged release in the Item's folder. Into an empty or
// missing folder the staged files are moved in whole; into a folder that has
// the previous version in it, each file the new version ships replaces the old
// one and each file only the old version shipped is removed, with anything
// else in the folder left alone. A failure puts the previous version back. The
// replacement itself is not interrupted by ctx.
func ApplyPortable(ctx context.Context, st *Staged, req PortableRequest) (Outcome, error) {
	files, err := st.Files()
	if err != nil {
		return Outcome{}, err
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, fmt.Errorf("install: %w", err)
	}
	main, hasMain := MainExecutable(st.Root, files, req.Repo)
	var digest string
	if hasMain {
		if digest, err = fileSHA256(filepath.Join(st.Root, filepath.FromSlash(main))); err != nil {
			return Outcome{}, err
		}
	}
	var previous []File
	if req.Previous != nil {
		previous = req.Previous.Files
	}
	if err := place(st.Root, req.Folder, files, previous); err != nil {
		return Outcome{}, err
	}
	rec := Record{
		Method: MethodPortable, Asset: req.Asset, Folder: req.Folder, MainExe: main, Dedicated: req.Dedicated, SHA256: digest,
		Files: make([]File, len(files)),
	}
	for i, f := range files {
		rec.Files[i] = File{Path: f}
	}
	if req.Previous != nil {
		rec.Shortcut = req.Previous.Shortcut
	}
	out := Outcome{Record: rec}
	if req.ShortcutPath != "" && hasMain {
		err := req.Platform.CreateShortcut(platform.Shortcut{
			Path: req.ShortcutPath, Target: filepath.Join(req.Folder, filepath.FromSlash(main)), Description: req.ShortcutText,
		})
		if err != nil {
			out.Warning = err
		} else {
			out.Record.Shortcut = req.ShortcutPath
		}
	}
	return out, nil
}

// place moves the staged files into folder.
func place(root, folder string, files []string, previous []File) error {
	if empty(folder) {
		if err := os.MkdirAll(filepath.Dir(folder), 0o750); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(folder), err)
		}
		// An empty folder is in the way of the move; one that is not is handled below.
		if err := os.Remove(folder); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("replace %s: %w", folder, err)
		}
		if err := renameFile(root, folder); err != nil {
			return fmt.Errorf("move into %s: %w", folder, err)
		}
		return nil
	}
	return replaceFiles(root, folder, files, previous)
}

// empty reports a folder that is missing or holds nothing.
func empty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err != nil && os.IsNotExist(err) || err == nil && len(entries) == 0
}

// swap is a file that was replaced: where it is now and where it belongs.
type swap struct{ aside, original string }

// replaceFiles is the file-by-file update. Everything it overwrites or
// removes is moved aside first, into a folder beside the app's, and moved back
// if any step fails.
func replaceFiles(root, folder string, files []string, previous []File) (err error) {
	backup, err := os.MkdirTemp(filepath.Dir(folder), ".replaced-")
	if err != nil {
		return &ReplaceError{Err: fmt.Errorf("make room for the old files: %w", err)}
	}
	var asides []swap
	var placed []string
	defer func() {
		if err == nil {
			_ = os.RemoveAll(backup) // Leftovers are only wasted space.
			return
		}
		restored := rollBack(placed, asides, folder) == nil
		if restored {
			_ = os.RemoveAll(backup)
		}
		err = &ReplaceError{Restored: restored, Err: err}
	}()

	touched := slices.Clone(files)
	for _, f := range previous {
		touched = append(touched, f.Path)
	}
	for _, rel := range touched {
		path, ok := local(folder, rel)
		if !ok {
			continue
		}
		if _, statErr := os.Lstat(path); statErr != nil {
			continue
		}
		aside := filepath.Join(backup, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(aside), 0o750); err != nil {
			return fmt.Errorf("move %s aside: %w", rel, err)
		}
		if err := renameFile(path, aside); err != nil {
			return fmt.Errorf("move %s aside: %w", rel, err)
		}
		asides = append(asides, swap{aside: aside, original: path})
	}
	for _, rel := range files {
		dst, ok := local(folder, rel)
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnsafeEntry, rel)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return fmt.Errorf("create the folder of %s: %w", rel, err)
		}
		if err := renameFile(filepath.Join(root, filepath.FromSlash(rel)), dst); err != nil {
			return fmt.Errorf("put %s in place: %w", rel, err)
		}
		placed = append(placed, dst)
	}
	dropEmptyFolders(folder, previous, files)
	return nil
}

// rollBack removes the files placed and moves the ones set aside back.
func rollBack(placed []string, asides []swap, folder string) error {
	var errs []error
	for _, p := range slices.Backward(placed) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	for _, s := range slices.Backward(asides) {
		if err := os.MkdirAll(filepath.Dir(s.original), 0o750); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := renameFile(s.aside, s.original); err != nil {
			errs = append(errs, err)
		}
	}
	// Folders made for the new files go with them.
	for _, p := range placed {
		removeEmptyParents(filepath.Dir(p), folder)
	}
	return errors.Join(errs...)
}

// dropEmptyFolders removes the folders a file only the previous version shipped
// leaves empty.
func dropEmptyFolders(folder string, previous []File, shipped []string) {
	for _, f := range previous {
		if slices.Contains(shipped, f.Path) {
			continue
		}
		if path, ok := local(folder, f.Path); ok {
			removeEmptyParents(filepath.Dir(path), folder)
		}
	}
}

// removeEmptyParents removes dir and its parents while they are empty, never
// the root itself.
func removeEmptyParents(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// RemoveResult is what removing an install did.
type RemoveResult struct {
	// Removed is how many files were deleted.
	Removed int
	// Left is how many other files are still in the folder of a custom install.
	Left int
	// Warning is a problem that does not stop the removal, such as a shortcut
	// that could not be deleted.
	Warning error
}

// RemovePortable removes a Portable install. A dedicated folder is deleted
// with whatever the program saved in it; in any other folder only the files
// the install put there are deleted, and the folders they leave empty. The
// shortcut goes either way. A folder the user could not have meant to give the
// application is refused.
func RemovePortable(rec Record, protected []string, p platform.Platform) (RemoveResult, error) {
	var res RemoveResult
	if err := ValidateFolder(rec.Folder, protected); err != nil {
		return res, err
	}
	if rec.Dedicated {
		res.Removed = len(rec.Files)
		if err := os.RemoveAll(rec.Folder); err != nil {
			return RemoveResult{}, fmt.Errorf("delete %s: %w", rec.Folder, err)
		}
	} else {
		removed, err := removeRecorded(rec)
		if err != nil {
			return RemoveResult{Removed: removed}, err
		}
		res.Removed, res.Left = removed, countFiles(rec.Folder)
	}
	if rec.Shortcut != "" {
		res.Warning = p.RemoveShortcut(rec.Shortcut)
	}
	return res, nil
}

// removeRecorded deletes the recorded files of a folder and the folders that
// leaves empty.
func removeRecorded(rec Record) (int, error) {
	var removed int
	var errs []error
	for _, f := range rec.Files {
		path, ok := local(rec.Folder, f.Path)
		if !ok {
			continue
		}
		err := os.Remove(path)
		switch {
		case err == nil:
			removed++
			removeEmptyParents(filepath.Dir(path), rec.Folder)
		case !os.IsNotExist(err):
			errs = append(errs, fmt.Errorf("delete %s: %w", f.Path, err))
		}
	}
	return removed, errors.Join(errs...)
}

func countFiles(dir string) int {
	var n int
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}
