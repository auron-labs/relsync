package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/platform"
)

// The reasons a Portable app cannot be moved to a folder.
var (
	// ErrNotMovable is an install that is not a Portable app: an Installer
	// install lives where its setup program put it, and a Tool in the tools folder.
	ErrNotMovable = errors.New("only a portable app can be moved")
	// ErrMoveMissing is an app whose folder is not there.
	ErrMoveMissing = errors.New("the app's folder cannot be found")
	// ErrMoveSameFolder is the folder the app is in already.
	ErrMoveSameFolder = errors.New("the app is in that folder already")
	// ErrMoveInsideSource is a folder inside the one the app is in.
	ErrMoveInsideSource = errors.New("that folder is inside the app's own")
	// ErrMoveNotEmpty is a folder that already holds something.
	ErrMoveNotEmpty = errors.New("that folder is not empty")
	// ErrMoveNoSpace is a drive without room for the app.
	ErrMoveNoSpace = errors.New("there is not enough free space there")
)

// copyForMove copies one file; tests replace it to make a step fail.
var copyForMove = copyFile

// MoveRequest is a Portable app and the folder it is to be moved to.
type MoveRequest struct {
	Record Record
	// Destination is the folder the app's files go in, empty or not yet made.
	Destination string
	// Protected are the folders too broad to put an app in.
	Protected []string
	Platform  platform.Platform
}

// MoveResult is what a move did.
type MoveResult struct {
	// Record is the app's install record, with its new folder.
	Record Record
	// Left is how many of the originals could not be deleted. The app is safe
	// in its new place and only leftovers remain in the old one.
	Left int
	// Shortcuts is how many shortcuts were pointed at the new folder.
	Shortcuts int
	// Warning is a problem that does not undo the move: shortcuts that could
	// not be repointed.
	Warning error
}

// moveFiles is what a move takes: the files, and the folders to make, as
// paths relative to the app's folder written with "/", and how many bytes.
type moveFiles struct {
	files, dirs []string
	bytes       int64
	// whole says the app's whole folder moves, so the old folder goes too.
	whole bool
}

// CheckMove says why the app cannot be moved to the folder, nil when it can.
// It does not change anything.
func CheckMove(req MoveRequest) error {
	_, err := planMove(req)
	return err
}

func planMove(req MoveRequest) (moveFiles, error) {
	rec := req.Record
	if rec.Method != MethodPortable {
		return moveFiles{}, ErrNotMovable
	}
	if info, err := os.Stat(rec.Folder); err != nil || !info.IsDir() {
		return moveFiles{}, fmt.Errorf("%w: %s", ErrMoveMissing, rec.Folder)
	}
	to := filepath.Clean(req.Destination)
	if err := ValidateFolder(to, req.Protected); err != nil {
		return moveFiles{}, err
	}
	from := filepath.Clean(rec.Folder)
	switch {
	case strings.EqualFold(from, to):
		return moveFiles{}, ErrMoveSameFolder
	case strings.HasPrefix(strings.ToLower(to)+string(filepath.Separator), strings.ToLower(from)+string(filepath.Separator)):
		return moveFiles{}, ErrMoveInsideSource
	}
	if entries, err := os.ReadDir(to); err == nil && len(entries) > 0 {
		return moveFiles{}, ErrMoveNotEmpty
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return moveFiles{}, fmt.Errorf("read %s: %w", to, err)
	}
	m, err := listMove(rec)
	if err != nil {
		return moveFiles{}, err
	}
	if free, err := req.Platform.FreeSpace(nearestExisting(to)); err == nil && free < uint64(m.bytes) { // #nosec G115 -- sizes are never negative.
		return moveFiles{}, ErrMoveNoSpace
	}
	return m, nil
}

// nearestExisting is the folder that holds the path, or its first parent that
// exists, which is where a folder not yet made would be written.
func nearestExisting(dir string) string {
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// listMove lists what moves. A folder the application made for the app moves
// whole, with whatever the app saved in it; a folder the person chose is
// shared with their own files, so only the files of the install move.
func listMove(rec Record) (moveFiles, error) {
	m := moveFiles{whole: rec.Dedicated}
	if !rec.Dedicated {
		for _, f := range rec.Files {
			path, ok := local(rec.Folder, f.Path)
			if !ok {
				continue
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			m.files = append(m.files, f.Path)
			m.bytes += info.Size()
		}
		return m, nil
	}
	err := filepath.WalkDir(rec.Folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(rec.Folder, path)
		if relErr != nil {
			return fmt.Errorf("locate %s: %w", path, relErr)
		}
		if rel == "." {
			return nil
		}
		switch {
		case d.IsDir():
			m.dirs = append(m.dirs, filepath.ToSlash(rel))
		case d.Type().IsRegular():
			info, infoErr := d.Info()
			if infoErr != nil {
				return fmt.Errorf("read %s: %w", path, infoErr)
			}
			m.files = append(m.files, filepath.ToSlash(rel))
			m.bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return moveFiles{}, fmt.Errorf("list the files of %s: %w", rec.Folder, err)
	}
	return m, nil
}

// MovePortable moves a Portable app to another folder without risking it:
// every file is copied and compared with its original first, and the originals
// are only deleted once all of them are safely in place. A failure, or ctx
// ending, before that deletes the copies and leaves the app exactly where it
// was. After the move, shortcuts on the Desktop, in the Start menu and on the
// taskbar are pointed at the new place. The new folder is the person's own, so
// the record no longer says the application made it: removing the app removes
// its files and leaves the folder.
func MovePortable(ctx context.Context, req MoveRequest) (MoveResult, error) {
	m, err := planMove(req)
	if err != nil {
		return MoveResult{}, err
	}
	rec := req.Record
	to := filepath.Clean(req.Destination)
	if err := copyAll(ctx, rec.Folder, to, m); err != nil {
		return MoveResult{}, err
	}
	// The copies are all in place: only now do the originals go.
	res := MoveResult{Left: deleteOriginals(rec.Folder, m)}
	res.Record = rec
	res.Record.Folder, res.Record.Dedicated = to, false
	res.Shortcuts, err = req.Platform.RetargetShortcuts(ctx, rec.Folder, to)
	if err != nil && !errors.Is(err, platform.ErrNotSupported) {
		res.Warning = err
	}
	return res, nil
}

// copyAll copies what moves into to and compares each copy with its original.
// On failure it removes what it made: to was empty or missing, so whatever is
// in it is the copy's.
func copyAll(ctx context.Context, from, to string, m moveFiles) (err error) {
	created := false
	if _, statErr := os.Stat(to); errors.Is(statErr, fs.ErrNotExist) {
		created = true
	}
	defer func() {
		if err == nil {
			return
		}
		if created {
			_ = os.RemoveAll(to)
			return
		}
		entries, _ := os.ReadDir(to)
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(to, e.Name()))
		}
	}()
	if err := os.MkdirAll(to, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", to, err)
	}
	for _, rel := range m.dirs {
		if err := os.MkdirAll(filepath.Join(to, filepath.FromSlash(rel)), 0o750); err != nil {
			return fmt.Errorf("create the folder %s: %w", rel, err)
		}
	}
	for _, rel := range m.files {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("move: %w", err)
		}
		src, dst := filepath.Join(from, filepath.FromSlash(rel)), filepath.Join(to, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return fmt.Errorf("create the folder of %s: %w", rel, err)
		}
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		want, err := copyForMove(src, dst, info.Mode())
		if err != nil {
			return err
		}
		if got, err := fileSHA256(dst); err != nil || got != want {
			return errors.Join(fmt.Errorf("%s did not copy correctly", rel), err)
		}
	}
	return nil
}

// deleteOriginals deletes the files that were copied and the folders that
// leaves empty, and returns how many files could not be deleted. A folder the
// application made goes too, once it is empty.
func deleteOriginals(folder string, m moveFiles) (left int) {
	for _, rel := range m.files {
		path := filepath.Join(folder, filepath.FromSlash(rel))
		if err := removeCopied(path); err != nil {
			left++
		}
	}
	dirs := slices.Clone(m.dirs)
	for _, rel := range m.files {
		for dir := slashDir(rel); dir != "."; dir = slashDir(dir) {
			dirs = append(dirs, dir)
		}
	}
	// The deepest folders first, so each is empty by the time it is tried.
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	for _, rel := range slices.Compact(dirs) {
		_ = os.Remove(filepath.Join(folder, filepath.FromSlash(rel))) // Fails, harmlessly, while anything is in it.
	}
	if m.whole {
		_ = os.Remove(folder) // A whole-folder move leaves nothing behind it.
	}
	return left
}

// slashDir is the folder part of a path written with "/".
func slashDir(rel string) string {
	i := strings.LastIndex(rel, "/")
	if i < 0 {
		return "."
	}
	return rel[:i]
}

// removeCopied deletes a file, clearing the read-only mark that keeps Windows
// from deleting it.
func removeCopied(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	err = fmt.Errorf("delete %s: %w", path, err)
	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

// OwnedFiles lists the files of a Portable install that are the application's
// to copy: all the files of a folder the application made, which holds what
// the app saved there too, and only the installed files of a folder the person
// chose, which is shared with their own. Paths are relative to the folder and
// written with "/". It also reports their size in bytes.
func OwnedFiles(rec Record) (files []string, size int64, err error) {
	m, err := listMove(rec)
	return m.files, m.bytes, err
}
