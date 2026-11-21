package install

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/selection"
)

var sampleFiles = files{"sample-1.0/bin/hello.txt": "hello from the archive\n", "sample-1.0/README.md": "readme\n"}

func TestExtractReadsEveryFormat(t *testing.T) {
	wantTree := []string{"sample-1.0/README.md", "sample-1.0/bin/hello.txt"}
	tarball := tarOf(t, sampleFiles)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"sample.zip", zipOf(t, sampleFiles)},
		{"sample.7z", fixture(t, "sample.7z")},
		{"sample.tar.gz", gzipOf(t, tarball)},
		{"sample.tgz", gzipOf(t, tarball)},
		{"sample.tar.xz", xzOf(t, tarball)},
		{"sample.txz", xzOf(t, tarball)},
		{"sample.tar.bz2", fixture(t, "sample.tar.bz2")},
		{"sample.tar.zst", zstdOf(t, tarball)},
		{"SAMPLE.ZIP", zipOf(t, sampleFiles)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			if err := Extract(t.Context(), download(t, tc.name, tc.data), tc.name, dest); err != nil {
				t.Fatal(err)
			}
			if got := onDisk(t, dest); !slices.Equal(got, wantTree) {
				t.Fatalf("unpacked %v, want %v", got, wantTree)
			}
			if got := read(t, filepath.Join(dest, "sample-1.0", "bin", "hello.txt")); got != "hello from the archive\n" {
				t.Errorf("hello.txt = %q", got)
			}
		})
	}
}

func TestExtractDecompressesASingleFile(t *testing.T) {
	const content = "single file contents\n"
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"single.txt.gz", gzipOf(t, []byte(content))},
		{"single.txt.xz", xzOf(t, []byte(content))},
		{"single.txt.bz2", fixture(t, "single.txt.bz2")},
		{"single.txt.zst", zstdOf(t, []byte(content))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			if err := Extract(t.Context(), download(t, tc.name, tc.data), tc.name, dest); err != nil {
				t.Fatal(err)
			}
			if got := onDisk(t, dest); !slices.Equal(got, []string{"single.txt"}) {
				t.Fatalf("unpacked %v", got)
			}
			if got := read(t, filepath.Join(dest, "single.txt")); got != content {
				t.Errorf("content = %q", got)
			}
		})
	}
}

func TestExtractRefusesUnsafeEntries(t *testing.T) {
	for _, name := range []string{
		"../evil.txt", "a/../../evil.txt", "/etc/evil.txt", `\evil.txt`, `..\evil.txt`, `C:\evil.txt`, "C:evil.txt",
		"dir/CON", "nul.txt", "COM1.log", "dir/aux", "trailing.", "space /file.txt", "a:b.txt", "pipe|name",
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			if err := os.Mkdir(dest, 0o750); err != nil {
				t.Fatal(err)
			}
			archive := download(t, "bad.zip", zipOf(t, files{"fine.txt": "ok", name: "evil"}))
			err := Extract(t.Context(), archive, "bad.zip", dest)
			if !errors.Is(err, ErrUnsafeEntry) {
				t.Fatalf("Extract = %v, want ErrUnsafeEntry", err)
			}
			if got := onDisk(t, parent); slices.ContainsFunc(got, func(p string) bool { return p == "evil.txt" || p == "dest/evil.txt" }) {
				t.Errorf("an entry was written: %v", got)
			}
		})
	}
}

func TestExtractRefusesUnsafeEntriesInTarballs(t *testing.T) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("no"))
	_ = w.Close()
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	if err := os.Mkdir(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	err := Extract(t.Context(), download(t, "bad.tar.gz", gzipOf(t, buf.Bytes())), "bad.tar.gz", dest)
	if !errors.Is(err, ErrUnsafeEntry) || exists(filepath.Join(parent, "escape.txt")) {
		t.Fatalf("Extract = %v; escaped file exists: %v", err, exists(filepath.Join(parent, "escape.txt")))
	}
}

func TestExtractSkipsLinks(t *testing.T) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, h := range []tar.Header{
		{Name: "tool.exe", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg},
		{Name: "symlink", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "tool.exe"},
	} {
		if err := w.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = w.Write([]byte("abc"))
		}
	}
	_ = w.Close()
	dest := t.TempDir()
	if err := Extract(t.Context(), download(t, "links.tar.gz", gzipOf(t, buf.Bytes())), "links.tar.gz", dest); err != nil {
		t.Fatal(err)
	}
	if got := onDisk(t, dest); !slices.Equal(got, []string{"tool.exe"}) {
		t.Errorf("unpacked %v, want only the file", got)
	}

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(0o777 | os.ModeSymlink)
	lw, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lw.Write([]byte("/etc/passwd"))
	_ = zw.Close()
	zdest := t.TempDir()
	if err := Extract(t.Context(), download(t, "links.zip", zbuf.Bytes()), "links.zip", zdest); err != nil {
		t.Fatal(err)
	}
	if got := onDisk(t, zdest); len(got) != 0 {
		t.Errorf("a zip link was extracted: %v", got)
	}
}

func TestExtractRefusesWhatItDoesNotRead(t *testing.T) {
	err := Extract(t.Context(), download(t, "tool.rar", []byte("x")), "tool.rar", t.TempDir())
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("Extract = %v, want ErrUnsupportedFormat", err)
	}
}

func TestExtractStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := Extract(ctx, download(t, "tool.zip", zipOf(t, files{"a.txt": "hello"})), "tool.zip", t.TempDir())
	if !errors.Is(err, ctx.Err()) {
		t.Errorf("Extract = %v, want the cancellation", err)
	}
}

func TestStageRemovesASingleTopLevelFolder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files files
		want  []string
	}{
		{"one folder", files{"tool-1.0/a.txt": "a", "tool-1.0/sub/b.txt": "b"}, []string{"a.txt", "sub/b.txt"}},
		{"two folders", files{"one/a.txt": "a", "two/b.txt": "b"}, []string{"one/a.txt", "two/b.txt"}},
		{"a folder and a file", files{"tool/a.txt": "a", "README": "r"}, []string{"README", "tool/a.txt"}},
		{"no folder", files{"a.txt": "a"}, []string{"a.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			st, err := Stage(t.Context(), download(t, "tool.zip", zipOf(t, tc.files)), "tool.zip", selection.ArtifactArchive, parent)
			if err != nil {
				t.Fatal(err)
			}
			got, err := st.Files()
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("Files = %v, %v; want %v", got, err, tc.want)
			}
			if err := st.Discard(); err != nil {
				t.Fatal(err)
			}
			if left := onDisk(t, parent); len(left) != 0 {
				t.Errorf("staging left %v", left)
			}
		})
	}
}

func TestStageCopiesAStandaloneProgramUnderItsName(t *testing.T) {
	st, err := Stage(t.Context(), download(t, "x.bin", []byte(consoleExe(""))), "tool-x64.exe", selection.ArtifactStandaloneExe, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Files(); !slices.Equal(got, []string{"tool-x64.exe"}) {
		t.Errorf("Files = %v", got)
	}
}

func TestStageKeepsAHostileAssetNameInsideTheStagingFolder(t *testing.T) {
	parent := t.TempDir()
	st, err := Stage(t.Context(), download(t, "x.bin", []byte(consoleExe(""))), `../../evil.exe`, selection.ArtifactStandaloneExe, parent)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := st.Files()
	if len(files) != 1 || strings.ContainsAny(files[0], `/\`) {
		t.Errorf("Files = %v, want one file directly in the staging folder", files)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(parent), "evil.exe")); err == nil {
		t.Error("a file was written outside the staging folder")
	}
}

func TestStageRefusesInstallers(t *testing.T) {
	_, err := Stage(t.Context(), download(t, "setup.exe", []byte(consoleExe(""))), "setup.exe", selection.ArtifactSetupExe, t.TempDir())
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("Stage = %v, want ErrUnsupportedFormat", err)
	}
}

func TestAFailedStageLeavesNoStagingFolder(t *testing.T) {
	parent := t.TempDir()
	_, err := Stage(t.Context(), download(t, "bad.zip", zipOf(t, files{"../x": "x"})), "bad.zip", selection.ArtifactArchive, parent)
	if !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("Stage = %v", err)
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Errorf("staging left %v", entries)
	}
}
