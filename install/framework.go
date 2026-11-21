package install

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Framework is the toolkit a setup program was built with, which decides the
// switches that make it install quietly and the one that names its folder.
type Framework string

// The frameworks setup programs are recognised by.
const (
	// FrameworkUnknown is a setup program built with none of the others; it is
	// run with its own interface.
	FrameworkUnknown Framework = ""
	FrameworkNSIS    Framework = "nsis"
	FrameworkInno    Framework = "inno"
	// FrameworkWiX is a WiX bundle: a setup program that chains installers.
	FrameworkWiX      Framework = "wix"
	FrameworkSquirrel Framework = "squirrel"
)

// markerPrefix bounds how much of a file is read for framework markers. The
// markers sit in the toolkit's own stub or in the program's headers, ahead of
// the payload.
const markerPrefix = 8 << 20

// markers are what each framework leaves in its setup programs, tried in order.
var markers = []struct {
	framework Framework
	text      []byte
}{
	{FrameworkNSIS, []byte("Nullsoft")},
	{FrameworkInno, []byte("Inno Setup")},
	// WiX's engine names the section that carries the bundle's payload.
	{FrameworkWiX, []byte(".wixburn")},
	{FrameworkSquirrel, []byte("Squirrel.Windows")},
	{FrameworkSquirrel, []byte("--squirrel-install")},
}

// DetectFramework reads the start of a program for the markers of a known
// setup toolkit. It never runs the file.
func DetectFramework(path string) (Framework, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return FrameworkUnknown, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = f.Close() }()
	head, err := io.ReadAll(io.LimitReader(f, markerPrefix))
	if err != nil {
		return FrameworkUnknown, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	for _, m := range markers {
		if bytes.Contains(head, m.text) {
			return m.framework, nil
		}
	}
	return FrameworkUnknown, nil
}

// silentArgs are the switches that make the framework's setup program, or
// uninstaller, run without asking anything. A program of no known framework has
// none: it shows its own interface.
func (f Framework) silentArgs() string {
	switch f {
	case FrameworkNSIS:
		return "/S"
	case FrameworkInno:
		return "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP-"
	case FrameworkWiX:
		return "/quiet /norestart"
	case FrameworkSquirrel:
		return "--silent"
	case FrameworkUnknown:
	}
	return ""
}

// folderArg is the switch that names the install folder, empty for a framework
// that has none. NSIS reads everything after /D= as the folder, so it is last
// and unquoted.
func (f Framework) folderArg(dir string) string {
	dir = strings.TrimRight(dir, `\/`)
	switch f {
	case FrameworkNSIS:
		return "/D=" + dir
	case FrameworkInno:
		return `/DIR="` + dir + `"`
	case FrameworkWiX, FrameworkSquirrel, FrameworkUnknown:
	}
	return ""
}
