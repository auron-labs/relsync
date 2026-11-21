package main

import (
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/oneo-me/onevra/source/components"
)

// reinstallTag is the release a Reinstall would install again: the one the
// Item is at, when this application installed it and the release still has a
// file to install.
func reinstallTag(e catalog.Entry) (string, bool) {
	in, managed := e.Installed()
	if !managed {
		return "", false
	}
	release, found := e.Release(in.Tag)
	if !found {
		return "", false
	}
	result := e.Resolve(release)
	_, picked := result.Pick()
	return in.Tag, picked && !result.IntentBroken
}

// repositoryOf is what is known of an Item's repository, as the repository
// dialog shows it before it looks the repository up again.
func repositoryOf(e catalog.Entry) source.Repository {
	it := e.Item
	return source.Repository{
		Source: it.Source, Owner: it.Owner, Name: it.Repo, Description: it.Description, Language: it.Language, Topics: it.Topics,
		Stars: it.Stars, AvatarURL: e.AvatarURL(), URL: source.RepoURL(e.Ref()),
	}
}

// itemMenu is the menu of an Item's card or row. What does not apply to the
// Item is left out.
func (s *screen) itemMenu(m *components.Menu, r libraryRow) {
	e := r.entry
	id := e.Item.ID
	_, working := s.opOf(id)
	in, managed := e.Installed()
	offer, hasOffer := e.LatestInstallable()

	if !working {
		switch {
		case e.InstalledVersion() == "" && hasOffer:
			if m.Item(locales.ActionInstall()).Chosen() {
				s.startInstall(id, offer.Release.Tag, nil)
			}
		case e.UpdateAvailable() && hasOffer:
			if m.Item(locales.ActionUpdate()).Chosen() {
				s.startInstall(id, offer.Release.Tag, nil)
			}
		}
	}
	if managed && in.MainExe != "" && m.Item(locales.ActionLaunch()).Chosen() {
		s.launch(in)
	}
	if label, shown := locationLabel(in); managed && shown && m.Item(label).Chosen() {
		s.showInstall(e)
	}
	if m.Item(locales.CheckForUpdates()).Disabled(s.checking(id)).Chosen() {
		s.checkItem(id, true)
	}
	if m.Item(locales.MenuOpenDetails()).Chosen() {
		s.openItem(id)
	}
	favorite := locales.MenuAddFavorite()
	if e.Item.Favorite {
		favorite = locales.MenuRemoveFavorite()
	}
	if m.Item(favorite).Chosen() {
		s.setFavorite(e, !e.Item.Favorite)
	}
	m.Group(locales.MenuMoveToGroup(), func(m *components.Menu) { s.moveToGroupItems(m, e) })
	m.Group(locales.MenuManage(), func(m *components.Menu) { s.manageItems(m, e, working) })
	m.Group(locales.MenuProject(), func(m *components.Menu) {
		// Repository details and a developer's other projects come from GitHub only.
		if source.IsGitHub(e.Item.Source) {
			if m.Item(locales.MenuRepositoryDetails()).Chosen() {
				s.showRepository(repositoryOf(e))
			}
			if m.Item(locales.MenuMoreFromDeveloper()).Chosen() {
				s.openOwner(e.Item.Owner)
			}
		}
		if m.Item(openProjectLabel(e)).Chosen() {
			s.openRepository(e)
		}
	})
	m.Separator()
	hide := locales.MenuHide()
	if e.Item.Hidden {
		hide = locales.MenuUnhide()
	}
	if m.Item(hide).Chosen() {
		s.setHidden(e, !e.Item.Hidden)
	}
	stop := locales.DrawerStopTracking()
	if managed {
		stop = locales.DrawerStopTrackingInstalled()
	}
	if m.Item(stop).Disabled(working).Chosen() {
		s.stopTracking(e)
	}
	if managed && in.Removable() && !working && m.Item(locales.ActionUninstall()).Chosen() {
		s.confirmUninstall(e)
	}
}

// moveToGroupItems are the choices of group: automatic, each of the user's
// groups and a new one.
func (s *screen) moveToGroupItems(m *components.Menu, e catalog.Entry) {
	for _, o := range s.groupOptions() {
		id := parseGroupValue(o.value)
		if m.Item(o.label).Checked(e.Item.GroupID == id).Chosen() {
			s.moveToGroup(e, id)
		}
	}
	if m.Item(locales.MenuNewGroup()).Chosen() {
		s.newGroupDialog(&e)
	}
}

// manageItems are the actions that change what the application believes about
// the Item.
func (s *screen) manageItems(m *components.Menu, e catalog.Entry, working bool) {
	switch {
	case e.Skipped():
		if m.Item(locales.DrawerStopSkipping()).Chosen() {
			s.stopSkipping(e)
		}
	case e.UpdateAvailable():
		if m.Item(locales.DrawerSkipVersion()).Chosen() {
			s.skipVersion(e)
		}
	}
	if tag, ok := reinstallTag(e); ok && !working && m.Item(locales.MenuReinstall()).Chosen() {
		s.startInstall(e.Item.ID, tag, nil)
	}
	if _, ok := e.Latest(); ok && !working && m.Item(locales.DrawerMarkInstalled()).Chosen() {
		s.markInstalled(e)
	}
	if m.Item(locales.LinkGuessChange()).Chosen() {
		s.openRelinkDialog(e)
	}
}
