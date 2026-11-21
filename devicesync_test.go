package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/secrets"

	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"
	libsync "github.com/aaronflorey/release-sync/sync"
)

// sharedFile is the one file the simulated devices share.
func sharedFile() *libsync.Memory { return &libsync.Memory{} }

// syncingEnv is a device that shares its Library through the transport.
func syncingEnv(t *testing.T, transport libsync.Transport, mutate func(*preferences)) *env {
	t.Helper()
	prefs := defaultPreferences()
	prefs.SyncMode = syncFolder
	prefs.SyncFolder = t.TempDir()
	if mutate != nil {
		mutate(&prefs)
	}
	e := newEnv(t, prefs)
	e.screen.devices.transport = func(context.Context, preferences, string) (libsync.Transport, error) { return transport, nil }
	return e
}

// syncNow runs a sync to its end.
func (e *env) syncNow(t *testing.T) {
	t.Helper()
	if err := e.screen.syncOnce(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	e.settle()
}

func sourceRef(name string) source.RepoRef { return source.RepoRef{Owner: testOwner, Repo: name} }

// tracksTool reports that the Library tracks the repository "tool".
func (e *env) tracksTool() bool {
	_, ok := e.screen.catalog.Find(sourceRef("tool"))
	return ok
}

// waitFor waits for a condition that a background job brings about.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSyncingSharesTheLibraryAndChecksTheItemsThatArrive(t *testing.T) {
	cloud := sharedFile()
	pc := syncingEnv(t, cloud, func(p *preferences) { p.SyncDeviceName = "Desk" })
	mac := syncingEnv(t, cloud, func(p *preferences) { p.SyncDeviceName = "Laptop" })
	for _, e := range []*env{pc, mac} {
		e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	}
	pc.track("tool", catalog.AddOptions{})

	pc.syncNow(t)
	if st := pc.screen.devices.status.Get(); st.Synced.IsZero() || st.Items != 1 || st.TryLater != 0 || len(st.Devices) != 1 || st.Problem != "" {
		t.Fatalf("status after the first sync = %+v", st)
	}
	if id := pc.screen.prefs.Get().SyncDeviceID; id == "" {
		t.Error("the device was not given an id")
	}

	mac.syncNow(t)
	arrived, ok := mac.screen.catalog.Find(sourceRef("tool"))
	if !ok {
		t.Fatal("the Item did not arrive")
	}
	if len(arrived.Releases) == 0 || !arrived.Item.MetadataFetched {
		t.Errorf("the Item that arrived was not checked for updates: %+v", arrived)
	}
	st := mac.screen.devices.status.Get()
	names := make([]string, 0, len(st.Devices))
	for _, d := range st.Devices {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"Laptop", "Desk"}) {
		t.Errorf("devices = %v, want this device first and then the others", names)
	}
	if reads := mac.source.releaseRequests(); reads == 0 {
		t.Error("nothing was asked of the site for the Item that arrived")
	}
}

func TestEachDeviceKeepsItsOwnInstalledSoftwareAndSettings(t *testing.T) {
	cloud := sharedFile()
	pc := syncingEnv(t, cloud, func(p *preferences) { p.DownloadFolder = filepath.Join(t.TempDir(), "downloads") })
	mac := syncingEnv(t, cloud, nil)
	for _, e := range []*env{pc, mac} {
		e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	}
	pc.track("tool", catalog.AddOptions{AlreadyHave: true})
	pc.syncNow(t)
	mac.syncNow(t)

	if got, _ := mac.screen.catalog.Find(sourceRef("tool")); got.Install != nil {
		t.Errorf("what is installed on one PC reached another: %+v", got.Install)
	}
	if text, _ := cloud.Text(); strings.Contains(text, "download") || strings.Contains(text, "v1.0.0") || strings.Contains(text, "token") {
		t.Errorf("the shared file holds something of this PC: %s", text)
	}
	if mac.screen.prefs.Get().DownloadFolder != "" {
		t.Error("a setting of one PC reached another")
	}
}

func TestADeletionOnOneDeviceReachesTheOtherAndItIsNotChecked(t *testing.T) {
	cloud := sharedFile()
	pc, mac := syncingEnv(t, cloud, nil), syncingEnv(t, cloud, nil)
	for _, e := range []*env{pc, mac} {
		e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	}
	added := pc.track("tool", catalog.AddOptions{})
	pc.syncNow(t)
	mac.syncNow(t)
	if err := mac.screen.catalog.Remove(t.Context(), mac.entryOf("tool").Item.ID); err != nil {
		t.Fatal(err)
	}
	mac.syncNow(t)
	pc.syncNow(t)
	if pc.tracksTool() {
		t.Errorf("the Item %d is still on the first device after it was deleted on the other", added.Item.ID)
	}
}

func TestSyncProblemsAreTheMessagesTheUserShouldSee(t *testing.T) {
	for name, c := range map[string]struct {
		err      error
		want     string
		expected bool
	}{
		"locked":     {libsync.ErrLocked, "This sync file is locked. Enter the passphrase you set up on your other device.", true},
		"wrong":      {libsync.ErrWrongPassphrase, "That passphrase doesn't open the sync file.", true},
		"damaged":    {libsync.ErrDamaged, "The sync file is damaged.", true},
		"unreadable": {libsync.ErrUnreadable, "The sync file isn't readable, so nothing was changed.", true},
		"newer":      {libsync.ErrNewerFormat, "The sync file was made by a newer version of Release Sync. Update Release Sync and try again.", true},
		"short":      {libsync.ErrPassphraseShort, "A passphrase needs at least 8 characters.", true},
		"no token":   {libsync.ErrNoToken, "Add a GitHub token in Settings to sync through a gist.", true},
		"rejected":   {libsync.ErrTokenRejected, "GitHub didn't accept your token. Add a fresh one in Settings.", true},
		"limit":      {libsync.ErrRateLimited, "GitHub's hourly request limit is used up. Sync will work again when it resets.", true},
		"gist":       {libsync.ErrGistInaccessible, "GitHub wouldn't open the sync gist. Your token needs permission to read and write gists, or the gist was deleted.", true},
		"offline":    {fmt.Errorf("sync: %w", libsync.ErrUnreachable), "Couldn't reach GitHub. Check your internet connection.", true},
		"slow":       {libsync.ErrTimedOut, "GitHub took too long to answer.", true},
		"status":     {&libsync.StatusError{Status: 502}, "GitHub answered 502.", true},
		"no folder":  {errNoSyncFolder, "Choose a folder to sync through.", true},
		"folder":     {&os.PathError{Op: "open", Path: "/x", Err: os.ErrPermission}, "Couldn't use the sync folder: permission denied", true},
		"a fault":    {errors.New("disk image corrupt"), "Something went wrong: disk image corrupt", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, expected := syncProblemText(c.err)
			if got != c.want || expected != c.expected {
				t.Errorf("syncProblemText = %q, %v; want %q, %v", got, expected, c.want, c.expected)
			}
		})
	}
}

func TestAnUnreadableFileIsLeftAloneAndTheProblemIsShown(t *testing.T) {
	cloud := sharedFile()
	if err := cloud.Write(t.Context(), "{ not json"); err != nil {
		t.Fatal(err)
	}
	e := syncingEnv(t, cloud, nil)
	e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	e.track("tool", catalog.AddOptions{})

	if err := e.screen.syncOnce(t.Context(), false); err != nil {
		t.Fatalf("a problem the user can see was returned to be reported: %v", err)
	}
	if text, _ := cloud.Text(); text != "{ not json" {
		t.Errorf("the file was overwritten with %q", text)
	}
	if got := e.screen.devices.status.Get().Problem; got != "The sync file isn't readable, so nothing was changed." {
		t.Errorf("problem = %q", got)
	}
}

func TestAFaultIsReportedAndShown(t *testing.T) {
	e := syncingEnv(t, failingSyncTransport{errors.New("disk image corrupt")}, nil)
	err := e.screen.syncOnce(t.Context(), false)
	if err == nil || !strings.Contains(err.Error(), "disk image corrupt") {
		t.Fatalf("syncOnce = %v, want the fault returned so the job reports it", err)
	}
	if got := e.screen.devices.status.Get().Problem; !strings.HasPrefix(got, "Something went wrong: ") || !strings.Contains(got, "disk image corrupt") {
		t.Errorf("problem = %q", got)
	}
}

type failingSyncTransport struct{ err error }

func (f failingSyncTransport) Read(context.Context) (string, bool, error) { return "", false, f.err }
func (f failingSyncTransport) Write(context.Context, string) error        { return f.err }

func TestSyncingThroughAGistNeedsAToken(t *testing.T) {
	e := newEnv(t, func() preferences { p := defaultPreferences(); p.SyncMode = syncGist; return p }())
	if err := e.screen.syncOnce(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if got := e.screen.devices.status.Get().Problem; got != "Add a GitHub token in Settings to sync through a gist." {
		t.Errorf("problem = %q", got)
	}
}

func TestTheGistThatWasMadeIsRemembered(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/gists":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/gists":
			_, _ = w.Write([]byte(`{"id": "gist-one"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	e := newEnv(t, func() preferences { p := defaultPreferences(); p.SyncMode = syncGist; return p }())
	if err := e.tokens.Set(t.Context(), githubTokenKey, []byte("good-token")); err != nil {
		t.Fatal(err)
	}
	e.screen.devices.transport = func(_ context.Context, p preferences, token string) (libsync.Transport, error) {
		return libsync.NewGist(libsync.GistOptions{BaseURL: server.URL + "/", Token: token, ID: p.SyncGistID, App: appSlug})
	}
	e.syncNow(t)
	if got := e.screen.prefs.Get().SyncGistID; got != "gist-one" {
		t.Errorf("gist id = %q, want the one that was made; requests %v", got, requests)
	}
}

func TestLockingTheFileWithAPassphraseAndOpeningItOnAnotherDevice(t *testing.T) {
	cloud := sharedFile()
	pc, mac := syncingEnv(t, cloud, nil), syncingEnv(t, cloud, nil)
	for _, e := range []*env{pc, mac} {
		e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	}
	pc.track("tool", catalog.AddOptions{})
	pc.syncNow(t)

	if err := pc.screen.setSyncPassphrase("short"); !errors.Is(err, libsync.ErrPassphraseShort) {
		t.Fatalf("a short passphrase gave %v", err)
	}
	if err := pc.screen.setSyncPassphrase("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	pc.settle()
	waitFor(t, "the file to be locked", func() bool { text, _ := cloud.Text(); return libsync.IsEncrypted(text) })
	pc.settle()
	if got, err := pc.tokens.Get(t.Context(), syncPassphraseKey); err != nil || string(got) != "correct horse battery" {
		t.Errorf("the passphrase in the credential store = %q, %v", got, err)
	}
	if text, _ := cloud.Text(); strings.Contains(text, "tool") {
		t.Error("the locked file shows what is in it")
	}

	// The other device does not have the passphrase yet.
	if err := mac.screen.syncOnce(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if got := mac.screen.devices.status.Get().Problem; got != "This sync file is locked. Enter the passphrase you set up on your other device." {
		t.Errorf("problem without the passphrase = %q", got)
	}
	if err := mac.screen.setSyncPassphrase("another passphrase"); err != nil {
		t.Fatal(err)
	}
	mac.settle()
	waitFor(t, "the wrong passphrase to be told", func() bool {
		return mac.screen.devices.status.Get().Problem == "That passphrase doesn't open the sync file."
	})
	mac.settle()
	if mac.tracksTool() {
		t.Error("the Item arrived with the wrong passphrase")
	}

	if err := mac.screen.setSyncPassphrase("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	mac.settle()
	waitFor(t, "the Item to arrive", func() bool { return mac.tracksTool() })
	mac.settle()

	// Turning the lock off writes the file plain and forgets the passphrase.
	pc.screen.removeSyncPassphrase()
	pc.settle()
	waitFor(t, "the file to be plain", func() bool { text, _ := cloud.Text(); return !libsync.IsEncrypted(text) })
	pc.settle()
	if _, err := pc.tokens.Get(t.Context(), syncPassphraseKey); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("the passphrase is still in the credential store: %v", err)
	}
	if pc.screen.prefs.Get().SyncEncrypt {
		t.Error("the lock is still on")
	}
}

func TestADeviceThatLostItsPassphraseSaysTheFileIsLocked(t *testing.T) {
	e := syncingEnv(t, sharedFile(), func(p *preferences) { p.SyncEncrypt = true })
	if err := e.screen.syncOnce(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if got := e.screen.devices.status.Get().Problem; !strings.Contains(got, "locked") {
		t.Errorf("problem = %q", got)
	}
}

// manualClock is a clock that moves only when told to.
type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) AfterFunc(d time.Duration, f func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, timer)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		timer.stopped = true
	}
}

// advance moves the clock and runs what came due.
func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, timer := range c.timers {
		if !timer.stopped && !timer.at.After(c.now) {
			timer.stopped = true
			due = append(due, timer.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

func TestEditsArePushedOnceTheyStopWaitingLongerForAGist(t *testing.T) {
	for name, c := range map[string]struct {
		mode  string
		delay time.Duration
	}{"a folder": {syncFolder, 2500 * time.Millisecond}, "a gist": {syncGist, 20 * time.Second}} {
		t.Run(name, func(t *testing.T) {
			cloud := sharedFile()
			e := syncingEnv(t, cloud, func(p *preferences) { p.SyncMode = c.mode })
			clock := &manualClock{now: time.Now()}
			e.screen.devices.clock = clock
			e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))

			e.track("tool", catalog.AddOptions{})
			e.screen.syncLocalChanged()
			clock.advance(c.delay - time.Millisecond)
			e.settle()
			if _, found := cloud.Text(); found {
				t.Fatalf("pushed before %v of quiet", c.delay)
			}
			// Another edit starts the wait again.
			e.screen.syncLocalChanged()
			clock.advance(c.delay - time.Millisecond)
			e.settle()
			if _, found := cloud.Text(); found {
				t.Fatal("an edit did not start the wait again")
			}
			clock.advance(time.Millisecond)
			e.settle()
			if _, found := cloud.Text(); !found {
				t.Fatalf("nothing was pushed %v after the last edit", c.delay)
			}
		})
	}
}

func TestAnEditThatChangesNothingSharedDoesNotCostARequest(t *testing.T) {
	cloud := sharedFile()
	e := syncingEnv(t, cloud, nil)
	clock := &manualClock{now: time.Now()}
	e.screen.devices.clock = clock
	e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	added := e.track("tool", catalog.AddOptions{})
	e.syncNow(t)
	reads, _ := cloud.Counts()

	// A check changes what the Library holds of the site, not what is shared.
	if res, err := e.screen.catalog.Check(t.Context(), added.Item.ID, true); err != nil || res.Failure != nil {
		t.Fatal(err, res.Failure)
	}
	e.screen.syncLocalChanged()
	clock.advance(time.Minute)
	e.settle()
	if after, _ := cloud.Counts(); after != reads {
		t.Errorf("the shared file was read %d more times after an edit that changed nothing shared", after-reads)
	}

	// A note is shared.
	if err := e.screen.catalog.SetNote(t.Context(), added.Item.ID, "a note"); err != nil {
		t.Fatal(err)
	}
	e.screen.syncLocalChanged()
	clock.advance(time.Minute)
	e.settle()
	if text, _ := cloud.Text(); !strings.Contains(text, "a note") {
		t.Errorf("the note was not pushed: %s", text)
	}
}

func TestNothingIsPushedWhenSyncingIsOff(t *testing.T) {
	cloud := sharedFile()
	e := syncingEnv(t, cloud, func(p *preferences) { p.SyncMode = syncOff })
	clock := &manualClock{now: time.Now()}
	e.screen.devices.clock = clock
	e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	e.track("tool", catalog.AddOptions{})
	e.screen.syncLocalChanged()
	clock.advance(time.Hour)
	e.settle()
	if _, found := cloud.Text(); found {
		t.Error("the Library was shared with syncing off")
	}
}

func TestChoosingAWayToSyncStartsSyncingAndTurningItOffForgetsTheStatus(t *testing.T) {
	cloud := sharedFile()
	e := syncingEnv(t, cloud, func(p *preferences) { p.SyncMode = syncOff })
	e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	e.track("tool", catalog.AddOptions{})
	startBackgroundOn(t, e.screen, &background.Fake{})

	e.screen.prefs.Update(func(p *preferences) { p.SyncMode = syncFolder })
	waitFor(t, "the first sync", func() bool { return !e.screen.devices.status.Get().Synced.IsZero() })
	if text, found := cloud.Text(); !found || !strings.Contains(text, `"repo": "tool"`) {
		t.Errorf("shared file = %q, %v", text, found)
	}

	e.screen.prefs.Update(func(p *preferences) { p.SyncMode = syncOff })
	waitFor(t, "the status to be forgotten", func() bool { return e.screen.devices.status.Get().Synced.IsZero() })
}

func TestTheSharingSettingsShowTheStatusTheDevicesAndWhatToChoose(t *testing.T) {
	cloud := sharedFile()
	pc := syncingEnv(t, cloud, func(p *preferences) { p.SyncDeviceName = "Desk" })
	mac := syncingEnv(t, cloud, func(p *preferences) { p.SyncDeviceName = "Laptop" })
	for _, e := range []*env{pc, mac} {
		e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	}
	pc.track("tool", catalog.AddOptions{})
	pc.syncNow(t)
	mac.syncNow(t)

	tt := mac.open()
	tt.SetSize(windowWidth, 12000)
	mac.screen.openSettings("data")
	requireText(t, tt, "Your devices and sharing", "Sync across your devices", "A shared folder", "Shared folder",
		"Lock with a passphrase", "This device's name", "Sync now", "Your devices", "Laptop (this device)", "Desk")
	if !textContaining(tt, "1 app, 0 on your try-later list · 1 other device") || !textContaining(tt, "Synced at") {
		t.Errorf("status line missing in %q", tt.Texts())
	}

	before, _ := cloud.Counts()
	click(t, tt, "Sync now")
	mac.settle()
	if after, _ := cloud.Counts(); after <= before {
		t.Error("Sync now did not read the shared file")
	}

	click(t, tt, "How to sync")
	for _, label := range []string{"Off", "Private GitHub Gist", "A shared folder"} {
		requireText(t, tt, label)
	}
	click(t, tt, "Private GitHub Gist")
	if got := mac.screen.prefs.Get().SyncMode; got != syncGist {
		t.Errorf("mode = %q", got)
	}
	requireText(t, tt, "Your gist", "The gist is made on the first sync.")
}

func TestTheSettingsHideWhatSyncingDoesNotNeedAndShowAProblem(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	tt.SetSize(windowWidth, 12000)
	e.screen.openSettings("data")
	requireText(t, tt, "Sync across your devices", "Lock with a passphrase", "This device's name")
	requireNoText(t, tt, "Sync now", "Laptop")

	e.screen.prefs.Update(func(p *preferences) { p.SyncMode = syncGist })
	if err := e.screen.syncOnce(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	requireText(t, tt, "Sync problem: Add a GitHub token in Settings to sync through a gist.")
}

func TestAPassphraseTypedInSettingsMustHaveEightCharacters(t *testing.T) {
	e := syncingEnv(t, sharedFile(), nil)
	tt := e.open()
	tt.SetSize(windowWidth, 12000)
	e.screen.openSettings("data")
	tt.Frame()
	typeInto(t, tt, "Passphrase", "short")
	click(t, tt, "Use passphrase")
	requireText(t, tt, "A passphrase needs at least 8 characters.")
	if _, err := e.tokens.Get(t.Context(), syncPassphraseKey); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("a passphrase that is too short was kept: %v", err)
	}
}

func TestAnotherDevicesChangeToTheSharedFolderArrivesWithoutPolling(t *testing.T) {
	root := t.TempDir()
	prefs := defaultPreferences()
	prefs.SyncMode, prefs.SyncFolder = syncFolder, root
	e := newEnv(t, prefs)
	e.source.publish("tool", "A tool", release("v1.0.0", 1, ""))
	startBackgroundOn(t, e.screen, &background.Fake{})

	doc := libsync.Document{Format: libsync.Format, Version: libsync.Version, Items: []libsync.Item{{
		Source: "github", Owner: testOwner, Repo: "tool", AddedAt: time.Now(), ModifiedAt: time.Now(),
	}}}
	text, err := doc.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := libsync.NewFolder(root, appSlug).Write(t.Context(), text); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the Item to arrive from the shared folder", func() bool { return e.tracksTool() })
}
