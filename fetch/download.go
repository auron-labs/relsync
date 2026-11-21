// Package fetch downloads release files and decides whether they are safe to
// hand to an installer. Download streams a file to disk, resumes interrupted
// transfers and hashes it on the way; Verify weighs the hash against what the
// publisher posted and the file's code signature.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// partSuffix marks a file that is still being downloaded.
	partSuffix = ".part"
	// validatorSuffix names the file beside a .part file that records the
	// ETag or modification time of the bytes in it, so a later range request
	// can be refused by the server when the file has changed.
	validatorSuffix = ".validator"

	defaultStallTimeout     = 60 * time.Second
	defaultProgressInterval = 250 * time.Millisecond
	// maxRetries is how many times in a row a transient failure is retried.
	maxRetries = 3
	// maxRedirects matches the standard library's own limit.
	maxRedirects = 10
	bufferSize   = 128 << 10
)

// ErrStalled is the failure of a transfer that received nothing for too long.
var ErrStalled = errors.New("no data received")

// ErrSizeMismatch means the finished file is not the size the release declared.
var ErrSizeMismatch = errors.New("downloaded size does not match the release")

// ErrTooLarge means a response was bigger than the caller allowed.
var ErrTooLarge = errors.New("response is too large")

// StatusError is a response with a status the download cannot use.
type StatusError struct {
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("server answered %d %s", e.Status, http.StatusText(e.Status))
}

// LocalError is a failure on this computer's disk rather than on the network,
// such as a full disk. Retrying does not help and it is worth reporting.
type LocalError struct {
	Op  string
	Err error
}

func (e *LocalError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *LocalError) Unwrap() error { return e.Err }

func local(op string, err error) error { return &LocalError{Op: op, Err: err} }

// Request is one file to download.
type Request struct {
	URL string
	// Header is sent with the request, including any credentials. Credentials
	// are dropped when the server redirects to another host.
	Header http.Header
	// Dest is where the finished file goes. The download is written to
	// Dest + ".part" first.
	Dest string
	// Size is the size the release declared for the file, 0 when unknown.
	Size int64
}

// Progress is how far a download has got.
type Progress struct {
	Done int64
	// Total is the size of the file, 0 while unknown.
	Total          int64
	BytesPerSecond float64
}

// File describes a finished download.
type File struct {
	Path string
	Size int64
	// SHA256 is the file's digest in lower-case hex, computed while it was written.
	SHA256 string
}

// Options tune a Client. The zero value is what the application uses.
type Options struct {
	// StallTimeout is how long a transfer may receive nothing; default 60 seconds.
	StallTimeout time.Duration
	// Backoff is how long to wait before retry number n (from 0); default 1, 2 then 4 seconds.
	Backoff func(retry int) time.Duration
	// ProgressInterval is the least time between progress reports; default a quarter second.
	ProgressInterval time.Duration
}

// Client downloads files. It is safe for concurrent use.
type Client struct {
	http     *http.Client
	stall    time.Duration
	backoff  func(int) time.Duration
	interval time.Duration
}

// NewClient returns a Client. It sets no overall time limit: a large file may
// take as long as it takes, provided data keeps arriving.
func NewClient(o Options) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The bytes written to disk and hashed must be the bytes the server sent.
	transport.DisableCompression = true
	c := &Client{
		stall: o.StallTimeout, backoff: o.Backoff, interval: o.ProgressInterval,
		http: &http.Client{Transport: transport, CheckRedirect: checkRedirect},
	}
	if c.stall <= 0 {
		c.stall = defaultStallTimeout
	}
	if c.backoff == nil {
		c.backoff = func(retry int) time.Duration { return time.Second << retry }
	}
	if c.interval <= 0 {
		c.interval = defaultProgressInterval
	}
	return c
}

// checkRedirect follows redirects but never forwards credentials to another
// host. The standard library compares domains and ignores the port, which is
// not strict enough for a token.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

// Download fetches req.URL to req.Dest. An earlier interrupted download of the
// same file is continued when the server can prove the bytes are unchanged,
// and started over when it cannot. Failures that look temporary are retried
// up to three times, with a pause growing each time, resuming where they
// stopped. A cancelled download keeps its .part file for later.
func (c *Client) Download(ctx context.Context, req Request, onProgress func(Progress)) (File, error) {
	dir := filepath.Dir(req.Dest)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return File{}, local("create download folder", err)
	}
	if err := markFolder(dir); err != nil {
		return File{}, err
	}
	part := req.Dest + partSuffix
	progress := newProgress(onProgress, c.interval)
	name := filepath.Base(req.Dest)
	retries, furthest := 0, int64(0)
	for {
		out, err := c.attempt(ctx, req, part, progress)
		if err == nil {
			return finish(req.Dest, part, out)
		}
		if ctx.Err() != nil {
			return File{}, fmt.Errorf("download %s: %w", name, context.Cause(ctx))
		}
		if errors.Is(err, ErrSizeMismatch) {
			removePart(part)
		}
		// Retries count failures in a row: an attempt that got further than
		// any before starts the count again.
		if out.done > furthest {
			furthest, retries = out.done, 0
		}
		if !retryable(err) || retries == maxRetries {
			return File{}, fmt.Errorf("download %s: %w", name, err)
		}
		select {
		case <-time.After(c.backoff(retries)):
		case <-ctx.Done():
			return File{}, fmt.Errorf("download %s: %w", name, context.Cause(ctx))
		}
		retries++
	}
}

// Get reads a small response into memory, for a checksum file. It fails with
// ErrTooLarge when the body is longer than limit bytes.
func (c *Client) Get(ctx context.Context, req Request, limit int64) ([]byte, error) {
	hreq, err := newRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read %s: %w", req.URL, &StatusError{Status: resp.StatusCode})
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", req.URL, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("read %s: %w", req.URL, ErrTooLarge)
	}
	return data, nil
}

func newRequest(ctx context.Context, req Request) (*http.Request, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for name, values := range req.Header {
		hreq.Header[name] = append([]string(nil), values...)
	}
	return hreq, nil
}

// outcome is what one attempt achieved.
type outcome struct {
	// received counts bytes taken from the network in this attempt.
	received int64
	done     int64
	sum      string
}

// attempt makes one request, continuing the .part file when it can, and copies
// the body to disk until it ends.
func (c *Client) attempt(ctx context.Context, req Request, part string, progress *progress) (outcome, error) {
	attemptCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watchdog := time.AfterFunc(c.stall, func() { cancel(ErrStalled) })
	defer watchdog.Stop()

	offset, validator := resumePoint(part)
	hreq, err := newRequest(attemptCtx, req)
	if err != nil {
		return outcome{}, err
	}
	if offset > 0 {
		hreq.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		hreq.Header.Set("If-Range", validator)
	}
	resp, err := c.http.Do(hreq)
	if err != nil {
		return outcome{}, networkError(attemptCtx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	total := req.Size
	switch resp.StatusCode {
	case http.StatusOK:
		// A full response, either because nothing was resumed or because the
		// server could not honour the range.
		offset = 0
		if resp.ContentLength > 0 {
			total = resp.ContentLength
		}
		saveValidator(part, validatorOf(resp.Header))
	case http.StatusPartialContent:
		start, size, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != offset {
			removePart(part)
			return outcome{}, fmt.Errorf("unusable Content-Range %q: %w", resp.Header.Get("Content-Range"), io.ErrUnexpectedEOF)
		}
		if size > 0 {
			total = size
		}
	case http.StatusRequestedRangeNotSatisfiable:
		if offset > 0 {
			// The .part file is longer than the file now is; start over.
			removePart(part)
			return outcome{}, fmt.Errorf("range refused: %w", io.ErrUnexpectedEOF)
		}
		return outcome{}, &StatusError{Status: resp.StatusCode}
	default:
		return outcome{}, &StatusError{Status: resp.StatusCode}
	}

	file, sum, err := openPart(part, offset)
	if err != nil {
		return outcome{}, err
	}
	// Hashing what was kept can take a while; the transfer's clock restarts after it.
	watchdog.Reset(c.stall)
	out, err := copyBody(resp.Body, file, sum, offset, total, func() { watchdog.Reset(c.stall) }, progress)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = local("write "+part, closeErr)
	}
	if err != nil {
		return out, networkOrLocalError(attemptCtx, err)
	}
	out.sum = hex.EncodeToString(sum.Sum(nil))
	if req.Size > 0 && out.done != req.Size {
		return out, fmt.Errorf("%w: got %d bytes, expected %d", ErrSizeMismatch, out.done, req.Size)
	}
	return out, nil
}

// copyBody writes the body to file after the first offset bytes, hashing and
// reporting progress as it goes.
func copyBody(body io.Reader, file *os.File, sum hash.Hash, offset, total int64, alive func(), progress *progress) (outcome, error) {
	out := outcome{done: offset}
	progress.begin(offset)
	buf := make([]byte, bufferSize)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			alive()
			if _, err := file.Write(buf[:n]); err != nil {
				return out, local("write "+file.Name(), err)
			}
			sum.Write(buf[:n])
			out.received += int64(n)
			out.done += int64(n)
			progress.update(out.done, total, false)
		}
		if errors.Is(readErr, io.EOF) {
			progress.update(out.done, total, true)
			return out, nil
		}
		if readErr != nil {
			return out, fmt.Errorf("read response: %w", readErr)
		}
	}
}

// networkError turns a failed request into ErrStalled when the watchdog
// cancelled it, and leaves any other cause as it is.
func networkError(attemptCtx context.Context, err error) error {
	if errors.Is(context.Cause(attemptCtx), ErrStalled) {
		return ErrStalled
	}
	return err
}

func networkOrLocalError(attemptCtx context.Context, err error) error {
	var le *LocalError
	if errors.As(err, &le) {
		return err
	}
	return networkError(attemptCtx, err)
}

// retryable reports a failure that may not happen again: a dropped
// connection, a stall, a timeout or a server error. Local failures and
// refusals such as 404 would only repeat.
func retryable(err error) bool {
	var le *LocalError
	if errors.As(err, &le) {
		return false
	}
	var status *StatusError
	if errors.As(err, &status) {
		return status.Status >= 500 || status.Status == http.StatusRequestTimeout || status.Status == http.StatusTooManyRequests
	}
	var netErr net.Error
	return errors.Is(err, ErrStalled) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &netErr)
}

// finish moves a complete .part file into place.
func finish(dest, part string, out outcome) (File, error) {
	if err := os.Rename(part, dest); err != nil {
		return File{}, local("move finished download into place", err)
	}
	_ = os.Remove(part + validatorSuffix)
	return File{Path: dest, Size: out.done, SHA256: out.sum}, nil
}

// openPart opens the .part file for writing at offset, and returns a hash
// that already covers the first offset bytes. Offset 0 starts the file over.
func openPart(part string, offset int64) (*os.File, hash.Hash, error) {
	file, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- the path is built from the download folder.
	if err != nil {
		return nil, nil, local("open "+part, err)
	}
	sum := sha256.New()
	if offset == 0 {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, nil, local("restart "+part, err)
		}
		return file, sum, nil
	}
	if _, err := io.CopyN(sum, file, offset); err != nil {
		_ = file.Close()
		return nil, nil, local("read "+part, err)
	}
	return file, sum, nil
}

// resumePoint is how many bytes of an interrupted download can be kept, and
// the validator to guard the range request with. Without a validator nothing
// can be kept: the server could not tell that the file had changed.
func resumePoint(part string) (offset int64, validator string) {
	info, err := os.Stat(part)
	if err != nil || info.Size() == 0 {
		return 0, ""
	}
	data, err := os.ReadFile(part + validatorSuffix) // #nosec G304 -- see openPart.
	if err != nil || len(data) == 0 {
		return 0, ""
	}
	return info.Size(), string(data)
}

// validatorOf is the header a range request can be guarded with: a strong
// ETag, or failing that the modification time.
func validatorOf(h http.Header) string {
	if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		return etag
	}
	return h.Get("Last-Modified")
}

// saveValidator records the validator beside the .part file, or forgets an old
// one when the server gave none.
func saveValidator(part, validator string) {
	path := part + validatorSuffix
	if validator == "" {
		_ = os.Remove(path)
		return
	}
	// Losing it only costs a restart, so a failed write is not an error.
	_ = os.WriteFile(path, []byte(validator), 0o600)
}

func removePart(part string) {
	_ = os.Remove(part)
	_ = os.Remove(part + validatorSuffix)
}

// parseContentRange reads "bytes start-end/size"; size is 0 when the server
// does not know it.
func parseContentRange(value string) (start, size int64, ok bool) {
	var end int64
	rest, found := strings.CutPrefix(value, "bytes ")
	if !found {
		return 0, 0, false
	}
	rangePart, sizePart, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(rangePart, "%d-%d", &start, &end); err != nil {
		return 0, 0, false
	}
	if sizePart != "*" {
		n, err := strconv.ParseInt(sizePart, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		size = n
	}
	return start, size, true
}

// progress throttles reports to the caller and works out the transfer speed.
type progress struct {
	report   func(Progress)
	interval time.Duration

	lastAt   time.Time
	lastDone int64
	speed    float64
}

func newProgress(report func(Progress), interval time.Duration) *progress {
	return &progress{report: report, interval: interval}
}

// begin starts measuring from done bytes, at the start of an attempt.
func (p *progress) begin(done int64) {
	p.lastAt, p.lastDone, p.speed = time.Now(), done, 0
}

// update reports done of total bytes, at most once an interval unless force.
func (p *progress) update(done, total int64, force bool) {
	if p.report == nil {
		return
	}
	now := time.Now()
	elapsed := now.Sub(p.lastAt)
	if !force && elapsed < p.interval {
		return
	}
	if elapsed > 0 {
		instant := float64(done-p.lastDone) / elapsed.Seconds()
		if p.speed == 0 {
			p.speed = instant
		} else {
			p.speed = (p.speed + instant) / 2
		}
	}
	p.lastAt, p.lastDone = now, done
	p.report(Progress{Done: done, Total: total, BytesPerSecond: p.speed})
}
