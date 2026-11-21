package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/auron-labs/mygo-framework/secrets"
	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	libsync "github.com/aaronflorey/release-sync/sync"

	"github.com/egoist/mygo"
)

const (
	syncJob = "sync"
	// checkSyncedJob checks the Items that arrived from another device.
	checkSyncedJob = "check-synced"
	// syncPassphraseKey is the credential store entry of the sync passphrase.
	syncPassphraseKey = "sync-passphrase"

	// How long after the last change the Library is pushed: a gist is slower,
	// and has a request limit, so it waits longer than a folder.
	syncGistDelay   = 20 * time.Second
	syncFolderDelay = 2500 * time.Millisecond
	// syncFolderSettle is how long after the shared file changed it is read,
	// as a file sync service may still be writing it.
	syncFolderSettle = time.Second
	// How often the shared file is looked at when nothing else prompts it. A
	// folder is watched too, so its interval is only a fallback.
	syncGistEvery   = 5 * time.Minute
	syncFolderEvery = 15 * time.Minute
	// syncStartDelay lets the application settle before the first sync.
	syncStartDelay = 10 * time.Second
)

// syncStatus is how the last sync went, for Settings.
type syncStatus struct {
	// Synced is when the last sync worked, zero before one did.
	Synced time.Time
	// Items and TryLater are how many apps and saved repositories every device
	// shares now.
	Items, TryLater int
	// Devices are the devices that have taken part, this one included.
	Devices []libsync.Device
	// Problem is why the last sync failed, "" when it did not.
	Problem string
}

// deviceSync is what the application keeps to sync the Library with its other
// devices.
type deviceSync struct {
	status *state.Cell[syncStatus]
	// hostname is the computer's name, which names the device until the user does.
	hostname string
	clock    background.Clock
	// sched polls the shared file.
	sched *background.Scheduler
	// transport opens the transport the preferences describe; replaceable in tests.
	transport func(ctx context.Context, p preferences, token string) (libsync.Transport, error)

	mu sync.Mutex
	// stopPush cancels the push waiting for edits to stop.
	stopPush func()
	// again says a sync was asked for while one ran.
	again bool
	// lastShared is the signature of the Library as the last sync left it, so
	// that an edit that changes nothing shared does not cause a sync.
	lastShared string
	// applied are the settings the schedule and the folder watch follow now.
	// started says the schedule runs: before that, the settings are those the
	// application started with, which the schedule's first run acts on.
	applied syncSettings
	started bool
	// stopWatch ends the watch of the shared folder.
	stopWatch context.CancelFunc
	// toCheck are the Items that arrived and have not been checked.
	toCheck []int64
}

// syncSettings are the preferences that decide where the schedule looks.
type syncSettings struct {
	mode, folder string
}

func newDeviceSync(clock background.Clock) *deviceSync {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = appName
	}
	return &deviceSync{status: state.NewCell(syncStatus{}), hostname: hostname, clock: clock, transport: openSyncTransport}
}

// lastSynced is when the last sync worked, which the schedule counts from.
func (d *deviceSync) lastSynced() (time.Time, bool) {
	at := d.status.Get().Synced
	return at, !at.IsZero()
}

// openSyncTransport is the transport of the preferences: a gist of the
// user's, or a folder.
func openSyncTransport(_ context.Context, p preferences, token string) (libsync.Transport, error) {
	switch p.SyncMode {
	case syncGist:
		gist, err := libsync.NewGist(libsync.GistOptions{
			Token: token, ID: p.SyncGistID, App: appSlug, UserAgent: userAgent(), Description: appName + " library, synced between your devices",
		})
		if err != nil {
			return nil, fmt.Errorf("open the sync gist: %w", err)
		}
		return gist, nil
	case syncFolder:
		if p.SyncFolder == "" {
			return nil, errNoSyncFolder
		}
		return libsync.NewFolder(p.SyncFolder, appSlug), nil
	}
	return nil, fmt.Errorf("sync mode %q has no transport", p.SyncMode)
}

var errNoSyncFolder = errors.New("no folder to sync through")

// syncEnabled reports a sync that can run.
func (p preferences) syncEnabled() bool { return p.SyncMode == syncGist || p.SyncMode == syncFolder }

// syncEvery is how often the shared file is looked at.
func (p preferences) syncEvery() time.Duration {
	switch p.SyncMode {
	case syncGist:
		return syncGistEvery
	case syncFolder:
		return syncFolderEvery
	}
	return 0
}

// syncDelay is how long after the last edit the Library is pushed.
func (p preferences) syncDelay() time.Duration {
	if p.SyncMode == syncFolder {
		return syncFolderDelay
	}
	return syncGistDelay
}

// deviceName is what this device is called: the user's name for it, else the
// computer's.
func (s *screen) deviceName(p preferences) string {
	if name := strings.TrimSpace(p.SyncDeviceName); name != "" {
		return name
	}
	return s.devices.hostname
}

// platformName is the operating system, as other devices list it.
func platformName() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	}
	return runtime.GOOS
}

// newDeviceID makes the id that names this device to the others.
func newDeviceID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("make a device id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// syncLocalChanged is what the Library calls when anything in it changed: it
// pushes the Library once the edits stop. It must not block.
func (s *screen) syncLocalChanged() {
	p := s.prefs.Get()
	if !p.syncEnabled() {
		return
	}
	d := s.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopPush != nil {
		d.stopPush()
	}
	d.stopPush = d.clock.AfterFunc(p.syncDelay(), func() { s.requestSync(true) })
}

// requestSync syncs in the background. local says an edit asked for it, which
// is not worth a request when nothing shared changed. A sync already under way
// is followed by another, so what was asked for since is not missed.
func (s *screen) requestSync(local bool) {
	if !s.prefs.Get().syncEnabled() {
		return
	}
	err := s.jobs.Start(syncJob, func(ctx context.Context, _ jobs.Report) error {
		for {
			if err := s.syncOnce(ctx, local); err != nil {
				return err
			}
			if !s.devices.takeAgain() {
				return nil
			}
			local = false
		}
	})
	switch {
	case errors.Is(err, jobs.ErrRunning):
		s.devices.mu.Lock()
		s.devices.again = true
		s.devices.mu.Unlock()
	case err != nil && !errors.Is(err, jobs.ErrStopped):
		log.Print(err)
	}
}

func (d *deviceSync) takeAgain() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	again := d.again
	d.again = false
	return again
}

// syncOnce is one sync: it shares the Library, takes in what the other devices
// shared, and records how it went. A failure the user can act on is shown in
// Settings and ends the sync quietly; any other is returned to be reported.
func (s *screen) syncOnce(ctx context.Context, local bool) error {
	p := s.prefs.Get()
	if !p.syncEnabled() {
		return nil
	}
	if local && !s.libraryChangedSinceSync(ctx) {
		return nil
	}
	p, err := s.ensureDeviceID(p)
	if err != nil {
		return err
	}
	outcome, err := s.runSync(ctx, p)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("sync: %w", ctx.Err())
		}
		return s.syncFailed(err)
	}
	s.syncSucceeded(ctx, p, outcome)
	return nil
}

// libraryChangedSinceSync reports that what is shared of the Library is not
// what the last sync left, or that it cannot be told.
func (s *screen) libraryChangedSinceSync(ctx context.Context) bool {
	doc, err := s.catalog.SyncDocument(ctx)
	if err != nil {
		return true
	}
	s.devices.mu.Lock()
	defer s.devices.mu.Unlock()
	return doc.Signature() != s.devices.lastShared
}

// ensureDeviceID gives this device the id the others know it by, once.
func (s *screen) ensureDeviceID(p preferences) (preferences, error) {
	if p.SyncDeviceID != "" {
		return p, nil
	}
	id, err := newDeviceID()
	if err != nil {
		return p, err
	}
	s.prefs.Update(func(p *preferences) {
		if p.SyncDeviceID == "" {
			p.SyncDeviceID = id
		}
	})
	return s.prefs.Get(), nil
}

// runSync opens the transport and the passphrase and runs the round.
func (s *screen) runSync(ctx context.Context, p preferences) (catalog.SyncOutcome, error) {
	token := ""
	if p.SyncMode == syncGist {
		token, _ = s.resolveToken(ctx)
	}
	transport, err := s.devices.transport(ctx, p, token)
	if err != nil {
		return catalog.SyncOutcome{}, err
	}
	keys := libsync.Keys{Encrypt: p.SyncEncrypt}
	saved, err := s.tokens.Get(ctx, syncPassphraseKey)
	switch {
	case err == nil:
		keys.Passphrase = string(saved)
	case !errors.Is(err, secrets.ErrNotFound):
		return catalog.SyncOutcome{}, fmt.Errorf("read the sync passphrase: %w", err)
	case p.SyncEncrypt:
		// Locked here, but this device has lost the passphrase.
		return catalog.SyncOutcome{}, libsync.ErrLocked
	}
	self := libsync.Device{ID: p.SyncDeviceID, Name: s.deviceName(p), Platform: platformName(), AppVersion: mygo.App.Version()}
	s.setSyncStatus(func(st *syncStatus) { st.Problem = "" })
	outcome, err := s.catalog.Sync(ctx, transport, keys, self)
	if err != nil {
		return outcome, fmt.Errorf("run the sync: %w", err)
	}
	if gist, ok := transport.(*libsync.Gist); ok && gist.ID() != p.SyncGistID {
		id := gist.ID()
		s.prefs.Update(func(p *preferences) { p.SyncGistID = id })
	}
	if !p.SyncEncrypt && keys.Passphrase != "" {
		// Turning the lock off needed the passphrase once, to read the file.
		if err := s.tokens.Delete(ctx, syncPassphraseKey); err != nil {
			log.Printf("remove the sync passphrase: %v", err)
		}
	}
	return outcome, nil
}

func (s *screen) setSyncStatus(change func(*syncStatus)) {
	s.devices.status.Update(func(st *syncStatus) { change(st) })
}

// syncSucceeded records a sync that worked, and has the Items that arrived
// checked for updates straight away.
func (s *screen) syncSucceeded(ctx context.Context, p preferences, outcome catalog.SyncOutcome) {
	devices := make([]libsync.Device, 0, len(outcome.Merged.Devices))
	for _, d := range outcome.Merged.Devices {
		devices = append(devices, d)
	}
	slices.SortFunc(devices, func(a, b libsync.Device) int {
		if a.ID == p.SyncDeviceID || b.ID == p.SyncDeviceID {
			if a.ID == b.ID {
				return 0
			}
			if a.ID == p.SyncDeviceID {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	now := time.Now()
	s.setSyncStatus(func(st *syncStatus) {
		*st = syncStatus{Synced: now, Items: len(outcome.Merged.Items), TryLater: len(outcome.Merged.TryLater), Devices: devices}
	})
	if doc, err := s.catalog.SyncDocument(ctx); err == nil {
		s.devices.mu.Lock()
		s.devices.lastShared = doc.Signature()
		s.devices.mu.Unlock()
	}
	applied := outcome.Applied
	if n := len(applied.Added) + applied.Updated + applied.Removed; n > 0 {
		s.record(ctx, activity.Info, 0, locales.ActivitySynced(len(applied.Added), applied.Updated, applied.Removed))
	}
	if len(applied.Added) > 0 {
		s.checkSyncedItems(applied.Added)
	}
}

// checkSyncedItems checks the Items that arrived from another device, which
// have no releases yet.
func (s *screen) checkSyncedItems(ids []int64) {
	s.devices.mu.Lock()
	s.devices.toCheck = append(s.devices.toCheck, ids...)
	s.devices.mu.Unlock()
	s.startJob(checkSyncedJob, func(ctx context.Context) error {
		for {
			s.devices.mu.Lock()
			batch := s.devices.toCheck
			s.devices.toCheck = nil
			s.devices.mu.Unlock()
			if len(batch) == 0 {
				return nil
			}
			if _, err := s.checker.CheckItems(ctx, batch, s.prefs.Get().ParallelChecks, func(int, int, string) {}); err != nil {
				return fmt.Errorf("check the items that arrived: %w", err)
			}
		}
	})
}

// syncFailed records why a sync failed. A failure the user can do something
// about is told to them and logged; any other is also returned, to be reported.
func (s *screen) syncFailed(err error) error {
	text, expected := syncProblemText(err)
	log.Printf("sync problem: %v", err)
	s.setSyncStatus(func(st *syncStatus) { st.Problem = text })
	if expected {
		return nil
	}
	return err
}

// syncProblemText explains a failed sync to the user, and says whether it was
// one they can act on or wait out rather than a fault.
func syncProblemText(err error) (text string, expected bool) {
	var status *libsync.StatusError
	var pathError *fs.PathError
	switch {
	case errors.Is(err, libsync.ErrLocked):
		return locales.SyncErrLocked(), true
	case errors.Is(err, libsync.ErrWrongPassphrase):
		return locales.SyncErrWrongPassphrase(), true
	case errors.Is(err, libsync.ErrDamaged):
		return locales.SyncErrDamaged(), true
	case errors.Is(err, libsync.ErrUnreadable):
		return locales.SyncErrUnreadable(), true
	case errors.Is(err, libsync.ErrNewerFormat):
		return locales.SyncErrNewer(appName), true
	case errors.Is(err, libsync.ErrPassphraseShort):
		return locales.SyncPassphraseShort(), true
	case errors.Is(err, libsync.ErrNoToken):
		return locales.SyncErrNoToken(), true
	case errors.Is(err, libsync.ErrTokenRejected):
		return locales.SyncErrTokenRejected(), true
	case errors.Is(err, libsync.ErrRateLimited):
		return locales.SyncErrRateLimited(), true
	case errors.Is(err, libsync.ErrGistInaccessible):
		return locales.SyncErrGist(), true
	case errors.Is(err, libsync.ErrUnreachable):
		return locales.SyncErrUnreachable(), true
	case errors.Is(err, libsync.ErrTimedOut):
		return locales.SyncErrTimedOut(), true
	case errors.As(err, &status):
		return locales.SyncErrStatus(status.Status), true
	case errors.Is(err, errNoSyncFolder):
		return locales.SyncErrNoFolder(), true
	case errors.As(err, &pathError):
		return locales.SyncErrFolder(pathError.Err.Error()), true
	}
	return locales.ErrOther(err.Error()), false
}

// applySyncSettings brings the schedule and the folder watch in line with the
// preferences: it looks at the shared file as often as the way of sharing
// asks, watches a shared folder, and syncs at once when the way changed.
func (s *screen) applySyncSettings(p preferences) {
	d := s.devices
	want := syncSettings{mode: p.SyncMode, folder: p.SyncFolder}
	d.mu.Lock()
	if want == d.applied {
		d.mu.Unlock()
		return
	}
	d.applied = want
	if d.stopWatch != nil {
		d.stopWatch()
		d.stopWatch = nil
	}
	if d.stopPush != nil {
		d.stopPush()
		d.stopPush = nil
	}
	d.lastShared = ""
	started := d.started
	d.mu.Unlock()

	if !p.syncEnabled() {
		d.sched.SetInterval(0)
		d.status.Set(syncStatus{})
		return
	}
	if p.SyncMode == syncFolder && p.SyncFolder != "" {
		s.watchSyncFolder(p.SyncFolder)
	}
	d.sched.SetInterval(p.syncEvery())
	if started {
		s.requestSync(false)
	}
}

// startSync starts looking at the shared file: after the application settled,
// then as often as the preferences say.
func (s *screen) startSync() {
	d := s.devices
	d.mu.Lock()
	d.started = true
	d.mu.Unlock()
	d.sched.Start(syncStartDelay, true)
}

// stopSync ends the schedule, the watch and the push that is waiting.
func (s *screen) stopSync() {
	d := s.devices
	d.sched.Stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopWatch != nil {
		d.stopWatch()
		d.stopWatch = nil
	}
	if d.stopPush != nil {
		d.stopPush()
		d.stopPush = nil
	}
}

// watchSyncFolder has a change to the shared file in the folder sync once it
// settles.
func (s *screen) watchSyncFolder(root string) {
	ctx, cancel := context.WithCancel(context.Background())
	folder := libsync.NewFolder(root, appSlug)
	var stopSettle func()
	var mu sync.Mutex
	err := folder.Watch(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if stopSettle != nil {
			stopSettle()
		}
		stopSettle = s.devices.clock.AfterFunc(syncFolderSettle, func() { s.requestSync(false) })
	})
	if err != nil {
		// The schedule still looks at the file now and then.
		cancel()
		log.Printf("watch the sync folder: %v", err)
		return
	}
	s.devices.mu.Lock()
	s.devices.stopWatch = func() {
		cancel()
		mu.Lock()
		defer mu.Unlock()
		if stopSettle != nil {
			stopSettle()
		}
	}
	s.devices.mu.Unlock()
}

// syncNow is the Sync now button.
func (s *screen) syncNow() { s.requestSync(false) }

// setSyncPassphrase locks the shared file with the passphrase, which is kept in
// the credential store. Turning the lock on rewrites the shared file.
func (s *screen) setSyncPassphrase(passphrase string) error {
	if err := libsync.ValidatePassphrase(passphrase); err != nil {
		return fmt.Errorf("use the sync passphrase: %w", err)
	}
	s.startJob("sync-passphrase", func(ctx context.Context) error {
		if err := s.tokens.Set(ctx, syncPassphraseKey, []byte(passphrase)); err != nil {
			log.Printf("keep the sync passphrase: %v", err)
			s.setSyncStatus(func(st *syncStatus) { st.Problem = locales.SyncPassphraseSaveFailed(err.Error()) })
			return nil //nolint:nilerr // Shown to the user and logged; it is not a fault of the application.
		}
		s.prefs.Update(func(p *preferences) { p.SyncEncrypt = true })
		s.requestSync(false)
		return nil
	})
	return nil
}

// removeSyncPassphrase turns the lock off. The next sync reads the file with
// the passphrase, writes it plain and forgets the passphrase.
func (s *screen) removeSyncPassphrase() {
	s.prefs.Update(func(p *preferences) { p.SyncEncrypt = false })
	s.requestSync(false)
}
