package install

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aaronflorey/release-sync/selection"

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// format is how an archive is read, from its file name.
type format struct {
	suffix string
	// open wraps the file in the decompressor of a compressed format; nil for
	// formats that are read by other means.
	open func(io.Reader) (io.Reader, error)
	// kind says what is inside once decompressed.
	kind contents
}

type contents int

const (
	zipArchive contents = iota
	sevenZipArchive
	tarArchive
	singleFile
)

var (
	gzipReader = func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }
	xzReader   = func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) }
	bzip2Read  = func(r io.Reader) (io.Reader, error) { return bzip2.NewReader(r), nil }
	zstdReader = func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) }
)

// formats are matched by suffix in order, so .tar.gz is tried before .gz.
var formats = []format{
	{".zip", nil, zipArchive},
	{".7z", nil, sevenZipArchive},
	{".tar.gz", gzipReader, tarArchive},
	{".tgz", gzipReader, tarArchive},
	{".tar.xz", xzReader, tarArchive},
	{".txz", xzReader, tarArchive},
	{".tar.bz2", bzip2Read, tarArchive},
	{".tbz2", bzip2Read, tarArchive},
	{".tar.zst", zstdReader, tarArchive},
	{".gz", gzipReader, singleFile},
	{".xz", xzReader, singleFile},
	{".bz2", bzip2Read, singleFile},
	{".zst", zstdReader, singleFile},
}

func formatOf(name string) (format, bool) {
	lower := strings.ToLower(name)
	for _, f := range formats {
		if strings.HasSuffix(lower, f.suffix) && len(lower) > len(f.suffix) {
			return f, true
		}
	}
	return format{}, false
}

// Extract unpacks the archive file into dest, which must exist. name is the
// download's file name, which tells the format. An entry whose path is
// absolute, has a drive prefix, leaves dest or uses a name Windows reserves
// fails the whole extraction with ErrUnsafeEntry; links are skipped.
func Extract(ctx context.Context, file, name, dest string) error {
	f, ok := formatOf(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedFormat, name)
	}
	x := extractor{ctx: ctx, dest: dest}
	var err error
	switch f.kind {
	case zipArchive:
		err = x.zip(file)
	case sevenZipArchive:
		err = x.sevenZip(file)
	case tarArchive:
		err = x.tar(file, f)
	case singleFile:
		err = x.single(file, name, f)
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnsafeEntry), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("unpack %s: %w", name, err)
	}
	return fmt.Errorf("unpack %s: %w: %w", name, ErrCorruptArchive, err)
}

// extractor writes archive entries below dest.
type extractor struct {
	ctx  context.Context //nolint:containedctx // Scoped to one extraction call, and read by the copy loops below it.
	dest string
}

// cleanEntry turns an archive path into a relative path with "/" separators,
// or fails with ErrUnsafeEntry. The empty path, as for the archive's root
// folder, is valid and is not an entry.
func cleanEntry(name string) (string, error) {
	unsafe := fmt.Errorf("%w: %q", ErrUnsafeEntry, name)
	slashed := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(slashed, "/") {
		return "", unsafe
	}
	var parts []string
	for part := range strings.SplitSeq(slashed, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", unsafe
		}
		if selection.ValidateFileName(part) != nil {
			return "", unsafe
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "/"), nil
}

// entry is one thing in an archive.
type entry struct {
	name string
	dir  bool
	// link marks a symbolic or hard link, which is not extracted.
	link bool
	// other marks anything that is neither file, folder nor link.
	other bool
	mode  fs.FileMode
	open  func() (io.ReadCloser, error)
}

// write extracts one entry.
func (x extractor) write(e entry) error {
	if e.link || e.other {
		return nil
	}
	rel, err := cleanEntry(e.name)
	if err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	target := filepath.Join(x.dest, filepath.FromSlash(rel))
	if e.dir {
		return mkdir(target)
	}
	if err := mkdir(filepath.Dir(target)); err != nil {
		return err
	}
	src, err := e.open()
	if err != nil {
		return fmt.Errorf("read %s: %w", e.name, err)
	}
	defer func() { _ = src.Close() }()
	return x.writeFile(target, e.mode, src)
}

func mkdir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create folder: %w", err)
	}
	return nil
}

// writeFile writes src to target, giving up when the context ends.
func (x extractor) writeFile(target string, mode fs.FileMode, src io.Reader) (err error) {
	// The file's own permissions, never less than the owner reading and writing it.
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm()|0o600) // #nosec G302 G304 -- The path was validated by cleanEntry and the mode comes from the archive.
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(target), err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("write %s: %w", filepath.Base(target), closeErr)
		}
	}()
	if _, err := io.Copy(out, contextReader{ctx: x.ctx, r: src}); err != nil { // #nosec G110 -- Reading stops when the context ends.
		return fmt.Errorf("write %s: %w", filepath.Base(target), err)
	}
	return nil
}

// contextReader stops a copy when its context ends.
type contextReader struct {
	ctx context.Context //nolint:containedctx // A reader has no other way to learn that the work was cancelled.
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, fmt.Errorf("unpack: %w", err)
	}
	return c.r.Read(p) //nolint:wrapcheck // Pass the underlying reader's errors through, io.EOF included.
}

func (x extractor) zip(file string) error {
	r, err := zip.OpenReader(file)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		mode := f.Mode()
		err := x.write(entry{
			name: f.Name, dir: f.FileInfo().IsDir(), link: mode&fs.ModeSymlink != 0,
			other: !mode.IsRegular() && !f.FileInfo().IsDir() && mode&fs.ModeSymlink == 0, mode: mode,
			open: f.Open,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (x extractor) sevenZip(file string) error {
	r, err := sevenzip.OpenReader(file)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		info := f.FileInfo()
		mode := info.Mode()
		err := x.write(entry{
			name: f.Name, dir: info.IsDir(), link: mode&fs.ModeSymlink != 0,
			other: !mode.IsRegular() && !info.IsDir() && mode&fs.ModeSymlink == 0, mode: mode,
			open: f.Open,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (x extractor) tar(file string, f format) error {
	in, err := os.Open(filepath.Clean(file))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = in.Close() }()
	decompressed, err := f.open(in)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if closer, ok := decompressed.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	} else if zr, ok := decompressed.(*zstd.Decoder); ok {
		defer zr.Close()
	}
	archive := tar.NewReader(decompressed)
	for {
		h, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		err = x.write(entry{
			name: h.Name, dir: h.Typeflag == tar.TypeDir, link: h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink,
			other: h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeSymlink && h.Typeflag != tar.TypeLink,
			mode:  h.FileInfo().Mode(),
			open:  func() (io.ReadCloser, error) { return io.NopCloser(archive), nil },
		})
		if err != nil {
			return err
		}
	}
}

// single decompresses a file that is one compressed file into dest, under the
// archive's name without its compression suffix.
func (x extractor) single(file, name string, f format) error {
	in, err := os.Open(filepath.Clean(file))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = in.Close() }()
	decompressed, err := f.open(in)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if zr, ok := decompressed.(*zstd.Decoder); ok {
		defer zr.Close()
	}
	inner := strings.TrimSuffix(path.Base(strings.ReplaceAll(name, "\\", "/")), name[len(name)-len(f.suffix):])
	return x.write(entry{
		name: inner, mode: 0o755,
		open: func() (io.ReadCloser, error) { return io.NopCloser(decompressed), nil },
	})
}
