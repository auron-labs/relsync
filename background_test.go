package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"

	"github.com/egoist/mygo"
)

// backgroundScreen is a screen with a desktop that shows nothing, the
// background work started, and a window that only counts how often it was hidden.
type backgroundScreen struct {
	*screen
	desktop *background.Fake
	hidden  int
}

func startBackgroundOn(t *testing.T, s *screen, d *background.Fake) *backgroundScreen {
	t.Helper()
	b := &backgroundScreen{screen: s, desktop: d}
	s.desktop = d
	s.hide = func() { b.hidden++ }
	stop := s.startBackground(true)
	t.Cleanup(stop)
	return b
}

func newBackgroundScreen(t *testing.T, prefs preferences) *backgroundScreen {
	t.Helper()
	return startBackgroundOn(t, newTestScreen(t, prefs), &background.Fake{})
}

func (b *backgroundScreen) closeWindow() *mygo.CloseEvent {
	var event mygo.CloseEvent
	b.beforeClose(&event)
	return &event
}

func TestClosingTheWindowHidesItWhileTheTrayIconIsUpAndSaysSoOnce(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	b := startBackgroundOn(t, e.screen, &background.Fake{})

	if event := b.closeWindow(); !event.DefaultPrevented() || b.hidden != 1 {
		t.Fatalf("closing: prevented %v, hidden %d times: it must hide and not quit", event.DefaultPrevented(), b.hidden)
	}
	e.settle()
	notices := b.desktop.Notices()
	if len(notices) != 1 || notices[0].Title != "Release Sync is still running" || !e.screen.prefs.Get().TrayNoticeShown {
		t.Fatalf("notices = %+v", notices)
	}

	if event := b.closeWindow(); !event.DefaultPrevented() || b.hidden != 2 {
		t.Errorf("closing again: prevented %v, hidden %d times", event.DefaultPrevented(), b.hidden)
	}
	e.settle()
	if got := len(b.desktop.Notices()); got != 1 {
		t.Errorf("the notice was shown %d times, want once", got)
	}
}

func TestClosingTheWindowQuitsWhenThereIsNoTrayIconToBringItBack(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*preferences)
		desk   *background.Fake
	}{
		{"the tray icon is off", func(p *preferences) { p.ShowTrayIcon = false }, &background.Fake{}},
		{"the icon could not be made", func(*preferences) {}, &background.Fake{TrayErr: errors.New("no notification area")}},
		{"the setting is off", func(p *preferences) { p.CloseToTray = false }, &background.Fake{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			prefs := defaultPreferences()
			c.mutate(&prefs)
			b := startBackgroundOn(t, newTestScreen(t, prefs), c.desk)
			if event := b.closeWindow(); event.DefaultPrevented() || b.hidden != 0 {
				t.Errorf("prevented %v, hidden %d times: the close must go through", event.DefaultPrevented(), b.hidden)
			}
		})
	}
}

func TestAQuitIsNotMistakenForAClose(t *testing.T) {
	b := newBackgroundScreen(t, defaultPreferences())
	// MyGo calls the close handler of each window during a quit, after OnBeforeQuit.
	b.presence.quitting.Store(true)
	if event := b.closeWindow(); event.DefaultPrevented() || b.hidden != 0 {
		t.Errorf("prevented %v, hidden %d times: quitting must close the window", event.DefaultPrevented(), b.hidden)
	}
}

func TestQuittingSavesPendingPreferencesAndAFailedSaveKeepsTheWindowOpenAndShown(t *testing.T) {
	dir := t.TempDir()
	s, db := persistentScreen(t, dir)
	b := startBackgroundOn(t, s, &background.Fake{})
	s.prefs.Update(func(p *preferences) { p.Theme = themeDark })
	b.presence.quitting.Store(true)

	if event := b.closeWindow(); event.DefaultPrevented() {
		t.Fatal("a quit with a working save was stopped")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, db := persistentScreen(t, dir)
	t.Cleanup(func() { _ = db.Close() })
	if got := restarted.prefs.Get().Theme; got != themeDark {
		t.Fatalf("theme after quitting = %q: the pending change was lost", got)
	}

	// With the save failing, closing neither quits nor hides, and says why.
	s, db = persistentScreen(t, t.TempDir())
	b = startBackgroundOn(t, s, &background.Fake{})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s.prefs.Update(func(p *preferences) { p.Theme = themeDark })
	b.presence.quitting.Store(true)
	if event := b.closeWindow(); !event.DefaultPrevented() || b.hidden != 0 {
		t.Fatalf("failed save: prevented %v, hidden %d times", event.DefaultPrevented(), b.hidden)
	}
	if b.presence.quitting.Load() {
		t.Error("the quit that was canceled is still under way: the next close would not hide")
	}
}

type recorder struct {
	events *[]string
	name   string
	err    error
}

func (r recorder) note() error { *r.events = append(*r.events, r.name); return r.err }

func (r recorder) Shutdown(context.Context) error { return r.note() }
func (r recorder) Close(context.Context) error    { return r.note() }

type recordedCloser struct{ recorder }

func (r recordedCloser) Close() error { return r.note() }

func TestTheApplicationClosesBackgroundWorkThenJobsThenSavesThenTheDatabaseEvenWhenOneFails(t *testing.T) {
	var events []string
	failure := errors.New("the jobs did not stop")
	c := closers{
		background: func() { events = append(events, "background") },
		jobs:       recorder{&events, "jobs", failure},
		saver:      recorder{&events, "saver", nil},
		database:   recordedCloser{recorder{&events, "database", nil}},
	}
	err := c.close()
	if want := []string{"background", "jobs", "saver", "database"}; !slices.Equal(events, want) {
		t.Errorf("closed in order %v, want %v", events, want)
	}
	if !errors.Is(err, failure) {
		t.Errorf("error = %v, want the jobs' failure", err)
	}
	if err := (closers{}).close(); err != nil {
		t.Errorf("closing nothing = %v", err)
	}
}

func TestTheTrayMenuOffersUpdatingEverythingOnlyWhenThereIsSomethingToUpdate(t *testing.T) {
	b := newBackgroundScreen(t, defaultPreferences())
	labels := func() []string {
		menu := b.desktop.Trays()[0].Menu()
		out := make([]string, len(menu))
		for i, m := range menu {
			out[i] = m.Label
		}
		return out
	}
	if got, want := labels(), []string{"Open", "Check for updates", "", "Quit"}; !slices.Equal(got, want) {
		t.Fatalf("menu = %q, want %q", got, want)
	}
	b.updateCount.Set(3)
	b.applyBackground()
	if got, want := labels(), []string{"Open", "Check for updates", "Update everything (3)", "", "Quit"}; !slices.Equal(got, want) {
		t.Errorf("menu with updates = %q, want %q", got, want)
	}
	if got := b.desktop.Trays()[0].ToolTip(); got != "Release Sync: 3 updates available" {
		t.Errorf("tooltip = %q", got)
	}
	for _, m := range b.desktop.Trays()[0].Menu() {
		if m.Label == "Update everything (3)" {
			m.Click()
		}
	}
	if b.page != "updates" {
		t.Errorf("page = %q: updating everything from the tray shows the Updates page", b.page)
	}
	b.updateCount.Set(0)
	b.applyBackground()
	if got := b.desktop.Trays()[0].ToolTip(); got != "Release Sync" {
		t.Errorf("tooltip = %q", got)
	}
}

func TestTheTrayIconFollowsTheSetting(t *testing.T) {
	b := newBackgroundScreen(t, defaultPreferences())
	if len(b.desktop.Trays()) != 1 || !b.presence.trayUp.Load() {
		t.Fatalf("trays = %d", len(b.desktop.Trays()))
	}
	b.prefs.Update(func(p *preferences) { p.ShowTrayIcon = false })
	b.applyBackground()
	if len(b.desktop.Trays()) != 0 || b.closesToTray() {
		t.Error("the icon is still up after it was turned off")
	}
	b.prefs.Update(func(p *preferences) { p.ShowTrayIcon = true })
	b.applyBackground()
	b.applyBackground()
	if len(b.desktop.Trays()) != 1 || !b.closesToTray() {
		t.Errorf("trays = %d after turning it on again", len(b.desktop.Trays()))
	}
}

func TestTheWindowStaysHiddenOnlyWhenTheSystemStartedTheApplicationAtSignIn(t *testing.T) {
	for _, c := range []struct {
		name      string
		atLogin   bool
		minimized bool
		trayErr   error
		want      bool
	}{
		{"started at sign-in", true, true, nil, true},
		{"started by hand", false, true, nil, false},
		{"start minimized is off", true, false, nil, false},
		{"no tray icon to open it from", true, true, errors.New("no notification area"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			prefs := defaultPreferences()
			prefs.StartMinimized = c.minimized
			b := startBackgroundOn(t, newTestScreen(t, prefs), &background.Fake{OpenedByLogin: c.atLogin, TrayErr: c.trayErr})
			if got := b.startsHidden(); got != c.want {
				t.Errorf("startsHidden = %v, want %v", got, c.want)
			}
		})
	}
}

func TestStartWithWindowsFollowsTheSettingAndRevertsWhenTheSystemRefuses(t *testing.T) {
	b := newBackgroundScreen(t, defaultPreferences())
	b.prefs.Update(func(p *preferences) { p.StartAtLogin = true })
	b.applyBackground()
	if !b.desktop.StartsAtLogin() {
		t.Fatal("the system was not asked to start the application at sign-in")
	}
	b.prefs.Update(func(p *preferences) { p.StartAtLogin = false })
	b.applyBackground()
	if b.desktop.StartsAtLogin() {
		t.Error("the system still starts the application at sign-in")
	}

	b.desktop.FailLogin(errors.New("registry is locked"))
	b.prefs.Update(func(p *preferences) { p.StartAtLogin = true })
	b.applyBackground()
	if b.prefs.Get().StartAtLogin {
		t.Error("the setting says on while the system refused")
	}
	tt := newTester(b.screen)
	tt.Frame()
	if !tt.HasText("Could not change whether Release Sync starts with Windows.") {
		t.Errorf("view = %q", tt.Texts())
	}
}

func TestTheSystemsStartAtLoginEntryIsTheTruthAtLaunch(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	d := &background.Fake{}
	if err := d.SetStartAtLogin(true); err != nil {
		t.Fatal(err)
	}
	s.desktop = d
	stop := s.startBackground(false)
	t.Cleanup(stop)
	if !s.prefs.Get().StartAtLogin {
		t.Error("the setting does not show the entry that is there")
	}

	// A run that keeps its data apart does not rewrite the setting from the installed one's entry.
	isolated := newTestScreen(t, defaultPreferences())
	isolated.desktop = d
	stop = isolated.startBackground(true)
	t.Cleanup(stop)
	if isolated.prefs.Get().StartAtLogin {
		t.Error("an isolated run took over the system's entry")
	}
}

func TestClickingANotificationOpensTheUpdatesPageOnlyForUpdates(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	s.notificationClicked("updates-available")
	if s.page != "updates" {
		t.Errorf("page after an update notification = %q", s.page)
	}
	s.navigate("library")
	s.notificationClicked("updates-installed")
	if s.page != "updates" {
		t.Errorf("page after the notification of installed updates = %q", s.page)
	}
	s.navigate("library")
	s.notificationClicked("tray")
	if s.page != "library" {
		t.Errorf("page after the tray notice = %q, want it left alone", s.page)
	}
}

func TestResumingAndTheNetworkReturningWakeTheSchedule(t *testing.T) {
	s := newTestScreen(t, defaultPreferences())
	var mu sync.Mutex
	runs := 0
	s.sched = background.NewScheduler(background.SystemClock(), func() (time.Time, bool) { return time.Time{}, false }, func() {
		mu.Lock()
		runs++
		mu.Unlock()
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return runs }
	d := &background.Fake{}
	startBackgroundOn(t, s, d)
	// A check is due at the first look at the clock, which an hour's delay keeps from coming by itself.
	s.sched.Start(time.Hour, true)
	t.Cleanup(s.sched.Stop)

	d.ChangeOnline(false)
	d.Resume()
	if got := count(); got != 0 {
		t.Fatalf("a check ran offline: %d", got)
	}
	d.ChangeOnline(true)
	if got := count(); got != 1 {
		t.Errorf("checks after the network returned = %d, want 1", got)
	}
}

func TestTheGeneralAndUpdatesSettingsControlTheBackgroundBehaviour(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	tt.SetSize(windowWidth, 2400)
	click(t, tt, "Settings")
	click(t, tt, "General")
	requireText(t, tt, "Start with Windows", "Start minimized", "Show tray icon", "Close button minimizes to tray")
	for _, toggle := range []string{
		"Toggle starting with Windows", "Toggle starting minimized", "Toggle the tray icon",
		"Toggle minimizing to the tray when the window is closed",
	} {
		click(t, tt, toggle)
	}
	got := e.screen.prefs.Get()
	if !got.StartAtLogin || got.StartMinimized || got.ShowTrayIcon || got.CloseToTray {
		t.Errorf("general preferences = %+v", got)
	}

	e.screen.openSettings("updates")
	requireText(t, tt, "Check for updates", "Notify me about updates", "Install updates automatically", "Every 6 hours")
	click(t, tt, "How often to check for updates")
	click(t, tt, "Every 30 minutes")
	click(t, tt, "Toggle update notifications")
	click(t, tt, "Toggle installing updates automatically")
	e.settle()
	got = e.screen.prefs.Get()
	if got.CheckInterval != "30m" || got.NotifyUpdates || !got.AutoInstall {
		t.Errorf("update preferences = %+v", got)
	}
}

func TestAnItemCanBeSetToUpdateAutomaticallyFromItsDrawer(t *testing.T) {
	f := newAutoFixture(t)
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-1-win-x64.zip", map[string][]byte{"app.exe": gui("v1")}))
	entry := f.track("app", catalog.AddOptions{})
	f.show(entry)
	requireText(t, f.tt, "Update automatically")
	click(t, f.tt, "Update automatically")
	f.settle()
	if !f.entry(entry).Item.AutoUpdate {
		t.Error("the checkbox did not turn automatic updates on for the Item")
	}
}
