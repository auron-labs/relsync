package fetch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// markerName is left in every folder this package creates for a release,
	// so housekeeping deletes only what the application downloaded even when
	// the download folder is one the user also keeps files in.
	markerName = ".fetch-folder"
	// StalePartAge is how old an unfinished download must be to be removed.
	StalePartAge = 7 * 24 * time.Hour
)

// DestPath is where a release file is stored under root:
// {owner}-{repo}/{tag}/{file name}. Tags and file names come from the site and
// may hold characters a path cannot, so each part is made safe to use.
func DestPath(root, owner, repo, tag, name string) string {
	return filepath.Join(root, safeSegment(owner+"-"+repo), safeSegment(tag), safeSegment(name))
}

// safeSegment makes s usable as one path element on every supported system.
func safeSegment(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	// A trailing dot or space is dropped by Windows, which would make ".." mean the parent.
	s = strings.TrimRight(s, ". ")
	if s == "" {
		return "_"
	}
	return s
}

// markFolder records that dir holds downloads made by this package.
func markFolder(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, markerName), nil, 0o600); err != nil {
		return local("mark download folder", err)
	}
	return nil
}

// Cleanup is the startup housekeeping of the download folder root. It removes
// unfinished downloads older than StalePartAge and, unless keepFinished,
// finished downloads, then the folders left empty. It touches only folders
// this package made, whatever else root holds. Failing to remove one file
// does not stop it removing the rest; the failures are returned together.
func Cleanup(root string, keepFinished bool, now time.Time) error {
	owners, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list download folder: %w", err)
	}
	var errs []error
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		ownerDir := filepath.Join(root, owner.Name())
		tags, err := os.ReadDir(ownerDir)
		if err != nil {
			errs = append(errs, fmt.Errorf("list %s: %w", ownerDir, err))
			continue
		}
		for _, tag := range tags {
			if tag.IsDir() {
				errs = append(errs, cleanFolder(filepath.Join(ownerDir, tag.Name()), keepFinished, now)...)
			}
		}
		// Fails, harmlessly, while anything is left in it.
		_ = os.Remove(ownerDir)
	}
	return errors.Join(errs...)
}

// cleanFolder tidies one release folder, if this package made it.
func cleanFolder(dir string, keepFinished bool, now time.Time) []error {
	if _, err := os.Stat(filepath.Join(dir, markerName)); err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []error{fmt.Errorf("list %s: %w", dir, err)}
	}
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if name == markerName || entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, name)
		if !shouldRemove(path, name, keepFinished, now) {
			continue
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	// The marker alone does not keep the folder.
	if remaining, err := os.ReadDir(dir); err == nil && len(remaining) == 1 && remaining[0].Name() == markerName {
		_ = os.Remove(filepath.Join(dir, markerName))
		_ = os.Remove(dir)
	}
	return errs
}

func shouldRemove(path, name string, keepFinished bool, now time.Time) bool {
	// A validator is as old as the .part file it belongs to, which is
	// rewritten as the download goes; the validator itself is not.
	age := path
	if strings.HasSuffix(name, partSuffix+validatorSuffix) {
		age = strings.TrimSuffix(path, validatorSuffix)
	} else if !strings.HasSuffix(name, partSuffix) {
		return !keepFinished
	}
	info, err := os.Stat(age)
	if errors.Is(err, os.ErrNotExist) {
		return age != path // a validator with no .part file is useless
	}
	return err == nil && now.Sub(info.ModTime()) > StalePartAge
}
