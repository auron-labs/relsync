package main

import (
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/oneo-me/onevra/source/components"
)

// statusText is the pill's text for a status. busy is the running
// operation's progress text.
func statusText(e catalog.Entry, status catalog.Status, busy string) string {
	switch status {
	case catalog.StatusBusy:
		if busy != "" {
			return busy
		}
		return locales.StatusChecking()
	case catalog.StatusError:
		switch e.Item.LastFailure {
		case catalog.FailNotFound:
			return locales.StatusNotFound()
		case catalog.FailNoReleases:
			return locales.StatusNoReleases()
		case catalog.FailNone, catalog.FailOther:
		}
		return locales.StatusCheckFailed()
	case catalog.StatusAttention:
		return locales.StatusNeedsAttention()
	case catalog.StatusNoDownload:
		return locales.StatusNoDownload()
	case catalog.StatusUpdate:
		return locales.UpdateAvailable()
	case catalog.StatusIgnored:
		if latest, ok := e.LatestInstallable(); ok && e.Skipped() {
			return locales.StatusSkipped(latest.Release.Tag)
		}
		return locales.StatusUpdateIgnored()
	case catalog.StatusUpToDate:
		return locales.StatusUpToDate()
	case catalog.StatusNotInstalled:
		return locales.StatusNotInstalled()
	case catalog.StatusUnchecked:
	}
	return locales.StatusUnchecked()
}

// statusTone is the pill's colour for a status.
func statusTone(status catalog.Status) components.BadgeTone {
	switch status {
	case catalog.StatusUpdate, catalog.StatusBusy:
		return components.BadgeAccent
	case catalog.StatusError:
		return components.BadgeDanger
	case catalog.StatusIgnored, catalog.StatusAttention, catalog.StatusNoDownload:
		return components.BadgeWarning
	case catalog.StatusUpToDate:
		return components.BadgeSuccess
	case catalog.StatusNotInstalled, catalog.StatusUnchecked:
	}
	return components.BadgeNeutral
}

// versionText is the card's version line: the change when an update exists,
// the installed version when up to date, what is available when not
// installed, and a dash when nothing is known.
func versionText(e catalog.Entry) string {
	latest, hasLatest := e.LatestInstallable()
	installed := e.InstalledVersion()
	switch {
	case installed != "" && hasLatest && e.HasUpdate():
		return locales.VersionChange(installed, latest.Release.Tag)
	case installed != "":
		return installed
	case hasLatest:
		return locales.VersionAvailable(latest.Release.Tag)
	}
	return "—"
}
