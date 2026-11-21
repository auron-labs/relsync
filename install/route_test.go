package install

import (
	"path/filepath"
	"testing"

	"github.com/aaronflorey/release-sync/selection"
)

func TestSettleKind(t *testing.T) {
	const (
		setup      = selection.ArtifactSetupExe
		standalone = selection.ArtifactStandaloneExe
	)
	for _, tc := range []struct {
		name      string
		kind      selection.ArtifactKind
		method    selection.Method
		framework Framework
		file      string
		want      selection.ArtifactKind
	}{
		{"a recognised toolkit is a setup program", setup, selection.MethodAutomatic, FrameworkNSIS, "app.exe", setup},
		{"a name that says setup is one, with no toolkit", setup, selection.MethodAutomatic, FrameworkUnknown, "app-setup.exe", setup},
		{"a name that says installer is one", setup, selection.MethodAutomatic, FrameworkUnknown, "app-installer-x64.exe", setup},
		{"a plain program is standalone", setup, selection.MethodAutomatic, FrameworkUnknown, "app-x64.exe", standalone},
		{"the Installer method overrides a plain program", setup, selection.MethodInstaller, FrameworkUnknown, "app.exe", setup},
		{"the Portable method overrides a setup program", setup, selection.MethodPortable, FrameworkNSIS, "app-setup.exe", standalone},
		{"an MSI is left alone", selection.ArtifactMSI, selection.MethodPortable, FrameworkUnknown, "app.msi", selection.ArtifactMSI},
		{"an archive is left alone", selection.ArtifactArchive, selection.MethodInstaller, FrameworkUnknown, "app.zip", selection.ArtifactArchive},
	} {
		if got := SettleKind(tc.kind, tc.method, tc.framework, tc.file); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func stagedFiles(t *testing.T, f files) *Staged {
	t.Helper()
	root := t.TempDir()
	f.write(t, root)
	return &Staged{Root: root, dir: root}
}

func TestSoleInstallerIsTheOnlyProgramOfAnArchive(t *testing.T) {
	nsis := guiExe("Nullsoft Install System")
	for _, tc := range []struct {
		name   string
		files  files
		method selection.Method
		want   string
		kind   selection.ArtifactKind
	}{
		{"one MSI", files{"app/app.msi": "x", "app/readme.txt": "x"}, selection.MethodAutomatic, "app/app.msi", selection.ArtifactMSI},
		{"one setup program", files{"app-setup.exe": nsis, "license.txt": "x"}, selection.MethodAutomatic, "app-setup.exe", selection.ArtifactSetupExe},
		{"one program that is a setup only by its name", files{"install.exe": guiExe("")}, selection.MethodAutomatic, "install.exe", selection.ArtifactSetupExe},
		{"one plain program is the app itself", files{"app.exe": guiExe(""), "data.dll": "x"}, selection.MethodAutomatic, "", ""},
		{"one plain program, when the Item says installer", files{"app.exe": guiExe("")}, selection.MethodInstaller, "app.exe", selection.ArtifactSetupExe},
		{"a setup program and the program itself", files{"setup.exe": nsis, "app.exe": guiExe("")}, selection.MethodAutomatic, "", ""},
		{"an MSI and a setup program", files{"app.msi": "x", "setup.exe": nsis}, selection.MethodAutomatic, "", ""},
		{"nothing to run", files{"readme.txt": "x"}, selection.MethodAutomatic, "", ""},
		{"an MSI, when the Item is limited to Portable", files{"app.msi": "x"}, selection.MethodPortable, "", ""},
	} {
		file, kind, ok, err := SoleInstaller(stagedFiles(t, tc.files), tc.method)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		wantPath := ""
		if tc.want != "" {
			wantPath = filepath.Base(tc.want)
		}
		if ok != (tc.want != "") || kind != tc.kind || (ok && filepath.Base(file) != wantPath) {
			t.Errorf("%s: %q, %q, %v; want %q, %q", tc.name, file, kind, ok, tc.want, tc.kind)
		}
	}
}
