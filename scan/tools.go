package scan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Locations are where other managers keep their records of what they installed.
type Locations struct {
	// BinConfig is the path of bin's configuration file; empty for none.
	BinConfig string
	// ScoopRoots are the folders Scoop is installed in, for the user and for everyone.
	ScoopRoots []string
}

// DefaultLocations finds the records the way the managers themselves do.
func DefaultLocations() Locations {
	home, _ := os.UserHomeDir()
	l := Locations{BinConfig: binConfigPath(home)}
	user := os.Getenv("SCOOP")
	if user == "" && home != "" {
		user = filepath.Join(home, "scoop")
	}
	global := os.Getenv("SCOOP_GLOBAL")
	if programData := os.Getenv("ProgramData"); global == "" && programData != "" {
		global = filepath.Join(programData, "scoop")
	}
	for _, root := range []string{user, global} {
		if root != "" {
			l.ScoopRoots = append(l.ScoopRoots, root)
		}
	}
	return l
}

// binConfigPath is where bin keeps its configuration: BIN_CONFIG, a legacy
// folder in the home folder when it is there, then the system's own place. It
// is empty when none can be worked out.
func binConfigPath(home string) string {
	if path := os.Getenv("BIN_CONFIG"); path != "" {
		return path
	}
	if home == "" {
		return ""
	}
	legacy := filepath.Join(home, ".bin", "config.json")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "bin", "config.json")
		}
		return legacy
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "bin", "config.json")
	}
	return filepath.Join(home, ".config", "bin", "config.json")
}

// ReadBin reads the tools bin installed, from its configuration file. A tool
// with a GitHub address as its source is linked by it. A configuration that is
// not there has no tools.
func ReadBin(path string) ([]Candidate, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the bin configuration: %w", err)
	}
	var config struct {
		Bins map[string]struct {
			Path       string `json:"path"`
			RemoteName string `json:"remote_name"`
			Version    string `json:"version"`
			URL        string `json:"url"`
		} `json:"bins"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("read the bin configuration %s: %w", path, err)
	}
	var found []Candidate
	for key, b := range config.Bins {
		file := b.Path
		if file == "" {
			file = key
		}
		name := b.RemoteName
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		}
		found = append(found, Candidate{
			Key: "bin:" + strings.ToLower(file), Origin: OriginBin, Name: name, Version: b.Version,
			Folder: filepath.Dir(file), MainExe: filepath.Base(file), Texts: []string{b.URL},
		}.linkedByText())
	}
	sortByName(found)
	return found, nil
}

// ReadScoop reads the apps Scoop installed under root, from their manifests:
// the version, the homepage and the source its version checks use. A root that
// is not there has no apps; an app whose manifest cannot be read is passed over.
func ReadScoop(root string) ([]Candidate, error) {
	apps, err := os.ReadDir(filepath.Join(root, "apps"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Scoop apps: %w", err)
	}
	var found []Candidate
	for _, app := range apps {
		if !app.IsDir() || app.Name() == "scoop" {
			continue
		}
		dir := filepath.Join(root, "apps", app.Name(), "current")
		data, err := os.ReadFile(filepath.Join(dir, "manifest.json")) //nolint:gosec // dir is a folder of Scoop's own.
		if err != nil {
			continue
		}
		var manifest struct {
			Version  string          `json:"version"`
			Homepage string          `json:"homepage"`
			Checkver json.RawMessage `json:"checkver"`
			URL      json.RawMessage `json:"url"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			continue
		}
		found = append(found, Candidate{
			Key: "scoop:" + strings.ToLower(filepath.Join(root, app.Name())), Origin: OriginScoop, Name: app.Name(),
			Version: manifest.Version, Folder: dir,
			Texts: []string{string(manifest.Checkver), manifest.Homepage, string(manifest.URL)},
		}.linkedByText())
	}
	sortByName(found)
	return found, nil
}
