package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const appName = "Release Sync"

// appSlug derives the data-directory name and URL scheme from the product
// name, so renaming the product is a one-line change.
var appSlug = strings.ToLower(strings.ReplaceAll(appName, " ", "-"))

const (
	// downloadDirectory is where downloaded files go by default, inside the cache
	// directory. The Download folder setting overrides it.
	downloadDirectory = "downloads"
	// stagingDirectory is where releases are unpacked before a Tool's programs
	// are copied out of them, inside the cache directory.
	stagingDirectory = "staging"
	// appBackupDirectory, libraryBackupDirectory and restoreDirectory are inside
	// the cache directory, the configuration directory and the configuration
	// directory.
	appBackupDirectory     = "app-backups"
	libraryBackupDirectory = "library-backups"
	restoreDirectory       = "restore-pending"
)

// dirs is where the application keeps its files: the database and settings in
// config, downloads, logs and caches in cache. resolveDirs is the one place
// that decides.
type dirs struct {
	config string
	cache  string
	// downloads is where release files are downloaded unless the user chose a folder.
	downloads string
	// apps and tools are where portable apps and command-line tools are
	// installed unless the user chose folders.
	apps, tools string
	// staging is where releases are unpacked before they are put in place.
	staging string
	// appBackups holds the zips of Portable apps taken before they change.
	// They are large and replaceable, so they live in the cache directory.
	appBackups string
	// libraryBackups is where Library backups go unless the person chose a
	// folder. They are small and not replaceable, so they live beside the database.
	libraryBackups string
	// restore holds a Library backup that replaces the database at the next start.
	restore string
}

// resolveDirs returns the application's directories. An explicit directory
// from -data-dir holds everything, so an isolated instance touches nothing of
// the installed one.
func resolveDirs(dataDir string) (dirs, error) {
	if dataDir != "" {
		return dirs{
			config: dataDir, cache: dataDir, downloads: filepath.Join(dataDir, downloadDirectory),
			apps: filepath.Join(dataDir, "apps"), tools: filepath.Join(dataDir, "tools"), staging: filepath.Join(dataDir, stagingDirectory),
			appBackups: filepath.Join(dataDir, appBackupDirectory), libraryBackups: filepath.Join(dataDir, libraryBackupDirectory),
			restore: filepath.Join(dataDir, restoreDirectory),
		}, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return dirs{}, fmt.Errorf("locate configuration directory: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return dirs{}, fmt.Errorf("locate cache directory: %w", err)
	}
	cache := filepath.Join(cacheDir, appSlug)
	// On Windows the cache directory is the local application-data folder, so
	// "Programs" in it is where per-user programs are installed.
	programs := filepath.Join(cacheDir, "Programs")
	return dirs{
		config:     filepath.Join(configDir, appSlug),
		cache:      cache,
		downloads:  filepath.Join(cache, downloadDirectory),
		apps:       filepath.Join(programs, appName+" Apps"),
		tools:      filepath.Join(programs, appName+" Tools"),
		staging:    filepath.Join(cache, stagingDirectory),
		appBackups: filepath.Join(cache, appBackupDirectory), libraryBackups: filepath.Join(configDir, appSlug, libraryBackupDirectory),
		restore: filepath.Join(configDir, appSlug, restoreDirectory),
	}, nil
}
