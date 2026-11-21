package install

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronflorey/release-sync/platform"
)

var deckCraft = Identity{Owner: "acme", Repo: "deckcraft", Title: "DeckCraft"}

func TestMatchScore(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    platform.Program
		want int
	}{
		{"name equal to the repository", platform.Program{Name: "DeckCraft"}, 100},
		{"name equal to the repository ignoring punctuation", platform.Program{Name: "Deck-Craft"}, 100},
		{"name containing the repository", platform.Program{Name: "DeckCraft Studio 2"}, 70},
		{"name that is part of the repository's", platform.Program{Name: "Deck"}, 70},
		{"unrelated", platform.Program{Name: "Other Tool"}, 0},
		{"a publisher that matches the owner", platform.Program{Name: "DeckCraft", Publisher: "Acme Inc."}, 120},
		{"an install location holding the repository's name", platform.Program{Name: "DeckCraft", InstallLocation: `C:\Program Files\DeckCraft`}, 110},
		{"all of it", platform.Program{Name: "DeckCraft", Publisher: "ACME", InstallLocation: `C:\Apps\deckcraft`}, 130},
		{"a publisher alone is nothing", platform.Program{Name: "Other", Publisher: "Acme"}, 0},
		{"too short a name is not contained", platform.Program{Name: "De"}, 0},
	} {
		if got := MatchScore(tc.p, deckCraft); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	titled := Identity{Owner: "acme", Repo: "dc", Title: "Deck Craft Pro"}
	if got := MatchScore(platform.Program{Name: "Deck Craft Pro 2.0"}, titled); got != 65 {
		t.Errorf("name containing the Item's name = %d, want 65", got)
	}
	if got := MatchScore(platform.Program{Name: ""}, Identity{}); got != 0 {
		t.Errorf("empty names matched with %d", got)
	}
}

func TestFindNewTakesTheOnlyChangedEntryAndTheBestOfSeveral(t *testing.T) {
	old := platform.Program{Key: "old", Name: "Old", Version: "1"}
	updated := platform.Program{Key: "old", Name: "Old", Version: "2"}
	deck := platform.Program{Key: "deck", Name: "DeckCraft", Version: "1"}
	other := platform.Program{Key: "other", Name: "Something Else", Version: "9"}

	if got, ok := FindNew([]platform.Program{old}, []platform.Program{old}, deckCraft); ok {
		t.Errorf("nothing changed, found %+v", got)
	}
	// One candidate is taken, whatever it is called.
	if got, ok := FindNew([]platform.Program{old}, []platform.Program{old, other}, deckCraft); !ok || got.Key != "other" {
		t.Errorf("the only new entry = %+v, %v", got, ok)
	}
	if got, ok := FindNew([]platform.Program{old}, []platform.Program{updated}, deckCraft); !ok || got.Version != "2" {
		t.Errorf("an entry whose version changed = %+v, %v", got, ok)
	}
	if got, ok := FindNew([]platform.Program{old}, []platform.Program{old, other, deck}, deckCraft); !ok || got.Key != "deck" {
		t.Errorf("the best match of several = %+v, %v", got, ok)
	}
	// With nothing to tell them apart the choice does not depend on the order listed.
	a, b := platform.Program{Key: "a", Name: "A"}, platform.Program{Key: "b", Name: "B"}
	for _, after := range [][]platform.Program{{a, b}, {b, a}} {
		if got, _ := FindNew(nil, after, deckCraft); got.Key != "a" {
			t.Errorf("tie broken as %q for %v", got.Key, after)
		}
	}
}

func TestMainProgramIsTheIconFileWhenThatIsAProgram(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "bin", "deckcraft.exe"), "x")
	write(t, filepath.Join(dir, "unins000.exe"), "x")

	icon := filepath.Join(dir, "bin", "deckcraft.exe")
	for _, tc := range []struct {
		name         string
		p            platform.Program
		folder, main string
	}{
		{"icon inside the install location, with an icon index", platform.Program{InstallLocation: dir, DisplayIcon: `"` + icon + `",0`}, dir, "bin/deckcraft.exe"},
		{"icon outside the install location", platform.Program{InstallLocation: t.TempDir(), DisplayIcon: icon}, filepath.Dir(icon), "deckcraft.exe"},
		{"icon and no install location", platform.Program{DisplayIcon: icon + ",-1"}, filepath.Dir(icon), "deckcraft.exe"},
		{"the uninstaller as the icon is not the program", platform.Program{InstallLocation: dir, DisplayIcon: filepath.Join(dir, "unins000.exe")}, dir, "bin/deckcraft.exe"},
		{"an icon that is not a program", platform.Program{InstallLocation: dir, DisplayIcon: filepath.Join(dir, "app.ico")}, dir, "bin/deckcraft.exe"},
		{"an icon file that is missing", platform.Program{InstallLocation: dir, DisplayIcon: filepath.Join(dir, "gone.exe")}, dir, "bin/deckcraft.exe"},
	} {
		folder, main := MainProgram(tc.p, "deckcraft")
		if folder != tc.folder || main != tc.main {
			t.Errorf("%s: %q, %q; want %q, %q", tc.name, folder, main, tc.folder, tc.main)
		}
	}
}

func TestMainProgramFallsBackToTheLocationOrNothing(t *testing.T) {
	empty := t.TempDir()
	if folder, main := MainProgram(platform.Program{InstallLocation: empty}, "x"); folder != empty || main != "" {
		t.Errorf("a location with no program: %q, %q", folder, main)
	}
	if folder, main := MainProgram(platform.Program{}, "x"); folder != "" || main != "" {
		t.Errorf("no icon and no location: %q, %q", folder, main)
	}
	deep := t.TempDir()
	write(t, filepath.Join(deep, "a", "b", "c", "d", "deep.exe"), "x")
	if _, main := MainProgram(platform.Program{InstallLocation: deep}, "deep"); main != "" {
		t.Errorf("a program four folders down was found: %q", main)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptingSoftwareThatIsThereAlreadyRecordsWhereItIsAndHowItIsRemoved(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "deckcraft.exe")
	write(t, exe, "x")

	entry := AdoptEntry(platform.Program{Key: "u|dc", Scope: platform.ScopeUser, Version: "2.1", InstallLocation: dir, DisplayIcon: exe}, "deckcraft")
	if entry.Method != MethodInstaller || entry.UninstallKey != "u|dc" || entry.Scope != platform.ScopeUser || entry.EntryVersion != "2.1" ||
		entry.Folder != dir || entry.MainExe != "deckcraft.exe" || entry.Asset != "" || !entry.Removable() {
		t.Errorf("entry = %+v", entry)
	}

	located := AdoptLocated(exe)
	if located.Method != MethodInstaller || located.UninstallKey != "" || located.Folder != dir || located.MainExe != "deckcraft.exe" || located.Removable() {
		t.Errorf("located = %+v", located)
	}

	portable := AdoptPortable(dir, "deckcraft.exe")
	if portable.Method != MethodPortable || portable.Folder != dir || portable.MainExe != "deckcraft.exe" || portable.Dedicated || len(portable.Files) != 0 {
		t.Errorf("portable = %+v", portable)
	}
}

func TestIsAuxiliaryRecognisesProgramsThatAreNotTheApplication(t *testing.T) {
	for name, want := range map[string]bool{
		"unins000.exe": true, "Uninstall.exe": true, "setup.exe": true, "installer.exe": true, "CrashHandler.exe": true,
		"updater.exe": true, "vc_redist.x64.exe": true, "helper.exe": true, "deckcraft.exe": false, "app-portable.exe": false,
		`C:\Apps\Tool\tool.exe`: false,
	} {
		if got := IsAuxiliary(name); got != want {
			t.Errorf("IsAuxiliary(%q) = %v, want %v", name, got, want)
		}
	}
}
