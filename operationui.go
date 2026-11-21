package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

// operationPanel shows where an install, update or removal is, with Cancel
// while the machine is still untouched.
func (s *screen) operationPanel(c *ui.Context, e catalog.Entry, op operation) {
	t := c.Theme()
	ui.Column(c).Key("operation").FillWidth().Padding(t.Space(3)).Gap(t.Space(2)).Radius(t.Radius).Border(1, t.Border).Children(func() {
		components.Text(c, s.stageText(op)).Wrap()
		if op.stage == stageDownloading {
			components.Progress(c, operationFraction(op)).Label(locales.DownloadTitle())
		}
		if op.offersCancel() {
			ui.Row(c).FillWidth().Gap(t.Space(2)).Children(func() {
				if components.Button(c, locales.Cancel(), components.ButtonOutline).Key("cancel-operation").Clicked() {
					s.cancelOperation(e.Item.ID)
				}
			})
		}
	})
}

// installButtons are the actions on the Item's install: install or update it,
// launch it and find its files, and uninstall it. None is offered while an
// operation is running on it.
func (s *screen) installButtons(c *ui.Context, e catalog.Entry) {
	offer, hasOffer := e.LatestInstallable()
	in, managed := e.Installed()
	switch {
	case e.InstalledVersion() == "" && hasOffer:
		if components.Button(c, locales.ActionInstall(), components.ButtonPrimary).Clicked() {
			s.startInstall(e.Item.ID, offer.Release.Tag, nil)
		}
	case e.UpdateAvailable() && hasOffer:
		if components.Button(c, locales.ActionUpdate(), components.ButtonPrimary).Clicked() {
			s.startInstall(e.Item.ID, offer.Release.Tag, nil)
		}
	}
	if !managed {
		return
	}
	if in.MainExe != "" {
		if components.Button(c, locales.ActionLaunch(), components.ButtonSecondary).Clicked() {
			s.launch(in)
		}
	}
	if label, shown := locationLabel(in); shown {
		if components.Button(c, label, components.ButtonSecondary).Clicked() {
			s.showInstall(e)
		}
	}
	if in.Method == install.MethodPortable && in.Folder != "" {
		if components.Button(c, locales.ActionMove(), components.ButtonSecondary).Key("move").Clicked() {
			s.chooseMoveFolder(e)
		}
	}
	if in.Removable() {
		if components.Button(c, locales.ActionUninstall(), components.ButtonOutline).Clicked() {
			s.confirmUninstall(e)
		}
	}
}

// locationLabel is the label of the button that shows where an install is, and
// whether there is anywhere to show: a program, a Tool's command, or a folder.
func locationLabel(in catalog.Install) (string, bool) {
	switch {
	case in.MainExe != "" || in.Method == install.MethodTool:
		return locales.ActionSeeFileLocation(), true
	case in.Folder != "":
		return locales.ActionOpenFolder(), true
	}
	return "", false
}

// installedNote says what the person cannot do with an installer's program and
// how to go on: an MSIX app is opened from the Start menu, and a program the
// system does not list is launched once it is located.
func (s *screen) installedNote(c *ui.Context, e catalog.Entry) {
	in, managed := e.Installed()
	if !managed || in.Method != install.MethodInstaller {
		return
	}
	t := c.Theme()
	components.Text(c, locales.MoveInstallerNote()).TextColor(t.TextMuted).Wrap()
	switch {
	case in.Package != "":
		components.Text(c, locales.InstallFromStartMenu()).TextColor(t.TextMuted).Wrap()
	case !in.Removable():
		ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
			components.Text(c, locales.InstallUnlisted()).TextColor(t.TextMuted).Wrap().Shrink(1)
			if in.MainExe == "" {
				if components.Button(c, locales.InstallUnlistedLocate(), components.ButtonSecondary).Clicked() {
					s.browseProgram(locales.InstallUnlistedChoose(), in.Folder, func(path string) { s.locateProgram(e, path) })
				}
			}
		})
	}
}

// locateProgram records the program the person picked as the one an install the
// system does not list is started by.
func (s *screen) locateProgram(e catalog.Entry, path string) {
	s.mutate(e, func(ctx context.Context) error {
		return s.catalog.LocateProgram(ctx, e.Item.ID, filepath.Dir(path), filepath.Base(path))
	}, locales.ActivityLocated(e.FullName(), filepath.Base(path)))
}

// launch starts an installed app.
func (s *screen) launch(in catalog.Install) {
	path := filepath.Join(in.Folder, filepath.FromSlash(in.MainExe))
	s.background(func(context.Context) error {
		if err := s.platform.Launch(path); err != nil {
			log.Printf("start %s: %v", path, err)
			s.toast(components.ToastError, locales.ToastLaunchFailed(filepath.Base(path)))
		}
		return nil
	})
}

// showInstall reveals where an install is: the app's program, a Tool's first
// command, or the app's folder when it has no program.
func (s *screen) showInstall(e catalog.Entry) {
	in, ok := e.Installed()
	if !ok {
		return
	}
	id := e.Item.ID
	s.background(func(ctx context.Context) error {
		switch {
		case in.MainExe != "":
			s.showFile(filepath.Join(in.Folder, filepath.FromSlash(in.MainExe)))
		case in.Method == install.MethodTool:
			files, err := s.catalog.InstallFiles(ctx, id)
			if err != nil {
				return fmt.Errorf("list the files of %s: %w", e.FullName(), err)
			}
			if len(files) == 0 {
				return nil
			}
			s.showFile(filepath.Join(in.Folder, filepath.FromSlash(files[0].Path)))
		case in.Folder != "":
			if err := s.reveal(in.Folder); err != nil {
				log.Printf("open %s: %v", in.Folder, err)
				s.toast(components.ToastError, locales.OpenFolderFailed())
			}
		}
		return nil
	})
}

// confirmUninstall asks before removing what was installed, unless the person
// turned that off.
func (s *screen) confirmUninstall(e catalog.Entry) {
	in, ok := e.Installed()
	if !ok {
		return
	}
	if !s.prefs.Get().ConfirmRemove {
		s.startUninstall(e, false)
		return
	}
	var body string
	switch {
	case in.Method == install.MethodTool:
		body = locales.UninstallBodyCommands(strings.Join(in.Commands, ", "))
	case in.Method == install.MethodInstaller:
		body = locales.UninstallBodyInstaller(e.Title())
	case in.Dedicated:
		body = locales.UninstallBodyFolder(in.Folder, e.Title())
	default:
		body = locales.UninstallBodyFiles(e.Title(), in.Folder)
	}
	s.showDialog(locales.UninstallTitle(e.Title()), func(c *ui.Context) {
		t := c.Theme()
		ui.Column(c).Gap(t.Space(3)).MinWidth(380).MaxWidth(520).Children(func() {
			components.Text(c, locales.UninstallTitle(e.Title())).Bold().FontSize(t.Rem(1.1)).Wrap()
			components.Text(c, body).TextColor(t.TextMuted).Wrap()
			ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
				if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
					s.dialogOpen = false
				}
				if components.Button(c, locales.UninstallConfirm(), components.ButtonPrimary).Clicked() {
					s.dialogOpen = false
					s.startUninstall(e, false)
				}
			})
		})
	})
}

// versionAction is the button on a release in the list of versions: install it,
// reinstall it or roll back to it, by how it compares with what is installed.
func (s *screen) versionAction(c *ui.Context, e catalog.Entry, tag string) {
	release, ok := e.Release(tag)
	if !ok {
		return
	}
	result := e.Resolve(release)
	if _, found := result.Pick(); !found || result.IntentBroken {
		return
	}
	label := locales.VersionInstall()
	switch e.Relation(tag) {
	case catalog.RelationSame:
		label = locales.VersionReinstall()
	case catalog.RelationOlder:
		label = locales.VersionRollback()
	case catalog.RelationNewer, catalog.RelationFresh:
	}
	if components.Button(c, label, components.ButtonGhost).Key("install-" + tag).Clicked() {
		s.startInstall(e.Item.ID, tag, nil)
	}
}

// installedLines are the rows the version card adds for an install the
// application made.
func installedLines(e catalog.Entry) []struct{ label, value string } {
	in, ok := e.Installed()
	if !ok {
		return nil
	}
	lines := []struct{ label, value string }{{locales.DrawerInstalledIn(), in.Folder}}
	if len(in.Commands) > 0 {
		lines = append(lines, struct{ label, value string }{locales.DrawerCommands(), strings.Join(in.Commands, ", ")})
	}
	return lines
}
