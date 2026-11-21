package main

import (
	"context"
	"log"
	"os"

	"github.com/auron-labs/mygo-framework/errorreport"

	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/selection"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

// settingsView renders the Settings page: a section per category, with this
// phase's settings filled in and the rest laid out for later phases.
func (s *screen) settingsView(c *ui.Context) {
	components.Settings(c, &s.settings, s.settingsCategories(), components.SettingsOptions{
		Narrow:            s.compact,
		Title:             locales.PageSettings(),
		SearchLabel:       locales.SettingsSearchLabel(),
		SearchPlaceholder: locales.SettingsSearchPlaceholder(),
		CategoryLabel:     locales.SettingsCategoryLabel(),
		ResultsTitle:      locales.SettingsResultsTitle(),
		EmptyText:         locales.SettingsSectionEmpty(),
	})
}

// settingsCategories is the Settings section layout: General, GitHub, Updates,
// Installation, Security, Appearance, Data.
func (s *screen) settingsCategories() []components.SettingsCategory {
	p := s.prefs.Get()
	return []components.SettingsCategory{
		{
			ID: "general", Title: locales.SettingsGeneral(), Icon: lucide.SlidersHorizontal(),
			Groups: s.generalGroups(),
		},
		{ID: settingsGitHub, Title: locales.SettingsGitHub(), Icon: lucide.KeyRound(), Groups: s.githubSettingsGroups()},
		{ID: "updates", Title: locales.SettingsUpdates(), Icon: lucide.RefreshCw(), Groups: s.updatesSettingsGroups()},
		{
			ID: "installation", Title: locales.SettingsInstallation(), Icon: lucide.HardDriveDownload(),
			Groups: s.installationSettingsGroups(),
		},
		{ID: "security", Title: locales.SettingsSecurity(), Icon: lucide.ShieldCheck(), Groups: s.securitySettingsGroups()},
		{
			ID: "appearance", Title: locales.SettingsAppearance(), Icon: lucide.SunMoon(),
			Groups: s.appearanceGroups(p),
		},
		{
			ID: "data", Title: locales.SettingsData(), Icon: lucide.Database(),
			Groups: s.dataSettingsGroups(),
		},
	}
}

// updatesSettingsGroups holds the Updates section: when and how checks run.
func (s *screen) updatesSettingsGroups() []components.SettingsGroup {
	p := s.prefs.Get()
	onStartup, prereleases, parallel := p.CheckOnStartup, p.DefaultIncludePrereleases, float64(p.ParallelChecks)
	notify, auto := p.NotifyUpdates, p.AutoInstall
	return []components.SettingsGroup{{ID: "checks", Items: []components.SettingsItem{
		{
			ID: "check-interval", Title: locales.CheckInterval(), Description: locales.CheckIntervalHint(appName),
			Build: func(c *ui.Context) {
				choice, changed := dropdown(c, locales.CheckIntervalLabel(), p.CheckInterval, intervalOptions())
				if changed {
					s.prefs.Update(func(p *preferences) { p.CheckInterval = choice })
				}
			},
		},
		{
			ID: "check-on-startup", Title: locales.CheckOnStartup(), Description: locales.CheckOnStartupHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &onStartup, locales.CheckOnStartupLabel(), components.SwitchOptions{HideLabel: true})
				if onStartup != p.CheckOnStartup {
					s.prefs.Update(func(p *preferences) { p.CheckOnStartup = onStartup })
				}
			},
		},
		{
			ID: "notify-updates", Title: locales.NotifyUpdates(), Description: locales.NotifyUpdatesHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &notify, locales.NotifyUpdatesLabel(), components.SwitchOptions{HideLabel: true})
				if notify != p.NotifyUpdates {
					s.prefs.Update(func(p *preferences) { p.NotifyUpdates = notify })
				}
			},
		},
		{
			ID: "auto-install", Title: locales.AutoInstall(), Description: locales.AutoInstallHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &auto, locales.AutoInstallLabel(), components.SwitchOptions{HideLabel: true})
				if auto != p.AutoInstall {
					s.setAutoInstall(auto)
				}
			},
		},
		{
			ID: "parallel-checks", Title: locales.ParallelChecks(), Description: locales.ParallelChecksHint(),
			Build: func(c *ui.Context) {
				input := components.NumberInput(c, &parallel, minParallelChecks, maxParallelChecks, 1).Label(locales.ParallelChecksLabel())
				input.Width(120)
				if input.Changed() {
					s.prefs.Update(func(p *preferences) { p.ParallelChecks = int(parallel) })
				}
			},
		},
		{
			ID: "default-prereleases", Title: locales.DefaultPrereleases(), Description: locales.DefaultPrereleasesHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &prereleases, locales.DefaultPrereleasesLabel(), components.SwitchOptions{HideLabel: true})
				if prereleases != p.DefaultIncludePrereleases {
					s.prefs.Update(func(p *preferences) { p.DefaultIncludePrereleases = prereleases })
				}
			},
		},
	}}}
}

// intervalOptions are the choices of how often to check, shortest first.
func intervalOptions() []option {
	labels := map[string]string{
		"15m": locales.Interval15m(), "30m": locales.Interval30m(), "1h": locales.Interval1h(), "3h": locales.Interval3h(),
		"6h": locales.Interval6h(), "12h": locales.Interval12h(), "24h": locales.Interval24h(), background.Manual: locales.IntervalManual(),
	}
	options := make([]option, len(background.Intervals))
	for i, o := range background.Intervals {
		options[i] = option{o.Value, labels[o.Value]}
	}
	return options
}

// installationSettingsGroups holds the Installation section: which download
// is chosen, and where downloads are kept.
func (s *screen) installationSettingsGroups() []components.SettingsGroup {
	p := s.prefs.Get()
	installers, keep := p.PreferInstallers, p.KeepDownloads
	installAfter, shortcuts := p.InstallAfterAdding, p.StartMenuShortcuts
	silent, everyone := p.SilentInstalls, p.InstallForEveryone
	return []components.SettingsGroup{{ID: "install", Items: []components.SettingsItem{
		{
			ID: "install-after-adding", Title: locales.InstallAfterAdding(), Description: locales.InstallAfterAddingHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &installAfter, locales.InstallAfterAddingLabel(), components.SwitchOptions{HideLabel: true})
				if installAfter != p.InstallAfterAdding {
					s.prefs.Update(func(p *preferences) { p.InstallAfterAdding = installAfter })
				}
			},
		},
		{
			ID: "portable-folder", Title: locales.PortableFolder(), Description: locales.PortableFolderHint(), Stacked: true,
			Build: func(c *ui.Context) {
				folder := s.appsFolder(p)
				s.folderControl(c, folder, func() {
					s.browseFolder(locales.PortableFolderChoose(), folder, func(path string) {
						s.prefs.Update(func(p *preferences) { p.PortableFolder = path })
					})
				}, func() { s.openFolder(folder) })
			},
		},
		{
			ID: "tools-folder", Title: locales.ToolsFolder(), Description: locales.ToolsFolderHint(), Stacked: true,
			Build: func(c *ui.Context) {
				folder := s.toolsFolder(p)
				s.folderControl(c, folder, func() {
					s.browseFolder(locales.ToolsFolderChoose(), folder, func(path string) {
						s.prefs.Update(func(p *preferences) { p.ToolsFolder = path })
					})
				}, func() { s.openFolder(folder) })
			},
		},
		{
			ID: "start-menu-shortcuts", Title: locales.StartMenuShortcuts(), Description: locales.StartMenuShortcutsHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &shortcuts, locales.StartMenuShortcutsLabel(), components.SwitchOptions{HideLabel: true})
				if shortcuts != p.StartMenuShortcuts {
					s.prefs.Update(func(p *preferences) { p.StartMenuShortcuts = shortcuts })
				}
			},
		},
		{
			ID: "silent-installs", Title: locales.SilentInstalls(), Description: locales.SilentInstallsHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &silent, locales.SilentInstallsLabel(), components.SwitchOptions{HideLabel: true})
				if silent != p.SilentInstalls {
					s.prefs.Update(func(p *preferences) { p.SilentInstalls = silent })
				}
			},
		},
		{
			ID: "install-for-everyone", Title: locales.InstallForEveryone(), Description: locales.InstallForEveryoneHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &everyone, locales.InstallForEveryoneLabel(), components.SwitchOptions{HideLabel: true})
				if everyone != p.InstallForEveryone {
					s.prefs.Update(func(p *preferences) { p.InstallForEveryone = everyone })
				}
			},
		},
	}}, {ID: "download", Items: []components.SettingsItem{
		{
			ID: "architecture", Title: locales.Architecture(), Description: locales.ArchitectureHint(),
			Build: func(c *ui.Context) {
				choice, changed := dropdown(c, locales.ArchitectureLabel(), p.PreferredArchitecture, []option{
					{archAuto, locales.ArchitectureAuto()},
					{selection.ArchX64, locales.ArchitectureX64()},
					{selection.ArchARM64, locales.ArchitectureArm64()},
					{selection.ArchX86, locales.ArchitectureX86()},
				})
				if changed {
					s.prefs.Update(func(p *preferences) { p.PreferredArchitecture = choice })
				}
			},
		},
		{
			ID: "prefer-installers", Title: locales.PreferInstallers(), Description: locales.PreferInstallersHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &installers, locales.PreferInstallersLabel(), components.SwitchOptions{HideLabel: true})
				if installers != p.PreferInstallers {
					s.prefs.Update(func(p *preferences) { p.PreferInstallers = installers })
				}
			},
		},
		{
			ID: "download-folder", Title: locales.DownloadFolder(), Description: locales.DownloadFolderHint(), Stacked: true,
			Build: func(c *ui.Context) {
				folder := s.downloadRoot(p)
				s.folderControl(c, folder, func() {
					s.browseFolder(locales.DownloadFolderChoose(), folder, func(path string) {
						s.prefs.Update(func(p *preferences) { p.DownloadFolder = path })
					})
				}, func() { s.openFolder(folder) })
			},
		},
		{
			ID: "keep-downloads", Title: locales.KeepDownloads(), Description: locales.KeepDownloadsHint(),
			Build: func(c *ui.Context) {
				components.Switch(c, &keep, locales.KeepDownloadsLabel(), components.SwitchOptions{HideLabel: true})
				if keep != p.KeepDownloads {
					s.prefs.Update(func(p *preferences) { p.KeepDownloads = keep })
				}
			},
		},
	}}}
}

// folderControl shows a folder with buttons to change it and to open it.
func (s *screen) folderControl(c *ui.Context, folder string, browse, open func()) {
	t := c.Theme()
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
		components.Text(c, folder).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
		if components.Button(c, locales.DownloadFolderBrowse(), components.ButtonSecondary).Clicked() {
			browse()
		}
		if components.Button(c, locales.DownloadFolderOpen(), components.ButtonSecondary).Clicked() {
			open()
		}
	})
}

// browseFolder asks which folder to use, then chosen is called with it.
func (s *screen) browseFolder(title, current string, chosen func(path string)) {
	s.browse(mygo.OpenDialogOptions{Title: title, DefaultPath: current, Directory: true, CreateDirectories: true}, chosen)
}

// browseProgram asks which program to use, then chosen is called with it.
func (s *screen) browseProgram(title, current string, chosen func(path string)) {
	s.browse(mygo.OpenDialogOptions{
		Title: title, DefaultPath: current, Filters: []mygo.FileFilter{{Name: locales.ProgramFiles(), Extensions: []string{"exe"}}},
	}, chosen)
}

// browse shows the open dialog, then chosen is called with the one path picked.
// The dialog blocks until it closes, so it runs off the UI thread.
func (s *screen) browse(opts mygo.OpenDialogOptions, chosen func(path string)) {
	if s.win == nil {
		return
	}
	opts.Parent = s.win
	go func() {
		paths, err := mygo.Dialog.Open(opts)
		if err != nil {
			log.Printf("choose %s: %v", opts.Title, err)
			return
		}
		if len(paths) == 1 {
			chosen(paths[0])
		}
	}()
}

// openFolder opens the folder in the file manager, creating it when nothing
// has been put there yet.
func (s *screen) openFolder(folder string) {
	s.background(func(context.Context) error {
		if err := os.MkdirAll(folder, 0o750); err != nil {
			log.Printf("create %s: %v", folder, err)
			s.toast(components.ToastError, locales.FolderOpenFailed())
			return nil
		}
		if err := s.reveal(folder); err != nil {
			log.Printf("open %s: %v", folder, err)
			s.toast(components.ToastError, locales.FolderOpenFailed())
		}
		return nil
	})
}

// generalGroups holds the General section's rows. "Send error reports" only
// exists in builds with an error-reporting DSN.
func (s *screen) generalGroups() []components.SettingsGroup {
	p := s.prefs.Get()
	confirm := p.ConfirmRemove
	login, minimized, tray, closeToTray := p.StartAtLogin, p.StartMinimized, p.ShowTrayIcon, p.CloseToTray
	items := []components.SettingsItem{{
		ID: "start-at-login", Title: locales.StartAtLogin(), Description: locales.StartAtLoginHint(appName),
		Build: func(c *ui.Context) {
			components.Switch(c, &login, locales.StartAtLoginLabel(), components.SwitchOptions{HideLabel: true})
			if login != p.StartAtLogin {
				s.prefs.Update(func(p *preferences) { p.StartAtLogin = login })
			}
		},
	}, {
		ID: "start-minimized", Title: locales.StartMinimized(), Description: locales.StartMinimizedHint(appName),
		Build: func(c *ui.Context) {
			components.Switch(c, &minimized, locales.StartMinimizedLabel(), components.SwitchOptions{HideLabel: true})
			if minimized != p.StartMinimized {
				s.prefs.Update(func(p *preferences) { p.StartMinimized = minimized })
			}
		},
	}, {
		ID: "show-tray-icon", Title: locales.ShowTrayIcon(), Description: locales.ShowTrayIconHint(appName),
		Build: func(c *ui.Context) {
			components.Switch(c, &tray, locales.ShowTrayIconLabel(), components.SwitchOptions{HideLabel: true})
			if tray != p.ShowTrayIcon {
				s.prefs.Update(func(p *preferences) { p.ShowTrayIcon = tray })
			}
		},
	}, {
		ID: "close-to-tray", Title: locales.CloseToTray(), Description: locales.CloseToTrayHint(appName),
		Build: func(c *ui.Context) {
			components.Switch(c, &closeToTray, locales.CloseToTrayLabel(), components.SwitchOptions{HideLabel: true})
			if closeToTray != p.CloseToTray {
				s.prefs.Update(func(p *preferences) { p.CloseToTray = closeToTray })
			}
		},
	}, {
		ID: "confirm-remove", Title: locales.ConfirmRemove(), Description: locales.ConfirmRemoveHint(),
		Build: func(c *ui.Context) {
			components.Switch(c, &confirm, locales.ConfirmRemoveLabel(), components.SwitchOptions{HideLabel: true})
			if confirm != p.ConfirmRemove {
				s.prefs.Update(func(p *preferences) { p.ConfirmRemove = confirm })
			}
		},
	}}
	if errorreport.Configured() {
		reports := p.ErrorReports
		items = append(items, components.SettingsItem{
			ID: "error-reports", Title: locales.ErrorReports(), Description: locales.ErrorReportsHint(),
			Build: func(c *ui.Context) {
				components.Checkbox(c, &reports, "").Label(locales.ErrorReportsLabel()).OnChange(func() {
					s.prefs.Update(func(p *preferences) { p.ErrorReports = reports })
				})
			},
		})
	}
	return []components.SettingsGroup{{ID: "general", Items: items}}
}

// appearanceGroups holds the Appearance section: the theme and how the Library
// is laid out and grouped.
func (s *screen) appearanceGroups(p preferences) []components.SettingsGroup {
	auto, sections := p.AutoGroups, p.GroupSections
	return []components.SettingsGroup{
		{ID: "appearance", Items: []components.SettingsItem{{
			ID: "theme", Title: locales.ThemeLabel(), Stacked: true,
			Build: func(c *ui.Context) { s.themeControl(c, p.Theme) },
		}}},
		{ID: "library", Items: []components.SettingsItem{
			{
				ID: "library-view", Title: locales.SettingsLibraryView(),
				Build: func(c *ui.Context) {
					choice, changed := dropdown(c, locales.SettingsLibraryView(), p.LibraryView, []option{
						{viewGrid, locales.SettingsViewGrid()}, {viewList, locales.SettingsViewList()},
					})
					if changed {
						s.prefs.Update(func(p *preferences) { p.LibraryView = choice })
					}
				},
			},
			{
				ID: "auto-groups", Title: locales.SettingsAutoGroups(), Description: locales.SettingsAutoGroupsHint(),
				Build: func(c *ui.Context) {
					components.Switch(c, &auto, locales.SettingsAutoGroups(), components.SwitchOptions{HideLabel: true})
					if auto != p.AutoGroups {
						s.prefs.Update(func(p *preferences) { p.AutoGroups = auto })
					}
				},
			},
			{
				ID: "group-sections", Title: locales.SettingsGroupSections(), Description: locales.SettingsGroupSectionsHint(),
				Build: func(c *ui.Context) {
					components.Switch(c, &sections, locales.SettingsGroupSections(), components.SwitchOptions{HideLabel: true})
					if sections != p.GroupSections {
						s.prefs.Update(func(p *preferences) { p.GroupSections = sections })
					}
				},
			},
		}},
	}
}

// themeControl applies the chosen appearance immediately.
func (s *screen) themeControl(c *ui.Context, current string) {
	appearance := current
	ui.RadioGroup(c, func() {
		for _, option := range []struct{ value, label string }{
			{themeSystem, locales.ThemeSystem()}, {themeLight, locales.ThemeLight()}, {themeDark, locales.ThemeDark()},
		} {
			components.Radio(c, &appearance, option.value, option.label).OnChange(func() {
				s.prefs.Update(func(p *preferences) { p.Theme = appearance })
			})
		}
	})
}
