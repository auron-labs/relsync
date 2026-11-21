package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aaronflorey/release-sync/selection"
)

// Staged is a release unpacked into a staging folder, ready to be applied. The
// staging folder is made beside the destination, so the final step is a move
// on one volume.
type Staged struct {
	// Root is the folder holding the release's files. When everything in the
	// archive sat under one top-level folder, it is that folder's contents.
	Root string
	dir  string
}

// Stage unpacks a verified download into a new staging folder inside parent. A
// standalone program is copied under its own name. Kind says what the file is;
// only archives and standalone programs can be staged.
func Stage(ctx context.Context, file, asset string, kind selection.ArtifactKind, parent string) (*Staged, error) {
	if kind != selection.ArtifactArchive && kind != selection.ArtifactStandaloneExe {
		return nil, fmt.Errorf("%w: %s is a %s", ErrUnsupportedFormat, asset, kind)
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", parent, err)
	}
	dir, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return nil, fmt.Errorf("create staging folder: %w", err)
	}
	s := &Staged{dir: dir, Root: dir}
	if kind == selection.ArtifactStandaloneExe {
		_, err = copyFile(file, filepath.Join(dir, SafeName(asset)), 0o755)
	} else {
		err = Extract(ctx, file, asset, dir)
	}
	if err != nil {
		return nil, errors.Join(err, s.Discard())
	}
	s.Root = withoutWrapper(dir)
	return s, nil
}

// withoutWrapper is the single folder everything in dir sits under, or dir.
func withoutWrapper(dir string) string {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(dir, entries[0].Name())
	}
	return dir
}

// Discard deletes the staging folder and whatever is left in it.
func (s *Staged) Discard() error {
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("delete staging folder: %w", err)
	}
	return nil
}

// Files lists the staged files, as paths relative to Root written with "/".
func (s *Staged) Files() ([]string, error) {
	var files []string
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, relErr := filepath.Rel(s.Root, p)
			if relErr != nil {
				return fmt.Errorf("locate %s: %w", p, relErr)
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list staged files: %w", err)
	}
	return files, nil
}

// copyFile copies src to a new file dst and returns the digest of what it
// wrote. It fails when dst exists.
func copyFile(src, dst string, mode fs.FileMode) (digest string, err error) {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filepath.Base(src), err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(filepath.Clean(dst), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm()|0o600) // #nosec G302 -- Programs must stay executable.
	if err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Base(dst), err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("write %s: %w", filepath.Base(dst), closeErr)
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, sum), in); err != nil {
		return "", fmt.Errorf("copy %s: %w", filepath.Base(src), err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// fileSHA256 is the digest of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = f.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// local is a path made from a recorded relative path, or false when it is not a
// plain path below the folder. Recorded paths are trusted no further than that.
func local(folder, rel string) (string, bool) {
	native := filepath.FromSlash(rel)
	if rel == "" || strings.Contains(rel, "\\") || !filepath.IsLocal(native) {
		return "", false
	}
	return filepath.Join(folder, native), true
}

// StageFolder unpacks a zip of a Portable app's folder into a new staging
// folder inside parent. Unlike Stage it keeps the layout exactly as zipped,
// even when everything sits under one folder, because the archive is of the
// folder itself. It applies the same rules as every unpacking: an entry that
// would leave the folder fails the whole unpacking with ErrUnsafeEntry.
func StageFolder(ctx context.Context, archive, parent string) (*Staged, error) {
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", parent, err)
	}
	dir, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return nil, fmt.Errorf("create staging folder: %w", err)
	}
	s := &Staged{dir: dir, Root: dir}
	if err := Extract(ctx, archive, "folder.zip", dir); err != nil {
		return nil, errors.Join(err, s.Discard())
	}
	return s, nil
}
