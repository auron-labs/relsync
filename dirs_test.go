package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDataDirHoldsEverything(t *testing.T) {
	got, err := resolveDirs("/tmp/isolated")
	if err != nil {
		t.Fatal(err)
	}
	if got.config != "/tmp/isolated" || got.cache != "/tmp/isolated" || got.downloads != filepath.Join("/tmp/isolated", "downloads") {
		t.Fatalf("dirs = %+v", got)
	}
}

func TestDataDirHoldsInstalledAppsToo(t *testing.T) {
	data := filepath.Join(t.TempDir(), "isolated")
	got, err := resolveDirs(data)
	if err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"apps": got.apps, "tools": got.tools, "staging": got.staging} {
		if filepath.Dir(dir) != data {
			t.Errorf("%s = %s, want it directly under the data directory", name, dir)
		}
	}
}

func TestDefaultInstallFoldersAreInTheUsersProgramsFolder(t *testing.T) {
	got, err := resolveDirs("")
	if err != nil {
		t.Fatal(err)
	}
	if got.apps == got.tools || filepath.Dir(got.apps) != filepath.Dir(got.tools) || filepath.Base(filepath.Dir(got.apps)) != "Programs" {
		t.Errorf("apps = %s, tools = %s", got.apps, got.tools)
	}
	if filepath.Dir(got.staging) != got.cache {
		t.Errorf("staging = %s is not under the cache directory %s", got.staging, got.cache)
	}
}

func TestDefaultDirsAreNamedAfterTheProduct(t *testing.T) {
	got, err := resolveDirs("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.config) != appSlug || filepath.Base(got.cache) != appSlug || got.config == got.cache {
		t.Fatalf("dirs = %+v", got)
	}
}

func TestDefaultDownloadsLiveUnderTheCacheDirectory(t *testing.T) {
	got, err := resolveDirs("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got.downloads) != got.cache {
		t.Fatalf("downloads %s are not directly under the cache directory %s", got.downloads, got.cache)
	}
}

// The packager reads mygo.json, not appName, so the two must agree for the
// installed name and URL scheme to follow the product-name constant.
func TestPackagingConfigMatchesProductName(t *testing.T) {
	data, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Name       string   `json:"name"`
		URLSchemes []string `json:"urlSchemes"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Name != appName {
		t.Errorf("mygo.json name = %q, want %q", config.Name, appName)
	}
	if !slices.Contains(config.URLSchemes, appSlug) {
		t.Errorf("mygo.json urlSchemes = %q, want %q", config.URLSchemes, appSlug)
	}
}
