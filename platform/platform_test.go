package platform

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSignatureStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status string
		signer string
		want   Signature
	}{
		{"Valid", "Example Corp", Signature{SignatureTrusted, "Example Corp"}},
		{"NotSigned", "", Signature{SignatureUnsigned, ""}},
		{"HashMismatch", "Example Corp", Signature{SignatureInvalid, "Example Corp"}},
		{"NotTrusted", "Example Corp", Signature{SignatureUntrusted, "Example Corp"}},
		{"UnknownError", "", Signature{SignatureUntrusted, ""}},
		{"NotSupportedFileFormat", "", Signature{SignatureNotChecked, ""}},
		{"SomethingNew", "", Signature{SignatureUntrusted, ""}},
	} {
		if got := signatureFromStatus(tc.status, tc.signer); got != tc.want {
			t.Errorf("%s = %+v, want %+v", tc.status, got, tc.want)
		}
	}
}

func TestPathWithEntry(t *testing.T) {
	for _, tc := range []struct {
		name, path, dir, want string
		changed               bool
	}{
		{"empty path", "", `C:\Tools`, `C:\Tools`, true},
		{"appended", `C:\A;C:\B`, `C:\Tools`, `C:\A;C:\B;C:\Tools`, true},
		{"trailing semicolon is not doubled", `C:\A;`, `C:\Tools`, `C:\A;C:\Tools`, true},
		{"already there", `C:\A;C:\Tools`, `C:\Tools`, `C:\A;C:\Tools`, false},
		{"already there in another case, with a slash", `C:\A;c:\tools\`, `C:\Tools`, `C:\A;c:\tools\`, false},
		{"a longer folder is not the folder", `C:\A;C:\Tools\bin`, `C:\Tools`, `C:\A;C:\Tools\bin;C:\Tools`, true},
	} {
		got, changed := pathWithEntry(tc.path, tc.dir)
		if got != tc.want || changed != tc.changed {
			t.Errorf("%s: pathWithEntry(%q, %q) = %q, %v; want %q, %v", tc.name, tc.path, tc.dir, got, changed, tc.want, tc.changed)
		}
	}
}

func TestInsideIgnoresCaseAndRequiresAFolderBoundary(t *testing.T) {
	sep := string(filepath.Separator)
	dir := sep + "Apps" + sep + "tool"
	for path, want := range map[string]bool{
		dir + sep + "tool.exe":               true,
		dir + sep + "bin" + sep + "tool.exe": true,
		strings.ToUpper(dir) + sep + "a.exe": true,
		dir + "-other" + sep + "tool.exe":    false,
		dir:                                  false,
	} {
		if got := inside(dir, path); got != want {
			t.Errorf("inside(%q, %q) = %v, want %v", dir, path, got, want)
		}
	}
}

func TestStubReportsUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the system has an implementation")
	}
	p := Current()
	if _, err := p.AddToUserPath("/tools"); !errors.Is(err, ErrNotSupported) {
		t.Errorf("AddToUserPath = %v", err)
	}
	if _, err := p.Folders(); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Folders = %v", err)
	}
	if _, err := p.ListShortcuts(t.Context()); !errors.Is(err, ErrNotSupported) {
		t.Errorf("ListShortcuts = %v", err)
	}
	if _, err := p.Winget(t.Context(), "--version"); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Winget = %v", err)
	}
	if _, err := p.VersionInfo("/tool"); !errors.Is(err, ErrNotSupported) {
		t.Errorf("VersionInfo = %v", err)
	}
}

func TestProgramFromKeepsOnlyWhatAPersonInstalled(t *testing.T) {
	base := uninstallValues{DisplayName: " Example ", UninstallString: `"C:\Example\unins000.exe"`, DisplayVersion: "1.2"}
	for _, tc := range []struct {
		name   string
		change func(*uninstallValues)
		shown  bool
	}{
		{"ordinary program", func(*uninstallValues) {}, true},
		{"only a quiet command to remove it", func(v *uninstallValues) { v.UninstallString, v.QuietUninstallString = "", "x /S" }, true},
		{"no name", func(v *uninstallValues) { v.DisplayName = "  " }, false},
		{"nothing to run to remove it", func(v *uninstallValues) { v.UninstallString = "" }, false},
		{"system component", func(v *uninstallValues) { v.SystemComponent = true }, false},
		{"part of another entry", func(v *uninstallValues) { v.ParentKeyName = "OperatingSystem" }, false},
		{"an update", func(v *uninstallValues) { v.ReleaseType = "Security Update" }, false},
		{"a hotfix", func(v *uninstallValues) { v.ReleaseType = "Hotfix" }, false},
		{"another release type", func(v *uninstallValues) { v.ReleaseType = "Application" }, true},
	} {
		v := base
		tc.change(&v)
		got, ok := programFrom("key", ScopeUser, v)
		if ok != tc.shown {
			t.Errorf("%s: shown = %v, want %v", tc.name, ok, tc.shown)
		}
		if ok && (got.Name != strings.TrimSpace(v.DisplayName) || got.Key != "key" || got.Scope != ScopeUser) {
			t.Errorf("%s: program = %+v", tc.name, got)
		}
	}
}

func TestProgramFromTrimsTheInstallLocation(t *testing.T) {
	got, _ := programFrom("k", ScopeMachine, uninstallValues{
		DisplayName: "X", UninstallString: "u", InstallLocation: ` "C:\Apps\X" `,
	})
	if got.InstallLocation != `C:\Apps\X` {
		t.Errorf("install location = %q", got.InstallLocation)
	}
}

func TestProgramFromKeepsTheEntrysLinks(t *testing.T) {
	got, _ := programFrom("k", ScopeUser, uninstallValues{
		DisplayName: "X", UninstallString: "u", URLInfoAbout: " https://github.com/acme/x ", URLUpdateInfo: "https://example.com/update",
		HelpLink: "https://example.com/help", Comments: " Built from https://github.com/acme/x ",
	})
	if got.AboutURL != "https://github.com/acme/x" || got.UpdateURL != "https://example.com/update" ||
		got.HelpLink != "https://example.com/help" || got.Comments != "Built from https://github.com/acme/x" {
		t.Errorf("links = %+v", got)
	}
}

func TestParseShortcuts(t *testing.T) {
	got, err := parseShortcuts([]byte(`[{"path":"C:\\Users\\a\\Desktop\\Tool.lnk","target":"D:\\tool\\tool.exe","description":"A tool"},` +
		`{"path":"C:\\x.lnk","target":"","description":""}]`))
	if err != nil || len(got) != 2 || got[0].Target != `D:\tool\tool.exe` || got[0].Description != "A tool" || got[1].Target != "" {
		t.Fatalf("shortcuts = %+v, %v", got, err)
	}
	if got, err := parseShortcuts([]byte(`[]`)); err != nil || len(got) != 0 {
		t.Errorf("empty list = %+v, %v", got, err)
	}
	if _, err := parseShortcuts([]byte(`not json`)); err == nil {
		t.Error("garbage was accepted")
	}
}
