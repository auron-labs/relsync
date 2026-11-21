package main

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auron-labs/mygo-framework/errorreport"

	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo"
	"github.com/oneo-me/onevra/source/components"
)

// selfUpdateEvery is how often the application looks for a newer version of itself.
const selfUpdateEvery = 24 * time.Hour

// presence is the application as it exists without its window: the tray icon,
// the choices it follows, and whether it is quitting. The close handler runs on
// the UI thread and asks it questions, so the two it needs are atomic.
type presence struct {
	// trayUp says the tray icon is showing; quitting says a quit is under way.
	trayUp, quitting atomic.Bool

	mu   sync.Mutex
	tray background.Tray
	// login and interval are the values last applied, so a settings change that
	// touches neither does not apply them again.
	login    bool
	interval string
	// backupEvery is the Library backup interval last applied.
	backupEvery string
	// poke asks the worker to apply the preferences; the worker is the only
	// place that creates or removes the tray icon.
	poke chan struct{}
}

// closesToTray reports whether closing the window should hide it: the tray icon
// is there to bring it back, the user wants that, and the application is not
// quitting.
func (s *screen) closesToTray() bool {
	return s.presence.trayUp.Load() && !s.presence.quitting.Load() && s.prefs.Get().CloseToTray
}

// startsHidden reports whether the window stays hidden at launch: the system
// started the application at sign-in, the user asked for that to be quiet, and
// the tray icon is there to open it from.
func (s *screen) startsHidden() bool {
	return s.prefs.Get().StartMinimized && s.desktop.OpenedAtLogin() && s.presence.trayUp.Load()
}

// startBackground makes the application follow the preferences that outlive the
// window and starts the update schedule. isolated says the run keeps its data
// apart from the installed application's, so it must not take over what the
// system holds for that one. The returned function ends it all.
func (s *screen) startBackground(isolated bool) (stop func()) {
	p := s.prefs.Get()
	if !isolated {
		// The system's start-at-login entry is the truth: the user can switch it off outside.
		if open := s.desktop.StartsAtLogin(); open != p.StartAtLogin {
			s.prefs.Update(func(p *preferences) { p.StartAtLogin = open })
			p.StartAtLogin = open
		}
	}
	s.presence.login = p.StartAtLogin
	s.applyBackground()

	done := make(chan struct{})
	var worker sync.WaitGroup
	worker.Go(func() {
		for {
			select {
			case <-s.presence.poke:
				s.applyBackground()
			case <-done:
				return
			}
		}
	})
	undo := []func(){
		s.prefs.Subscribe(func(preferences) { s.pokeBackground() }),
		s.updateCount.Subscribe(func(int) { s.pokeBackground() }),
		s.desktop.OnResume(func() { s.sched.Wake(); s.selfSched.Wake(); s.backupSched.Wake(); s.devices.sched.Wake() }),
		s.desktop.OnOnline(func(online bool) {
			s.sched.SetOnline(online)
			s.selfSched.SetOnline(online)
			s.devices.sched.SetOnline(online)
		}),
		s.catalog.Entries().Subscribe(func([]catalog.Entry) { s.syncLocalChanged() }),
		s.catalog.TryLaterEntries().Subscribe(func([]catalog.TryLater) { s.syncLocalChanged() }),
		s.catalog.Groups().Subscribe(func([]catalog.Group) { s.syncLocalChanged() }),
	}
	online := s.desktop.Online()
	s.sched.SetOnline(online)
	s.selfSched.SetOnline(online)
	s.devices.sched.SetOnline(online)
	s.selfSched.SetInterval(selfUpdateEvery)
	s.selfSched.Start(0, false)
	s.backupSched.Start(backupStartDelay, libraryBackupOverdue(p, time.Now()))
	s.startSync()
	return func() {
		for _, off := range undo {
			off()
		}
		close(done)
		worker.Wait()
		s.sched.Stop()
		s.selfSched.Stop()
		s.backupSched.Stop()
		s.stopSync()
		s.removeTray()
	}
}

func (s *screen) pokeBackground() {
	select {
	case s.presence.poke <- struct{}{}:
	default:
	}
}

// applyBackground brings the tray icon, the start-at-login entry and the check
// interval in line with the preferences.
func (s *screen) applyBackground() {
	p := s.prefs.Get()
	s.presence.mu.Lock()
	defer s.presence.mu.Unlock()
	if p.CheckInterval != s.presence.interval {
		s.presence.interval = p.CheckInterval
		s.sched.SetInterval(p.checkEvery())
	}
	if p.LibraryBackupEvery != s.presence.backupEvery {
		s.presence.backupEvery = p.LibraryBackupEvery
		s.backupSched.SetInterval(p.backupEvery())
	}
	if p.StartAtLogin != s.presence.login {
		s.presence.login = p.StartAtLogin
		s.applyStartAtLogin(p.StartAtLogin)
	}
	s.applySyncSettings(p)
	s.applyTray(p.ShowTrayIcon)
}

// applyStartAtLogin asks the system to start the application at sign-in, or
// not. When it cannot, the setting goes back to what the system has.
func (s *screen) applyStartAtLogin(on bool) {
	err := s.desktop.SetStartAtLogin(on)
	if err == nil {
		return
	}
	log.Printf("set start at login to %v: %v", on, err)
	errorreport.Capture(err, "login.set", "ui")
	s.presence.login = !on
	s.prefs.Update(func(p *preferences) { p.StartAtLogin = !on })
	s.toast(components.ToastError, locales.StartAtLoginFailed(appName))
}

// applyTray shows the icon or removes it, and keeps its menu and tooltip
// current. The caller holds presence.mu.
func (s *screen) applyTray(show bool) {
	updates := s.updateCount.Get()
	toolTip, menu := s.trayToolTip(updates), s.trayMenu(updates)
	switch {
	case show && s.presence.tray == nil:
		tray, err := s.desktop.NewTray(background.TrayOptions{ToolTip: toolTip, Menu: menu, OnOpen: s.showWindow})
		if err != nil {
			// A desktop with no notification area is not a failure of the application.
			log.Printf("show tray icon: %v", err)
			return
		}
		s.presence.tray = tray
		s.presence.trayUp.Store(true)
	case !show && s.presence.tray != nil:
		s.removeTrayLocked()
	case s.presence.tray != nil:
		s.presence.tray.Set(toolTip, menu)
	}
}

func (s *screen) removeTray() {
	s.presence.mu.Lock()
	defer s.presence.mu.Unlock()
	s.removeTrayLocked()
}

func (s *screen) removeTrayLocked() {
	if s.presence.tray == nil {
		return
	}
	s.presence.trayUp.Store(false)
	s.presence.tray.Destroy()
	s.presence.tray = nil
}

func (s *screen) trayToolTip(updates int) string {
	if updates == 0 {
		return locales.TrayToolTip(appName)
	}
	return locales.TrayToolTipUpdates(appName, updates)
}

// trayMenu is the tray icon's menu: open, check, update everything when there
// is something to update, and quit.
func (s *screen) trayMenu(updates int) []background.MenuItem {
	menu := []background.MenuItem{
		{Label: locales.TrayOpen(), Click: s.showWindow},
		{Label: locales.CheckForUpdates(), Click: func() { s.checkLibrary(false) }},
	}
	if updates > 0 {
		menu = append(menu, background.MenuItem{Label: locales.TrayUpdateEverything(updates), Click: s.updateFromTray})
	}
	return append(menu, background.MenuItem{}, background.MenuItem{Label: locales.TrayQuit(), Click: mygo.App.Quit})
}

// updateFromTray shows the Updates page and updates everything on it.
func (s *screen) updateFromTray() {
	s.navigate("updates")
	s.showWindow()
	s.updateAll()
}

// notificationClicked opens the Updates page for a notification about updates,
// and just the window for any other.
func (s *screen) notificationClicked(id string) {
	if id == noticeAvailable || id == noticeInstalled {
		s.navigate("updates")
	}
	s.showWindow()
}

// tellAboutTray says once that closing the window left the application
// running, and how to quit it.
func (s *screen) tellAboutTray() {
	if s.prefs.Get().TrayNoticeShown {
		return
	}
	s.prefs.Update(func(p *preferences) { p.TrayNoticeShown = true })
	s.background(func(ctx context.Context) error {
		s.notify(ctx, background.Notice{ID: noticeTray, Title: locales.TrayNoticeTitle(appName), Body: locales.TrayNoticeBody()})
		return nil
	})
}
