package install

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronflorey/release-sync/install/installtest"
)

func writeProgram(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFrameworkIsRecognisedFromTheMarkersInTheFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		want  Framework
	}{
		{"NSIS", "Nullsoft Install System v3.08", FrameworkNSIS},
		{"Inno Setup", "Inno Setup Setup Data (6.2.2)", FrameworkInno},
		{"WiX bundle", ".wixburn", FrameworkWiX},
		{"Squirrel", "Squirrel.Windows", FrameworkSquirrel},
		{"a program of no known toolkit", "just a program", FrameworkUnknown},
		{"no text at all", "", FrameworkUnknown},
	} {
		path := writeProgram(t, "setup.exe", installtest.GUIExe(tc.extra))
		got, err := DetectFramework(path)
		if err != nil || got != tc.want {
			t.Errorf("%s: framework = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestFrameworkMarkersBeyondTheInspectedPrefixAreNotRead(t *testing.T) {
	body := append(installtest.GUIExe(""), bytes.Repeat([]byte{0}, markerPrefix)...)
	body = append(body, "Nullsoft"...)
	if got, err := DetectFramework(writeProgram(t, "big.exe", body)); err != nil || got != FrameworkUnknown {
		t.Errorf("framework = %q, %v; a marker past the prefix must not count", got, err)
	}
}

func TestDetectingAFrameworkInAMissingFileFails(t *testing.T) {
	if _, err := DetectFramework(filepath.Join(t.TempDir(), "gone.exe")); err == nil {
		t.Error("no error for a file that is not there")
	}
}
