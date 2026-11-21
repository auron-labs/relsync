// Package installtest builds the small files tests of installs need: Windows
// programs the install package recognises by their bytes, and zip archives.
package installtest

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"sort"
)

// The Windows subsystems a program can be built for.
const (
	subsystemGUI     = 2
	subsystemConsole = 3
)

// image is a minimal 64-bit Windows program with no sections. Different extra
// text makes different programs.
func image(subsystem uint16, extra string) []byte {
	const (
		peOffset     = 0x40
		optionalSize = 240
	)
	b := make([]byte, peOffset+4+20+optionalSize, peOffset+4+20+optionalSize+len(extra))
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], peOffset)
	copy(b[peOffset:], "PE\x00\x00")
	file := b[peOffset+4:]
	binary.LittleEndian.PutUint16(file[0:], 0x8664) // x64.
	binary.LittleEndian.PutUint16(file[16:], optionalSize)
	binary.LittleEndian.PutUint16(file[18:], 0x0002) // Executable image.
	optional := file[20:]
	binary.LittleEndian.PutUint16(optional[0:], 0x20b) // PE32+.
	binary.LittleEndian.PutUint16(optional[68:], subsystem)
	binary.LittleEndian.PutUint32(optional[108:], 16) // Data directories.
	return append(b, extra...)
}

// ConsoleExe is a command-line program.
func ConsoleExe(extra string) []byte { return image(subsystemConsole, extra) }

// GUIExe is a graphical program.
func GUIExe(extra string) []byte { return image(subsystemGUI, extra) }

// Zip is an archive of the files, by path.
func Zip(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range names {
		f, err := w.Create(name)
		if err != nil {
			return nil, err //nolint:wrapcheck // A test helper.
		}
		if _, err := f.Write(files[name]); err != nil {
			return nil, err //nolint:wrapcheck // A test helper.
		}
	}
	if err := w.Close(); err != nil {
		return nil, err //nolint:wrapcheck // A test helper.
	}
	return buf.Bytes(), nil
}
