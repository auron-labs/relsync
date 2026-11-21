package install

import (
	"path/filepath"
	"strings"

	"github.com/aaronflorey/release-sync/selection"
)

// SettleKind corrects the kind a file was given from its name alone, now that
// its bytes can be read. A name cannot tell a setup program from a program that
// merely runs, so an .exe that was provisionally a setup program stays one only
// when it is built with a known setup toolkit or its name says setup, installer
// or install; any other is a standalone program. The Item's install method,
// when it has one, overrides all of this.
func SettleKind(kind selection.ArtifactKind, method selection.Method, framework Framework, file string) selection.ArtifactKind {
	if kind != selection.ArtifactSetupExe {
		return kind
	}
	switch method {
	case selection.MethodInstaller:
		return selection.ArtifactSetupExe
	case selection.MethodPortable:
		return selection.ArtifactStandaloneExe
	case selection.MethodAutomatic:
	}
	if framework != FrameworkUnknown || selection.NamesInstaller(file) {
		return selection.ArtifactSetupExe
	}
	return selection.ArtifactStandaloneExe
}

// SoleInstaller finds the installer in an unpacked archive whose only program
// is one MSI package or one setup program, which is installed rather than put
// in a folder. It reports false for any other archive, and always for an Item
// limited to the Portable method.
func SoleInstaller(st *Staged, method selection.Method) (file string, kind selection.ArtifactKind, ok bool, err error) {
	if method == selection.MethodPortable {
		return "", "", false, nil
	}
	files, err := st.Files()
	if err != nil {
		return "", "", false, err
	}
	var programs []string
	for _, f := range files {
		if ext := strings.ToLower(filepath.Ext(f)); ext == ".msi" || ext == ".exe" {
			programs = append(programs, f)
		}
	}
	if len(programs) != 1 {
		return "", "", false, nil
	}
	file = filepath.Join(st.Root, filepath.FromSlash(programs[0]))
	if strings.EqualFold(filepath.Ext(file), ".msi") {
		return file, selection.ArtifactMSI, true, nil
	}
	framework, err := DetectFramework(file)
	if err != nil {
		return "", "", false, err
	}
	if SettleKind(selection.ArtifactSetupExe, method, framework, filepath.Base(file)) != selection.ArtifactSetupExe {
		return "", "", false, nil
	}
	return file, selection.ArtifactSetupExe, true, nil
}
