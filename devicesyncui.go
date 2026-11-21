package main

import (
	"strings"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// syncSettingsUI is what the sharing settings remember between frames. It is
// only touched on the UI thread.
type syncSettingsUI struct {
	// passphrase is what is typed in the passphrase field, and passphraseNote
	// says why it cannot be used.
	passphrase, passphraseNote string
	// name is the device name being edited; nameLoaded says it was read from
	// the preferences.
	name       string
	nameLoaded bool
}

// syncSettingsGroup is the "Your devices and sharing" part of Settings → Data.
func (s *screen) syncSettingsGroup() components.SettingsGroup {
	p := s.prefs.Get()
	items := []components.SettingsItem{{
		ID: "sync-mode", Title: locales.SyncMode(), Description: locales.SyncModeHint(),
		Keywords: []string{"sync", "devices", "gist", "folder", "share"},
		Build: func(c *ui.Context) {
			choice, changed := dropdown(c, locales.SyncModeLabel(), p.SyncMode, []option{
				{syncOff, locales.SyncModeOff()}, {syncGist, locales.SyncModeGist()}, {syncFolder, locales.SyncModeFolder()},
			})
			if changed {
				s.prefs.Update(func(p *preferences) { p.SyncMode = choice })
			}
		},
	}}
	switch p.SyncMode {
	case syncGist:
		items = append(items, components.SettingsItem{
			ID: "sync-gist", Title: locales.SyncGist(), Description: locales.SyncGistHint(), Keywords: []string{"gist", "github"},
			Build: func(c *ui.Context) {
				text := locales.SyncGistNone()
				if p.SyncGistID != "" {
					text = locales.SyncGistId(p.SyncGistID)
				}
				components.Text(c, text).SingleLine().Ellipsis("…")
			},
		})
	case syncFolder:
		items = append(items, components.SettingsItem{
			ID: "sync-folder", Title: locales.SyncFolder(), Description: locales.SyncFolderHint(appName), Stacked: true,
			Keywords: []string{"folder", "dropbox", "onedrive", "syncthing"},
			Build:    func(c *ui.Context) { s.syncFolderControl(c, p.SyncFolder) },
		})
	case syncOff:
	}
	items = append(items,
		components.SettingsItem{
			ID: "sync-passphrase", Title: locales.SyncPassphrase(), Description: locales.SyncPassphraseHint(), Stacked: true,
			Keywords: []string{"passphrase", "encrypt", "password", "lock"},
			Build:    func(c *ui.Context) { s.syncPassphraseControl(c, p.SyncEncrypt) },
		},
		components.SettingsItem{
			ID: "sync-device-name", Title: locales.SyncDeviceName(), Description: locales.SyncDeviceNameHint(), Stacked: true,
			Build: func(c *ui.Context) { s.syncDeviceNameControl(c, p) },
		},
	)
	if p.syncEnabled() {
		items = append(items, components.SettingsItem{
			ID: "sync-now", Title: locales.SyncStatusTitle(), Stacked: true, Keywords: []string{"sync"},
			Build: func(c *ui.Context) { s.syncNowControl(c) },
		})
		items = append(items, components.SettingsItem{
			ID: "sync-devices", Title: locales.SyncDevices(), Stacked: true, Keywords: []string{"devices"},
			Build: func(c *ui.Context) { s.syncDeviceList(c, p) },
		})
	}
	return components.SettingsGroup{ID: "sync", Title: locales.SyncSettingsTitle(), Items: items}
}

// syncFolderControl shows the shared folder, with buttons to change it and to
// open it.
func (s *screen) syncFolderControl(c *ui.Context, folder string) {
	t := c.Theme()
	if folder != "" {
		s.folderControl(c, folder, func() { s.chooseSyncFolder(folder) }, func() { s.openFolder(folder) })
		return
	}
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
		components.Text(c, locales.SyncFolderNone()).TextColor(t.TextMuted).Grow(1).Shrink(1).MinWidth(0)
		if components.Button(c, locales.DownloadFolderBrowse(), components.ButtonSecondary).Clicked() {
			s.chooseSyncFolder("")
		}
	})
}

func (s *screen) chooseSyncFolder(current string) {
	s.browseFolder(locales.SyncFolderChoose(), current, func(path string) {
		s.prefs.Update(func(p *preferences) { p.SyncFolder = path })
	})
}

// syncPassphraseControl is the passphrase field with its buttons.
func (s *screen) syncPassphraseControl(c *ui.Context, locked bool) {
	t := c.Theme()
	st := &s.syncPage
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.TextBox(c, &st.passphrase, components.TextBoxOptions{}).
			Label(locales.SyncPassphraseLabel()).Placeholder(locales.SyncPassphrasePlaceholder()).Password().
			FillWidth().MaxWidth(tokenFieldWidth)
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			use := components.Button(c, locales.SyncPassphraseSet(), components.ButtonPrimary).Disabled(st.passphrase == "")
			if use.Clicked() {
				if err := s.setSyncPassphrase(st.passphrase); err != nil {
					st.passphraseNote = locales.SyncPassphraseShort()
				} else {
					st.passphrase, st.passphraseNote = "", ""
				}
			}
			if locked && components.Button(c, locales.SyncPassphraseRemove(), components.ButtonOutline).Clicked() {
				st.passphrase, st.passphraseNote = "", ""
				s.removeSyncPassphrase()
			}
		})
		switch {
		case st.passphraseNote != "":
			components.Text(c, st.passphraseNote).FontSize(t.Rem(.85)).TextColor(t.Danger).Wrap()
		case locked:
			components.Text(c, locales.SyncPassphraseOn()).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
		}
	})
}

// syncDeviceNameControl is the name this device is listed under.
func (s *screen) syncDeviceNameControl(c *ui.Context, p preferences) {
	t := c.Theme()
	st := &s.syncPage
	if !st.nameLoaded {
		st.name, st.nameLoaded = p.SyncDeviceName, true
	}
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
		components.TextBox(c, &st.name, components.TextBoxOptions{}).
			Label(locales.SyncDeviceNameLabel()).Placeholder(s.devices.hostname).Grow(1).Shrink(1).MinWidth(0).MaxWidth(tokenFieldWidth)
		unchanged := strings.TrimSpace(st.name) == p.SyncDeviceName
		if components.Button(c, locales.SyncDeviceNameSave(), components.ButtonSecondary).Disabled(unchanged).Clicked() {
			name := strings.TrimSpace(st.name)
			s.prefs.Update(func(p *preferences) { p.SyncDeviceName = name })
			s.requestSync(false)
		}
	})
}

// syncNowControl is the Sync now button and the status line.
func (s *screen) syncNowControl(c *ui.Context) {
	t := c.Theme()
	status, started := s.jobs.Status(syncJob)
	running := started && status.State == jobs.Running
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		if components.Button(c, locales.SyncNow(), components.ButtonSecondary).Key("sync-now").Disabled(running).Clicked() {
			s.syncNow()
		}
		st := s.devices.status.Get()
		color := t.TextMuted
		if st.Problem != "" && !running {
			color = t.Danger
		}
		components.Text(c, s.syncStatusLine(running, st)).FontSize(t.Rem(.85)).TextColor(color).Wrap()
	})
}

// syncStatusLine says how the last sync went.
func (s *screen) syncStatusLine(running bool, st syncStatus) string {
	switch {
	case running:
		return locales.Syncing()
	case st.Problem != "":
		return locales.SyncStatusProblem(st.Problem)
	case st.Synced.IsZero():
		return locales.SyncStatusNever()
	}
	return locales.SyncStatusOk(st.Synced, st.Items, st.TryLater, max(len(st.Devices)-1, 0))
}

// syncDeviceList lists the devices that have taken part, this one first.
func (s *screen) syncDeviceList(c *ui.Context, p preferences) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		for _, d := range s.devices.status.Get().Devices {
			name := d.Name
			if d.ID == p.SyncDeviceID {
				name = locales.SyncDeviceThis(name)
			}
			ui.Column(c).Key(d.ID).FillWidth().Gap(t.Space(.5)).Children(func() {
				components.Text(c, name).Bold().SingleLine().Ellipsis("…")
				components.Text(c, locales.SyncDeviceLine(d.Platform, d.AppVersion, d.LastSeen.Local())).
					FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
			})
		}
	})
}
