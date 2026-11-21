package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
)

// releaseHost serves a release's files over HTTP, for the downloads in tests.
type releaseHost struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	// gate, when set, holds every response until it is closed.
	gate chan struct{}
	// halfThenWait makes responses send half the file and then wait for the client to leave.
	halfThenWait bool
}

func newReleaseHost(t *testing.T) *releaseHost {
	t.Helper()
	h := &releaseHost{files: map[string][]byte{}}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)
	return h
}

func (h *releaseHost) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	body, ok := h.files[strings.TrimPrefix(r.URL.Path, "/")]
	gate, half := h.gate, h.halfThenWait
	h.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("ETag", `"v1"`)
	if half && r.Header.Get("Range") == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// add publishes a file and returns the release asset that points at it.
func (h *releaseHost) add(name string, body []byte) source.Asset {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[name] = body
	return source.Asset{Name: name, Size: int64(len(body)), URL: h.URL + "/" + name}
}

func (h *releaseHost) setGate(gate chan struct{}) {
	h.mu.Lock()
	h.gate = gate
	h.mu.Unlock()
}

func (h *releaseHost) setHalf(half bool) {
	h.mu.Lock()
	h.halfThenWait = half
	h.mu.Unlock()
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// downloadFixture is an Item whose release has the given assets, with the
// downloader, platform and file manager replaced by local stand-ins.
type downloadFixture struct {
	*env
	entry    catalog.Entry
	release  source.Release
	root     string
	platform *platform.Fake
	// revealed holds the paths shown in the file manager.
	mu       sync.Mutex
	revealed []string
	tt       *ui.Tester
}

func newDownloadFixture(t *testing.T, prefs preferences, assets ...source.Asset) *downloadFixture {
	t.Helper()
	e := newEnv(t, prefs)
	rel := source.Release{Tag: "v1.0.0", Name: "v1.0.0", Published: releaseDay, Assets: assets}
	e.source.publish("tool", "A tool", rel)
	f := &downloadFixture{env: e, platform: &platform.Fake{Result: platform.Signature{State: platform.SignatureNotChecked}}}
	f.entry = e.track("tool", catalog.AddOptions{})
	f.release = f.entry.Releases[0]
	f.root = e.screen.downloadRoot(prefs)
	e.screen.fetcher = fetch.NewClient(fetch.Options{
		StallTimeout: 5 * time.Second, Backoff: func(int) time.Duration { return time.Millisecond }, ProgressInterval: time.Nanosecond,
	})
	e.screen.platform = f.platform
	e.screen.showFile = func(path string) {
		f.mu.Lock()
		f.revealed = append(f.revealed, path)
		f.mu.Unlock()
	}
	f.tt = e.open()
	e.screen.openItem(f.entry.Item.ID)
	f.tt.Frame()
	return f
}

func (f *downloadFixture) shown() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revealed...)
}

func (f *downloadFixture) dest(file string) string {
	return fetch.DestPath(f.root, testOwner, "tool", f.release.Tag, file)
}

func (f *downloadFixture) lastEntry() activity.Entry { return f.screen.activity.Entries().Get()[0] }

func downloadPrefs(t *testing.T) preferences {
	t.Helper()
	p := defaultPreferences()
	p.DownloadFolder = filepath.Join(t.TempDir(), "downloads")
	// These tests are about checksums and signatures; the other checks have tests of their own.
	p.ScanDownloads, p.CheckProvenance = false, false
	return p
}

func TestDownloadFileOnlyDownloadsVerifiesAndRevealsTheFile(t *testing.T) {
	host := newReleaseHost(t)
	body := fileBody(40 << 10)
	zip := host.add("tool-x64.zip", body)
	sums := host.add("checksums.txt", []byte(sha(body)+"  tool-x64.zip\n"))
	f := newDownloadFixture(t, downloadPrefs(t), zip, sums)

	click(t, f.tt, "Download file only")
	f.settle()

	dest := f.dest("tool-x64.zip")
	got, err := os.ReadFile(dest) // #nosec G304 -- a path inside the test's temporary directory.
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("downloaded file: %v", err)
	}
	if shown := f.shown(); len(shown) != 1 || shown[0] != dest {
		t.Errorf("revealed = %v", shown)
	}
	want := "Downloaded tool-x64.zip from acme/tool · checksum verified (checksums.txt)"
	if entry := f.lastEntry(); entry.Message != want || entry.Level != activity.Success || entry.ItemID != f.entry.Item.ID {
		t.Errorf("activity = %+v, want %q", entry, want)
	}
	requireText(t, f.tt, "checksum verified (checksums.txt)", "Show in folder", "Dismiss")
	requireText(t, f.tt, "Downloaded tool-x64.zip")
}

func TestAChecksumThatDoesNotMatchDeletesTheFileAndSaysWhy(t *testing.T) {
	host := newReleaseHost(t)
	body := fileBody(10 << 10)
	zip := host.add("tool-x64.zip", body)
	sums := host.add("checksums.txt", []byte(sha([]byte("something else"))+"  tool-x64.zip\n"))
	f := newDownloadFixture(t, downloadPrefs(t), zip, sums)

	click(t, f.tt, "Download file only")
	f.settle()

	if _, err := os.Stat(f.dest("tool-x64.zip")); !os.IsNotExist(err) {
		t.Errorf("the rejected file is still there: %v", err)
	}
	if len(f.shown()) != 0 {
		t.Error("a rejected file was shown in the file manager")
	}
	reason := "The download's checksum doesn't match the one the publisher posted. The file was deleted."
	if entry := f.lastEntry(); entry.Level != activity.Error || !strings.Contains(entry.Message, reason) {
		t.Errorf("activity = %+v", entry)
	}
	requireText(t, f.tt, reason, "Try again")
}

func TestAnUnsignedProgramWithNoChecksumIsKeptWithItsFindings(t *testing.T) {
	host := newReleaseHost(t)
	exe := host.add("tool-x64-setup.exe", fileBody(8<<10))
	f := newDownloadFixture(t, downloadPrefs(t), exe)
	f.platform.Result = platform.Signature{State: platform.SignatureUnsigned}

	click(t, f.tt, "Download file only")
	f.settle()

	if _, err := os.Stat(f.dest("tool-x64-setup.exe")); err != nil {
		t.Fatalf("the file was not kept: %v", err)
	}
	if len(f.shown()) != 1 {
		t.Error("the kept file was not shown")
	}
	if entry := f.lastEntry(); entry.Level != activity.Warning || !strings.Contains(entry.Message, "no checksum published · not digitally signed") {
		t.Errorf("activity = %+v", entry)
	}
	requireText(t, f.tt, "It isn't digitally signed.", "The project publishes no checksum to compare against.")
	requireNoText(t, f.tt, "Install anyway")
}

func TestSignedProgramsReportTheirSigner(t *testing.T) {
	host := newReleaseHost(t)
	exe := host.add("tool-x64-setup.exe", fileBody(8<<10))
	f := newDownloadFixture(t, downloadPrefs(t), exe)
	f.platform.Result = platform.Signature{State: platform.SignatureTrusted, Signer: "Example Corp"}

	click(t, f.tt, "Download file only")
	f.settle()

	if entry := f.lastEntry(); entry.Level != activity.Success || !strings.HasSuffix(entry.Message, "no checksum published · signed by Example Corp") {
		t.Errorf("activity = %+v", entry)
	}
}

func TestAnInvalidSignatureDeletesTheFile(t *testing.T) {
	host := newReleaseHost(t)
	exe := host.add("tool-x64-setup.exe", fileBody(8<<10))
	f := newDownloadFixture(t, downloadPrefs(t), exe)
	f.platform.Result = platform.Signature{State: platform.SignatureInvalid, Signer: "Example Corp"}

	click(t, f.tt, "Download file only")
	f.settle()

	if _, err := os.Stat(f.dest("tool-x64-setup.exe")); !os.IsNotExist(err) {
		t.Errorf("the rejected file is still there: %v", err)
	}
	requireText(t, f.tt, "The download's digital signature is invalid. The file was deleted.")
}

func TestVerificationCanBeTurnedOff(t *testing.T) {
	host := newReleaseHost(t)
	body := fileBody(10 << 10)
	zip := host.add("tool-x64.zip", body)
	sums := host.add("checksums.txt", []byte(sha([]byte("something else"))+"  tool-x64.zip\n"))
	prefs := downloadPrefs(t)
	prefs.VerifyDownloads = false
	f := newDownloadFixture(t, prefs, zip, sums)

	click(t, f.tt, "Download file only")
	f.settle()

	if _, err := os.Stat(f.dest("tool-x64.zip")); err != nil {
		t.Fatalf("the file was not kept: %v", err)
	}
	if entry := f.lastEntry(); entry.Level != activity.Success || !strings.HasSuffix(entry.Message, "not verified") {
		t.Errorf("activity = %+v", entry)
	}
}

func TestAnyFileOfAReleaseCanBeDownloadedFromShowAllFiles(t *testing.T) {
	host := newReleaseHost(t)
	notes := host.add("NOTES.txt", []byte("hello"))
	zip := host.add("tool-x64.zip", fileBody(1<<10))
	f := newDownloadFixture(t, downloadPrefs(t), zip, notes)

	requireNoText(t, f.tt, "NOTES.txt · 5 B")
	click(t, f.tt, "Show all files")
	requireText(t, f.tt, "NOTES.txt · 5 B", "Hide files")
	if err := f.tt.Click("Download file only"); err != nil { // the chosen file's button
		t.Fatal(err)
	}
	f.settle()
	if _, err := os.Stat(f.dest("tool-x64.zip")); err != nil {
		t.Fatal(err)
	}
}

func TestAFileThatIsNotThereFailsWithAReason(t *testing.T) {
	host := newReleaseHost(t)
	missing := source.Asset{Name: "tool-x64.zip", Size: 10, URL: host.URL + "/gone.zip"}
	f := newDownloadFixture(t, downloadPrefs(t), missing)

	click(t, f.tt, "Download file only")
	f.settle()

	requireText(t, f.tt, "The file could not be found on the server.", "Try again")
	if entry := f.lastEntry(); entry.Level != activity.Error || !strings.HasPrefix(entry.Message, "Could not download tool-x64.zip from acme/tool") {
		t.Errorf("activity = %+v", entry)
	}
}

func TestCancellingKeepsThePartAndResumeFinishesTheFile(t *testing.T) {
	host := newReleaseHost(t)
	body := fileBody(200 << 10)
	zip := host.add("tool-x64.zip", body)
	host.setHalf(true)
	f := newDownloadFixture(t, downloadPrefs(t), zip)

	click(t, f.tt, "Download file only")
	deadline := time.Now().Add(5 * time.Second)
	for {
		tr := f.screen.transfersOf(f.entry.Item.ID)
		if len(tr) == 1 && tr[0].done >= int64(len(body)/2) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("download never got going: %+v", tr)
		}
		time.Sleep(time.Millisecond)
	}
	f.tt.Frame()
	requireText(t, f.tt, "Cancel")
	click(t, f.tt, "Cancel")
	f.settle()

	requireText(t, f.tt, "Canceled. Resume to continue where it stopped.", "Resume")
	if _, err := os.Stat(f.dest("tool-x64.zip") + ".part"); err != nil {
		t.Fatalf("the .part file was not kept: %v", err)
	}
	if entry := f.lastEntry(); entry.Level != activity.Info || !strings.HasPrefix(entry.Message, "Canceled downloading tool-x64.zip") {
		t.Errorf("activity = %+v", entry)
	}

	host.setHalf(false)
	click(t, f.tt, "Resume")
	f.settle()
	got, err := os.ReadFile(f.dest("tool-x64.zip"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("resumed file: %v", err)
	}
}

func TestOnlyThreeDownloadsRunAtOnceAndTheRestWaitInTheQueue(t *testing.T) {
	host := newReleaseHost(t)
	gate := make(chan struct{})
	host.setGate(gate)
	assets := make([]source.Asset, 0, 5)
	for i := range 5 {
		assets = append(assets, host.add("part"+strconv.Itoa(i)+".zip", fileBody(1<<10)))
	}
	f := newDownloadFixture(t, downloadPrefs(t), assets...)
	for _, a := range assets {
		f.screen.downloadFile(f.entry, f.release, a)
	}
	f.tt.Frame()
	waitUntil(t, "two downloads to queue", func() bool {
		queued := 0
		for _, tr := range f.screen.transfersOf(f.entry.Item.ID) {
			if f.screen.lanes.Queued(tr.key) {
				queued++
			}
		}
		return queued == 2
	})
	f.tt.Frame()
	if got := countText(f.tt, "Waiting in queue…"); got != 2 {
		t.Errorf("%d downloads show the queue, want 2 in %q", got, f.tt.Texts())
	}
	// The Library shows the Item as busy with its download.
	f.screen.page = "library"
	f.tt.Frame()
	requireText(t, f.tt, "Downloading…")

	close(gate)
	f.settle()
	for _, a := range assets {
		if _, err := os.Stat(f.dest(a.Name)); err != nil {
			t.Errorf("%s: %v", a.Name, err)
		}
	}
}

func TestCancellingAQueuedDownloadEndsIt(t *testing.T) {
	host := newReleaseHost(t)
	gate := make(chan struct{})
	host.setGate(gate)
	assets := make([]source.Asset, 0, 4)
	for i := range 4 {
		assets = append(assets, host.add("part"+strconv.Itoa(i)+".zip", fileBody(1<<10)))
	}
	f := newDownloadFixture(t, downloadPrefs(t), assets...)
	for _, a := range assets {
		f.screen.downloadFile(f.entry, f.release, a)
	}
	last := downloadJob(f.entry.Item.ID, f.release.Tag, "part3.zip")
	waitUntil(t, "the fourth download to queue", func() bool { return f.screen.lanes.Queued(last) })

	f.screen.cancelDownload(last)
	waitUntil(t, "the queued download to be cancelled", func() bool {
		for _, tr := range f.screen.transfersOf(f.entry.Item.ID) {
			if tr.key == last {
				return tr.state == transferCanceled
			}
		}
		return false
	})
	close(gate)
	f.settle()
	if _, err := os.Stat(f.dest("part3.zip") + ".part"); !os.IsNotExist(err) {
		t.Error("a download that never started left a .part file")
	}
	if _, err := os.Stat(f.dest("part0.zip")); err != nil {
		t.Error(err)
	}
}

func TestDismissingAFinishedDownloadClearsIt(t *testing.T) {
	host := newReleaseHost(t)
	f := newDownloadFixture(t, downloadPrefs(t), host.add("tool-x64.zip", fileBody(1<<10)))
	click(t, f.tt, "Download file only")
	f.settle()
	requireText(t, f.tt, "Dismiss")
	click(t, f.tt, "Dismiss")
	requireNoText(t, f.tt, "Dismiss", "Show in folder")
}

func TestStartupHousekeepingRemovesFinishedDownloadsUnlessTheyAreKept(t *testing.T) {
	for _, keep := range []bool{false, true} {
		host := newReleaseHost(t)
		prefs := downloadPrefs(t)
		prefs.KeepDownloads = keep
		f := newDownloadFixture(t, prefs, host.add("tool-x64.zip", fileBody(1<<10)))
		click(t, f.tt, "Download file only")
		f.settle()
		if _, err := os.Stat(f.dest("tool-x64.zip")); err != nil {
			t.Fatal(err)
		}
		f.screen.cleanDownloads()
		f.settle()
		_, err := os.Stat(f.dest("tool-x64.zip"))
		if kept := err == nil; kept != keep {
			t.Errorf("keep downloads = %v: file kept = %v", keep, kept)
		}
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
