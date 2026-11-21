package main

import (
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo"
)

// menu is the application menu bar. Its items call the same actions as the
// buttons, on the main thread like the view.
func (s *screen) menu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: locales.FileMenu(), Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleClose},
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleWindowMenu},
		{Label: locales.HelpMenu(), Submenu: []*mygo.MenuItem{
			{ID: "support-bundle", Label: locales.SaveSupportBundle(), Click: func(*mygo.MenuItem, *mygo.Window) {
				s.saveSupportBundle()
			}},
		}},
	})
}

// showWindow brings the window to the front, from the tray, a notification, a
// second launch or a deep link. The window may be hidden or minimized.
func (s *screen) showWindow() {
	if s.win == nil {
		return
	}
	s.win.Show()
	s.win.Restore()
	s.win.Focus()
}
