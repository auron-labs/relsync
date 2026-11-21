package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aaronflorey/release-sync/selection"
)

// ValidateFolder refuses a folder that is too broad to manage: a drive root, a
// protected folder or a folder that holds one, as C:\Users holds a profile.
// The application never fills or empties one.
func ValidateFolder(dir string, protected []string) error {
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || filepath.Dir(clean) == clean {
		return fmt.Errorf("%w: %s", ErrTooBroad, dir)
	}
	within := strings.ToLower(clean) + string(filepath.Separator)
	for _, p := range protected {
		if strings.HasPrefix(strings.ToLower(filepath.Clean(p))+string(filepath.Separator), within) {
			return fmt.Errorf("%w: %s", ErrTooBroad, dir)
		}
	}
	return nil
}

// DefaultFolder is the folder an Item gets in the apps folder: named for the
// repository, or for the repository and its owner when taken says another
// Item has the first name.
func DefaultFolder(apps, owner, repo string, taken func(dir string) bool) string {
	dir := filepath.Join(apps, SafeName(repo))
	if taken != nil && taken(dir) {
		return filepath.Join(apps, SafeName(repo+"-"+owner))
	}
	return dir
}

// CustomFolder is the folder to install into when the user chose dir: dir
// itself, or a folder named for the repository inside it when dir already
// holds something. It refuses a folder that is too broad.
func CustomFolder(dir, repo string, protected []string) (string, error) {
	if err := ValidateFolder(dir, protected); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return filepath.Join(dir, SafeName(repo)), nil
	}
	return filepath.Clean(dir), nil
}

// SafeName makes a repository, owner or Item name usable as a file or folder
// name.
func SafeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < ' ' || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	if name == "" || selection.ValidateFileName(name) != nil {
		return "_" + name
	}
	return name
}
