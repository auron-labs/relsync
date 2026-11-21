package fetch_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/fetch"
)

// payload is deterministic file content of n bytes.
func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i>>8)
	}
	return b
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// seen is a request as the test server received it.
type seen struct {
	rangeHeader string
	ifRange     string
	auth        string
}

// fileServer serves one file with range support and records every request.
// behave, when set, may take over a request; it is given the 1-based request
// number and reports whether it handled it.
type fileServer struct {
	*httptest.Server
	mu       sync.Mutex
	body     []byte
	etag     string
	requests []seen
	behave   func(w http.ResponseWriter, r *http.Request, n int) bool
}

func newFileServer(t *testing.T, body []byte) *fileServer {
	t.Helper()
	fs := &fileServer{body: body, etag: `"v1"`}
	fs.Server = httptest.NewServer(http.HandlerFunc(fs.serve))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fileServer) serve(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	fs.requests = append(fs.requests, seen{r.Header.Get("Range"), r.Header.Get("If-Range"), r.Header.Get("Authorization")})
	n, body, etag, behave := len(fs.requests), fs.body, fs.etag, fs.behave
	fs.mu.Unlock()
	if behave != nil && behave(w, r, n) {
		return
	}
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "file.bin", time.Time{}, bytes.NewReader(body))
}

func (fs *fileServer) setFile(body []byte, etag string) {
	fs.mu.Lock()
	fs.body, fs.etag = body, etag
	fs.mu.Unlock()
}

func (fs *fileServer) log() []seen {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]seen(nil), fs.requests...)
}

func newClient() *fetch.Client {
	return fetch.NewClient(fetch.Options{
		StallTimeout:     2 * time.Second,
		Backoff:          func(int) time.Duration { return time.Millisecond },
		ProgressInterval: time.Nanosecond,
	})
}

func request(t *testing.T, url string, size int64) fetch.Request {
	t.Helper()
	return fetch.Request{URL: url, Dest: filepath.Join(t.TempDir(), "owner-repo", "v1.0.0", "tool.zip"), Size: size}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a path inside the test's temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requireMissing(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should not exist (stat error: %v)", p, err)
		}
	}
}

// sendHalfThenWait answers with the whole file's headers and the first half of
// the body, then holds the connection open until the client goes away.
func sendHalfThenWait(body []byte, etag string) func(http.ResponseWriter, *http.Request, int) bool {
	return func(w http.ResponseWriter, r *http.Request, _ int) bool {
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	}
}

func TestDownloadWritesTheFileHashesItAndReportsProgress(t *testing.T) {
	body := payload(300 << 10)
	srv := newFileServer(t, body)
	req := request(t, srv.URL, int64(len(body)))
	var reports []fetch.Progress
	got, err := newClient().Download(t.Context(), req, func(p fetch.Progress) { reports = append(reports, p) })
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != req.Dest || got.Size != int64(len(body)) || got.SHA256 != digest(body) {
		t.Fatalf("result = %+v", got)
	}
	if !bytes.Equal(mustRead(t, req.Dest), body) {
		t.Error("file content differs")
	}
	requireMissing(t, req.Dest+".part", req.Dest+".part.validator")
	last := reports[len(reports)-1]
	if last.Done != int64(len(body)) || last.Total != int64(len(body)) {
		t.Errorf("last progress = %+v", last)
	}
	for i := 1; i < len(reports); i++ {
		if reports[i].Done < reports[i-1].Done {
			t.Fatalf("progress went backwards: %+v then %+v", reports[i-1], reports[i])
		}
	}
}

func TestProgressTotalIsUnknownWithoutALength(t *testing.T) {
	body := payload(10 << 10)
	srv := newFileServer(t, body)
	srv.behave = func(w http.ResponseWriter, _ *http.Request, _ int) bool {
		_, _ = w.Write(body) // chunked: no Content-Length
		return true
	}
	var last fetch.Progress
	if _, err := newClient().Download(t.Context(), request(t, srv.URL, 0), func(p fetch.Progress) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last.Total != 0 || last.Done != int64(len(body)) {
		t.Errorf("last progress = %+v", last)
	}
}

func TestACancelledDownloadKeepsItsPartAndTheNextOneResumes(t *testing.T) {
	body := payload(200 << 10)
	srv := newFileServer(t, body)
	srv.behave = func(w http.ResponseWriter, r *http.Request, n int) bool {
		return n == 1 && sendHalfThenWait(body, srv.etag)(w, r, n)
	}
	req := request(t, srv.URL, int64(len(body)))

	ctx, cancel := context.WithCancel(t.Context())
	_, err := newClient().Download(ctx, req, func(p fetch.Progress) {
		if p.Done >= int64(len(body)/2) {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled download = %v", err)
	}
	kept := mustRead(t, req.Dest+".part")
	if len(kept) == 0 || !bytes.Equal(kept, body[:len(kept)]) {
		t.Fatalf("kept %d bytes, which are not the start of the file", len(kept))
	}
	requireMissing(t, req.Dest)

	got, err := newClient().Download(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != digest(body) || !bytes.Equal(mustRead(t, req.Dest), body) {
		t.Fatal("resumed file differs from the original")
	}
	second := srv.log()[1]
	if second.rangeHeader != "bytes="+strconv.Itoa(len(kept))+"-" || second.ifRange != `"v1"` {
		t.Errorf("resume request = %+v", second)
	}
	requireMissing(t, req.Dest+".part", req.Dest+".part.validator")
}

func TestADownloadStartsOverWhenTheFileChanged(t *testing.T) {
	old := payload(200 << 10)
	srv := newFileServer(t, old)
	srv.behave = func(w http.ResponseWriter, r *http.Request, n int) bool {
		return n == 1 && sendHalfThenWait(old, srv.etag)(w, r, n)
	}
	req := request(t, srv.URL, 0)
	ctx, cancel := context.WithCancel(t.Context())
	_, _ = newClient().Download(ctx, req, func(p fetch.Progress) {
		if p.Done >= int64(len(old)/2) {
			cancel()
		}
	})

	changed := payload(150 << 10)
	for i := range changed {
		changed[i] ^= 0xff
	}
	srv.setFile(changed, `"v2"`)
	got, err := newClient().Download(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != digest(changed) || !bytes.Equal(mustRead(t, req.Dest), changed) {
		t.Fatal("the new file was mixed with the old bytes")
	}
	if r := srv.log()[1]; r.rangeHeader == "" || r.ifRange != `"v1"` {
		t.Errorf("the resume attempt was not guarded by the old validator: %+v", r)
	}
}

func TestAPartWithoutAValidatorIsNotResumed(t *testing.T) {
	body := payload(64 << 10)
	srv := newFileServer(t, body)
	req := request(t, srv.URL, int64(len(body)))
	if err := os.MkdirAll(filepath.Dir(req.Dest), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(req.Dest+".part", []byte("stale bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newClient().Download(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if r := srv.log()[0]; r.rangeHeader != "" {
		t.Errorf("requested a range without a validator: %+v", r)
	}
	if !bytes.Equal(mustRead(t, req.Dest), body) {
		t.Error("file content differs")
	}
}

func TestTruncatedTransfersAreRetriedAndResumed(t *testing.T) {
	body := payload(200 << 10)
	srv := newFileServer(t, body)
	srv.behave = func(w http.ResponseWriter, _ *http.Request, n int) bool {
		if n > 2 {
			return false
		}
		// The connection drops after part of the body the headers promised.
		w.Header().Set("ETag", srv.etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:len(body)/4])
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return true
		}
		_ = conn.Close()
		return true
	}
	req := request(t, srv.URL, int64(len(body)))
	got, err := newClient().Download(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != digest(body) || !bytes.Equal(mustRead(t, req.Dest), body) {
		t.Fatal("file differs after retries")
	}
	if log := srv.log(); len(log) != 3 || log[1].rangeHeader == "" || log[2].rangeHeader == "" {
		t.Errorf("requests = %+v", log)
	}
}

func TestTransientFailuresAreRetriedThreeTimesThenReported(t *testing.T) {
	srv := newFileServer(t, payload(1<<10))
	srv.behave = func(w http.ResponseWriter, _ *http.Request, _ int) bool {
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}
	req := request(t, srv.URL, 1<<10)
	_, err := newClient().Download(t.Context(), req, nil)
	var status *fetch.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusServiceUnavailable {
		t.Fatalf("error = %v", err)
	}
	if n := len(srv.log()); n != 4 {
		t.Errorf("made %d requests, want the first try and three retries", n)
	}
	requireMissing(t, req.Dest, req.Dest+".part")
}

func TestARefusalIsNotRetried(t *testing.T) {
	srv := newFileServer(t, nil)
	srv.behave = func(w http.ResponseWriter, _ *http.Request, _ int) bool {
		http.NotFound(w, nil)
		return true
	}
	req := request(t, srv.URL, 10)
	_, err := newClient().Download(t.Context(), req, nil)
	var status *fetch.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusNotFound {
		t.Fatalf("error = %v", err)
	}
	if n := len(srv.log()); n != 1 {
		t.Errorf("made %d requests", n)
	}
	requireMissing(t, req.Dest, req.Dest+".part", req.Dest+".part.validator")
}

func TestAStalledTransferIsRetried(t *testing.T) {
	body := payload(50 << 10)
	srv := newFileServer(t, body)
	srv.behave = func(w http.ResponseWriter, r *http.Request, n int) bool {
		if n > 1 {
			return false
		}
		w.Header().Set("ETag", srv.etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:1<<10])
		w.(http.Flusher).Flush()
		<-r.Context().Done() // nothing more arrives until the client gives up
		return true
	}
	client := fetch.NewClient(fetch.Options{
		StallTimeout: 100 * time.Millisecond, Backoff: func(int) time.Duration { return time.Millisecond },
	})
	req := request(t, srv.URL, int64(len(body)))
	got, err := client.Download(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != digest(body) {
		t.Error("file differs after the stall")
	}
	if log := srv.log(); len(log) != 2 || log[1].rangeHeader != "bytes=1024-" {
		t.Errorf("requests = %+v", log)
	}
}

func TestAStallThatNeverEndsIsAFailureAndKeepsThePart(t *testing.T) {
	srv := newFileServer(t, payload(1<<10))
	srv.behave = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		w.Header().Set("ETag", srv.etag)
		w.Header().Set("Content-Length", "2048")
		_, _ = w.Write(make([]byte, 512))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	}
	client := fetch.NewClient(fetch.Options{
		StallTimeout: 50 * time.Millisecond, Backoff: func(int) time.Duration { return time.Millisecond },
	})
	req := request(t, srv.URL, 2048)
	_, err := client.Download(t.Context(), req, nil)
	if !errors.Is(err, fetch.ErrStalled) {
		t.Fatalf("error = %v", err)
	}
	if info, statErr := os.Stat(req.Dest + ".part"); statErr != nil || info.Size() == 0 {
		t.Errorf("the .part file was not kept: %v", statErr)
	}
	requireMissing(t, req.Dest)
}

func TestAFileOfTheWrongSizeIsRejectedAndRemoved(t *testing.T) {
	body := payload(10 << 10)
	srv := newFileServer(t, body)
	req := request(t, srv.URL, int64(len(body)-1))
	_, err := newClient().Download(t.Context(), req, nil)
	if !errors.Is(err, fetch.ErrSizeMismatch) {
		t.Fatalf("error = %v", err)
	}
	if n := len(srv.log()); n != 1 {
		t.Errorf("a size mismatch was retried: %d requests", n)
	}
	requireMissing(t, req.Dest, req.Dest+".part", req.Dest+".part.validator")
}

func TestARangeTheServerRefusesStartsOver(t *testing.T) {
	body := payload(64 << 10)
	srv := newFileServer(t, body)
	req := request(t, srv.URL, int64(len(body)))
	if err := os.MkdirAll(filepath.Dir(req.Dest), 0o750); err != nil {
		t.Fatal(err)
	}
	// A .part file longer than the file the server has now.
	if err := os.WriteFile(req.Dest+".part", payload(len(body)+100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(req.Dest+".part.validator", []byte(`"v1"`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := newClient().Download(t.Context(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != digest(body) {
		t.Error("file differs")
	}
}

func TestCredentialsAreNotSentToAnotherHost(t *testing.T) {
	body := payload(1 << 10)
	target := newFileServer(t, body)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/blob", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	req := request(t, origin.URL, int64(len(body)))
	req.Header = http.Header{"Authorization": {"Bearer secret"}}
	if _, err := newClient().Download(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if got := target.log()[0].auth; got != "" {
		t.Errorf("the redirect target received Authorization %q", got)
	}
}

func TestCredentialsFollowARedirectWithinTheSameHost(t *testing.T) {
	body := payload(1 << 10)
	srv := newFileServer(t, body)
	srv.behave = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.URL.Path == "/asset" {
			http.Redirect(w, r, "/blob", http.StatusFound)
			return true
		}
		return false
	}
	req := request(t, srv.URL+"/asset", int64(len(body)))
	req.Header = http.Header{"Authorization": {"Bearer secret"}}
	if _, err := newClient().Download(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if log := srv.log(); len(log) != 2 || log[1].auth != "Bearer secret" {
		t.Errorf("requests = %+v", log)
	}
}

func TestGetReadsASmallResponseAndRefusesALargeOne(t *testing.T) {
	srv := newFileServer(t, []byte("0123456789"))
	client := newClient()
	data, err := client.Get(t.Context(), fetch.Request{URL: srv.URL}, 10)
	if err != nil || string(data) != "0123456789" {
		t.Fatalf("Get = %q, %v", data, err)
	}
	if _, err := client.Get(t.Context(), fetch.Request{URL: srv.URL}, 9); !errors.Is(err, fetch.ErrTooLarge) {
		t.Fatalf("Get over the limit = %v", err)
	}
}
