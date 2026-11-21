package main

import (
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// argsEditor is the text of the installer arguments being edited in the
// drawer. saved is the Item's value it started from, so a change made
// elsewhere replaces what is shown.
type argsEditor struct {
	id          int64
	saved, text string
}

// installerOptions are the Item options of an App that may come with a setup
// program: its own installer arguments, the folder to install in and whether
// to install for everyone. Controls bind to local copies and commit through the
// catalog when changed.
func (s *screen) installerOptions(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	everyone := e.Item.InstallForEveryone
	components.Checkbox(c, &everyone, locales.DrawerInstallForEveryone()).OnChange(func() {
		s.setInstallForEveryone(e, everyone)
	})
	if s.argsEdit.id != e.Item.ID || s.argsEdit.saved != e.Item.InstallerArgs {
		s.argsEdit = argsEditor{id: e.Item.ID, saved: e.Item.InstallerArgs, text: e.Item.InstallerArgs}
	}
	labelled(c, locales.DrawerInstallerArgs(), func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
			components.TextBox(c, &s.argsEdit.text, components.TextBoxOptions{}).
				Label(locales.DrawerInstallerArgsLabel()).Placeholder(locales.DrawerInstallerArgsPlaceholder()).Grow(1).Shrink(1).MinWidth(0)
			unchanged := s.argsEdit.text == s.argsEdit.saved
			if components.Button(c, locales.DrawerInstallerArgsSave(), components.ButtonSecondary).Disabled(unchanged).Clicked() {
				s.setInstallerArgs(e, s.argsEdit.text)
			}
		})
		components.Text(c, locales.DrawerInstallerArgsHint()).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
	})
	labelled(c, locales.DrawerInstallFolder(), func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
			where := e.Item.InstallFolder
			if where == "" {
				where = locales.AddInstallLocationDefault()
			}
			components.Text(c, where).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
			if components.Button(c, locales.DownloadFolderBrowse(), components.ButtonSecondary).Clicked() {
				s.browseFolder(locales.AddInstallLocationChoose(), e.Item.InstallFolder, func(path string) {
					if problem := s.installFolderProblem(path); problem != "" {
						s.toast(components.ToastError, problem)
						return
					}
					s.setInstallFolder(e, path)
				})
			}
			if e.Item.InstallFolder != "" {
				if components.Button(c, locales.AddInstallLocationUseDefault(), components.ButtonGhost).Clicked() {
					s.setInstallFolder(e, "")
				}
			}
		})
		components.Text(c, locales.DrawerInstallFolderHint()).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
	})
}
