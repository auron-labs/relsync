// Package catalog holds the Library: tracked repositories (Items), their cached
// releases and what is installed, with the derived state views show.
package catalog

import (
	"time"

	"github.com/aaronflorey/release-sync/category"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
	"github.com/aaronflorey/release-sync/version"
)

// FailureKind classifies why an Item has no usable release.
type FailureKind string

// The kinds of check failure an Item records.
const (
	FailNone FailureKind = ""
	// FailNotFound is a repository the source no longer knows.
	FailNotFound FailureKind = "not_found"
	// FailNoReleases is a repository that published nothing the Item tracks.
	FailNoReleases FailureKind = "no_releases"
	// FailOther is any other failed check.
	FailOther FailureKind = "failed"
)

// Item is one tracked repository.
type Item struct {
	ID     int64
	Source string
	Owner  string
	Repo   string
	// Lane is the tag prefix of the release track the Item follows when its
	// repository publishes several; empty follows every release.
	Lane string

	Name        string
	Description string
	AvatarURL   string
	Language    string
	Topics      []string
	Stars       int
	// MetadataFetched is set once the repository's description, stars and the
	// like have been fetched.
	MetadataFetched bool

	// Kind is what the user chose the Item to be; empty detects it.
	Kind selection.ItemKind
	// Method limits how an App may be installed; empty accepts any.
	Method selection.Method

	IncludePrereleases bool
	// HoldUpdates ignores updates for this Item.
	HoldUpdates bool
	// TrustUnsigned installs this Item's unsigned programs without asking.
	TrustUnsigned bool
	// SkippedVersion is the one release tag the user chose to skip.
	SkippedVersion string
	// AutoUpdate installs this Item's updates without asking, when it is safe
	// to do with nobody watching.
	AutoUpdate bool
	// NotifiedVersion is the release tag the user was last told about.
	NotifiedVersion string
	// LinkGuessed says the repository was matched by name, so it is not known
	// to be the project the installed program comes from.
	LinkGuessed bool
	// InstallFolder is where the user asked a Portable app to be installed;
	// empty is the default place.
	InstallFolder string
	// InstallError is why the last install, update or removal failed, for the
	// user; empty when it did not.
	InstallError string
	// InstallerArgs are the user's own arguments for the Item's setup program;
	// they replace the silent switches.
	InstallerArgs string
	// InstallForEveryone installs the Item for every user of the PC.
	InstallForEveryone bool

	// Category is the kind of software the repository is, worked out from its
	// metadata.
	Category category.Key
	// GroupID is the Item's own group; 0 leaves it in the group its category
	// gives it.
	GroupID int64
	// Favorite lists the Item before the others.
	Favorite bool
	// Hidden keeps the Item out of the Library's views, counts, update checks
	// and notifications.
	Hidden bool
	// Note and Tags are the user's own words about the Item.
	Note string
	Tags []string

	// RepoMissing is set when the source reports the repository as not found.
	RepoMissing bool
	ETag        string
	LastChecked time.Time
	LastFailure FailureKind
	// LastFailureText explains a failed check to the user.
	LastFailureText string
	AddedAt         time.Time
	// ModifiedAt is when the user last changed what the Item holds of their
	// own, zero if never since the Library kept the time. Syncing keeps the
	// later of two edits.
	ModifiedAt time.Time
}

// Install is what is on this machine for an Item: the version, and when this
// application put it there, what it put there.
type Install struct {
	Tag string
	At  time.Time
	// Method is how the application installed it; empty is a version the user
	// only says they have.
	Method install.Method
	// Asset is the release file it was installed from.
	Asset string
	// Folder is where the files are, and MainExe the program a Portable app
	// starts, relative to it.
	Folder  string
	MainExe string
	// Dedicated says Folder was made for the Item in the apps folder.
	Dedicated bool
	// Shortcut is the Start menu shortcut, if any.
	Shortcut string
	SHA256   string
	// Commands are the names a Tool's programs are run by.
	Commands []string
	// UninstallKey, Package, Scope and EntryVersion are what an installer's
	// install is recorded by; see install.Record.
	UninstallKey string
	Package      string
	Scope        platform.Scope
	EntryVersion string
}

// Removable reports an install this application can take off the machine.
func (i Install) Removable() bool {
	return install.Record{Method: i.Method, UninstallKey: i.UninstallKey, Package: i.Package}.Removable()
}

// Managed reports an install this application made, which it can update,
// launch and remove.
func (i Install) Managed() bool { return i.Method != "" }

// Entry is an Item with its cached releases (newest published first) and its
// install record, from which every derived value is computed.
type Entry struct {
	Item     Item
	Releases []source.Release
	Install  *Install
	// Intent is the choice of file kept for the Item; nil takes the best match.
	Intent *selection.Intent
	// Policy is what the machine and settings say about choosing a file.
	Policy selection.Policy

	// offer is the latest installable release, worked out once when the
	// catalog publishes the Entry; an Entry made any other way works it out
	// on every use.
	offer *offerCache
}

// Status is what a card's pill reports. The values are ordered for sorting:
// what needs attention comes first.
type Status int

// The statuses, first match wins in Entry.Status.
const (
	StatusAttention Status = iota
	StatusUpdate
	StatusBusy
	StatusError
	StatusNoDownload
	StatusIgnored
	StatusNotInstalled
	StatusUpToDate
	StatusUnchecked
)

// Ref names the Item's repository.
func (e Entry) Ref() source.RepoRef { return source.NewRef(e.Item.Source, e.Item.Owner, e.Item.Repo) }

// FullName is owner/repo.
func (e Entry) FullName() string { return e.Ref().String() }

// Title is the name to show: the repository's name.
func (e Entry) Title() string {
	if e.Item.Name != "" {
		return e.Item.Name
	}
	return e.Item.Repo
}

// AvatarURL is the owner's avatar, derived from the owner until the
// repository's metadata says otherwise; only GitHub's can be derived.
func (e Entry) AvatarURL() string {
	if e.Item.AvatarURL != "" || !source.IsGitHub(e.Item.Source) {
		return e.Item.AvatarURL
	}
	return "https://github.com/" + e.Item.Owner + ".png?size=96"
}

// Candidates are the releases the Item tracks: in its lane, pre-releases only
// when it includes them, and without throwaway builds unless nothing else is
// left. Newest published first.
func (e Entry) Candidates() []source.Release {
	return candidates(e.Releases, e.Item.IncludePrereleases, e.Item.Lane)
}

func candidates(releases []source.Release, includePrereleases bool, lane string) []source.Release {
	var tracked, stable []source.Release
	for _, r := range releases {
		if (r.Prerelease && !includePrereleases) || (lane != "" && version.Lane(r.Tag) != lane) {
			continue
		}
		tracked = append(tracked, r)
		if !version.IsOneOff(r.Tag) {
			stable = append(stable, r)
		}
	}
	if len(stable) > 0 {
		return stable
	}
	return tracked
}

// Latest is the highest version among the tracked releases, which is not
// necessarily the most recently published one.
func (e Entry) Latest() (source.Release, bool) {
	tracked := e.Candidates()
	if len(tracked) == 0 {
		return source.Release{}, false
	}
	best := tracked[0]
	for _, r := range tracked[1:] {
		if e.newer(r.Tag, best.Tag) {
			best = r
		}
	}
	return best, true
}

// Installed is the install this application made, if there is one.
func (e Entry) Installed() (Install, bool) {
	if e.Install == nil || !e.Install.Managed() {
		return Install{}, false
	}
	return *e.Install, true
}

// InstalledVersion is the version the user has, or "".
func (e Entry) InstalledVersion() string {
	if e.Install == nil {
		return ""
	}
	return e.Install.Tag
}

// UpdateAvailable reports an installed Item whose latest installable version
// is newer, whether or not updates are held or skipped.
func (e Entry) UpdateAvailable() bool {
	installed := e.InstalledVersion()
	latest, ok := e.LatestInstallable()
	return installed != "" && ok && e.newer(latest.Release.Tag, installed)
}

// Skipped reports that the latest installable version is the one the user skipped.
func (e Entry) Skipped() bool {
	latest, ok := e.LatestInstallable()
	return ok && e.Item.SkippedVersion != "" && e.Item.SkippedVersion == latest.Release.Tag
}

// HasUpdate reports an update the user should act on. A hidden Item has none:
// it is out of the counts, the Updates page and every notification.
func (e Entry) HasUpdate() bool {
	return e.updatePending() && !e.Item.Hidden
}

// updatePending is HasUpdate before hiding is taken into account, which is
// what a hidden Item's own status still says.
func (e Entry) updatePending() bool {
	return e.UpdateAvailable() && !e.Item.HoldUpdates && !e.Skipped()
}

// NeedsAttention reports an Item whose stored choice of file describes nothing
// in the release it would update to, so the file must be checked before
// updating. Held and skipped Items are left alone.
func (e Entry) NeedsAttention() bool {
	latest, ok := e.LatestInstallable()
	return ok && latest.Result.IntentBroken && !e.Item.HoldUpdates && !e.Skipped() &&
		e.InstalledVersion() != latest.Release.Tag
}

// Status is the Item's status. busy says an operation is running on it.
func (e Entry) Status(busy bool) Status {
	_, hasRelease := e.Latest()
	_, hasDownload := e.LatestInstallable()
	installed := e.InstalledVersion() != ""
	switch {
	case busy:
		return StatusBusy
	case e.Item.LastFailure != FailNone && !hasRelease:
		return StatusError
	case hasRelease && !hasDownload:
		return StatusNoDownload
	case e.NeedsAttention():
		return StatusAttention
	case e.updatePending():
		return StatusUpdate
	case e.UpdateAvailable():
		return StatusIgnored
	case installed:
		return StatusUpToDate
	case hasRelease:
		return StatusNotInstalled
	default:
		return StatusUnchecked
	}
}

// NewerReleases are the tracked releases newer than the installed version,
// newest first: what the release notes cover. It is empty when nothing is
// installed.
func (e Entry) NewerReleases() []source.Release {
	installed := e.InstalledVersion()
	if installed == "" {
		return nil
	}
	var newer []source.Release
	for _, r := range e.Candidates() {
		if e.newer(r.Tag, installed) {
			newer = append(newer, r)
		}
	}
	return newer
}

// LatestPublished is when the latest release was published, zero if unknown.
func (e Entry) LatestPublished() time.Time {
	latest, _ := e.Latest()
	return latest.Published
}

func (e Entry) newer(a, b string) bool {
	return version.IsNewer(a, b, e.publishedOf(a), e.publishedOf(b))
}

// publishedOf is when a tag was published. Only the newest releases are cached,
// so a tag that is not among them is treated as older than all of them.
func (e Entry) publishedOf(tag string) time.Time {
	var oldest time.Time
	for _, r := range e.Releases {
		if r.Tag == tag && !r.Published.IsZero() {
			return r.Published
		}
		if !r.Published.IsZero() && (oldest.IsZero() || r.Published.Before(oldest)) {
			oldest = r.Published
		}
	}
	if oldest.IsZero() {
		return time.Time{}
	}
	return oldest.Add(-time.Second)
}
