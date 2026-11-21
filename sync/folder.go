package sync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// fileName is the name of the sync file.
const fileName = "library-sync.json"

// Folder keeps the text in one file of a folder that something else, a file
// sync service, copies between the devices.
type Folder struct {
	file string
}

var _ Transport = (*Folder)(nil)

// NewFolder returns the Folder that keeps its file in the sub-folder app of
// root. The sub-folder is made on the first write.
func NewFolder(root, app string) *Folder {
	return &Folder{file: filepath.Join(root, app, fileName)}
}

// File is the path of the sync file.
func (f *Folder) File() string { return f.file }

// Read returns the file's text, and false when there is no file yet.
func (f *Folder) Read(context.Context) (string, bool, error) {
	text, err := os.ReadFile(f.file)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the sync file: %w", err)
	}
	return string(text), true, nil
}

// Write replaces the file. The text goes to a temporary file beside it first
// and is renamed into place, so another device never reads half of it.
func (f *Folder) Write(_ context.Context, text string) (err error) {
	dir := filepath.Dir(f.file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make the sync folder: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".library-sync-*.tmp")
	if err != nil {
		return fmt.Errorf("write the sync file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(temp.Name())
		}
	}()
	if _, err := temp.WriteString(text); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write the sync file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write the sync file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write the sync file: %w", err)
	}
	if err := os.Rename(temp.Name(), f.file); err != nil {
		return fmt.Errorf("replace the sync file: %w", err)
	}
	return nil
}

// Watch calls changed whenever the sync file is created, written or replaced,
// until ctx ends, which is how a change another device made arrives without
// polling. It makes the folder if it does not exist, and returns an error when
// the folder cannot be watched.
func (f *Folder) Watch(ctx context.Context, changed func()) error {
	dir := filepath.Dir(f.file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make the sync folder: %w", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watch the sync folder: %w", err)
	}
	// The directory is watched rather than the file, as replacing the file
	// ends the watch of the old one.
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("watch the sync folder: %w", err)
	}
	go func() {
		defer func() { _ = watcher.Close() }()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Clean(event.Name) == f.file && event.Has(fsnotify.Create|fsnotify.Write|fsnotify.Rename) {
					changed()
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return nil
}
