package main

import (
	"slices"
	"strings"
	"time"

	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/scan"
	"github.com/aaronflorey/release-sync/source"

	"github.com/oneo-me/onevra/source/components"
)

// releasesState is how far the look at a linked repository's releases is.
type releasesState int

const (
	// releasesUnknown is a row that is not linked, so has none to look at.
	releasesUnknown releasesState = iota
	// releasesPending is a linked row whose releases have not been read yet.
	releasesPending
	// releasesKnown is a row whose releases were read.
	releasesKnown
	// releasesUnchecked is a row left unread because the request allowance ran out.
	releasesUnchecked
	// releasesFailed is a row whose releases could not be read.
	releasesFailed
)

// scanRow is one program the PC scan found, with what is known about the
// repository it is linked to. A row is replaced, not changed, when something
// about it does.
type scanRow struct {
	scan.Candidate
	// selected says the row is ticked to be added to the Library.
	selected bool
	// busy says a search by name or a link is under way for the row.
	busy bool
	// note says why the row is not linked, or what went wrong with it.
	note string
	// tracked says the repository is in the Library already.
	tracked bool

	releases releasesState
	// preview is what the site said about the linked repository, kept so adding
	// the row asks for nothing more.
	preview *catalog.Preview
	// latest is the newest release; adopted is the release the Library would
	// record for the version on the machine. Both are empty without releases.
	latest, adopted string
}

// canSelect reports a row that can be ticked: it has a repository that is not
// in the Library yet.
func (r scanRow) canSelect() bool { return r.Linked() && !r.tracked }

// ticked reports a row that will be added.
func (r scanRow) ticked() bool { return r.selected && r.canSelect() }

// hasUpdate reports a row whose version on the machine is behind the newest release.
func (r scanRow) hasUpdate() bool { return r.latest != "" && r.adopted != r.latest }

// withPreview returns the row with what the site said about its repository.
func (r scanRow) withPreview(p catalog.Preview, includePrereleases bool) scanRow {
	draft := p.Draft(catalog.AddOptions{IncludePrereleases: includePrereleases})
	r.preview, r.releases = &p, releasesKnown
	r.latest, r.adopted = "", ""
	if latest, ok := draft.Latest(); ok {
		r.latest, r.adopted = latest.Tag, draft.TagForVersion(r.Version)
	}
	return r
}

// linkLabel is how the row came to be linked, in words.
func (r scanRow) linkLabel() string {
	switch r.Link.Kind {
	case scan.LinkProgram:
		return locales.ScanKindProgram()
	case scan.LinkUser:
		return locales.ScanKindUser()
	case scan.LinkLikely:
		return locales.ScanKindLikely()
	case scan.LinkPossible:
		return locales.ScanKindPossible()
	case scan.LinkNone:
	}
	return ""
}

// status is the row's line about its releases, or why it is not linked.
func (r scanRow) status() string {
	switch {
	case r.tracked:
		return locales.ScanStatusTracked()
	case !r.Linked():
		return r.note
	case r.releases == releasesPending:
		return locales.ScanStatusChecking()
	case r.releases == releasesUnchecked:
		return locales.ScanStatusUnchecked()
	case r.releases == releasesFailed:
		return locales.ScanStatusFailed()
	case r.latest == "":
		return locales.ScanStatusNoReleases()
	case r.Version == "":
		return locales.ScanStatusLatest(r.latest)
	case r.hasUpdate():
		return locales.ScanStatusUpdate(r.Version, r.latest)
	}
	return locales.ScanStatusCurrent(r.latest)
}

// repoName is the repository the row is linked to, as the site spells it.
func (r scanRow) repoName() string {
	if r.preview != nil {
		return r.preview.Repository.Ref().String()
	}
	return r.Link.Ref.String()
}

// avatarURL is the linked repository's owner's avatar.
func (r scanRow) avatarURL() string {
	if r.preview != nil && r.preview.Repository.AvatarURL != "" {
		return r.preview.Repository.AvatarURL
	}
	if !source.IsGitHub(r.Link.Ref.Source) {
		return ""
	}
	return "https://github.com/" + r.Link.Ref.Owner + ".png?size=96"
}

// scanResults is what the Scan page shows. It is replaced, never changed, as
// the scan and the lookups run on other goroutines.
type scanResults struct {
	rows []scanRow
	// scanned says a scan has read the machine, so the lists are shown.
	scanned bool
	// result is the line under the card when no job is reporting progress.
	result string
}

func (r scanResults) linked() []scanRow {
	return slices.DeleteFunc(slices.Clone(r.rows), func(row scanRow) bool { return !row.Linked() })
}

// others are the rows with no repository, without the Windows, Microsoft and
// driver components when hideSystem is set.
func (r scanResults) others(hideSystem bool) []scanRow {
	return slices.DeleteFunc(slices.Clone(r.rows), func(row scanRow) bool {
		return row.Linked() || hideSystem && scan.SystemLike(row.Candidate)
	})
}

func (r scanResults) row(key string) (scanRow, bool) {
	i := slices.IndexFunc(r.rows, func(row scanRow) bool { return row.Key == key })
	if i < 0 {
		return scanRow{}, false
	}
	return r.rows[i], true
}

// edited returns the results with the row changed, or the results as they were
// when the row is gone.
func (r scanResults) edited(key string, change func(*scanRow)) scanResults {
	i := slices.IndexFunc(r.rows, func(row scanRow) bool { return row.Key == key })
	if i < 0 {
		return r
	}
	rows := slices.Clone(r.rows)
	change(&rows[i])
	r.rows = rows
	return r
}

// scanUI is what the Scan page remembers between frames apart from the
// results. It is only touched on the UI thread.
type scanUI struct {
	results *state.Cell[scanResults]
	// showSystem lists the Windows, Microsoft and driver components too.
	showSystem bool
	list       components.ListState
	// shown is what the last frame listed, for the list's keys.
	shown []scanItem
	// link is the dialog for linking a program by hand, while one is open.
	link *scanLinkState
}

func newScanUI() scanUI { return scanUI{results: state.NewCell(scanResults{})} }

// scanItem is one line of the Scan page's list.
type scanItem struct {
	kind scanItemKind
	row  scanRow
}

type scanItemKind int

const (
	itemNoLinks scanItemKind = iota
	itemLinkedHeader
	itemLinked
	itemOtherHeader
	itemOther
)

// Heights of the list's lines.
const (
	scanHeaderHeight  = 64
	scanNoLinksHeight = 72
	scanLinkedHeight  = 96
	scanOtherHeight   = 64
)

// items lays out the page's list: the programs linked to GitHub, then the others.
func (u *scanUI) items(r scanResults) []scanItem {
	var items []scanItem
	linked := r.linked()
	if len(linked) == 0 {
		items = append(items, scanItem{kind: itemNoLinks})
	} else {
		items = append(items, scanItem{kind: itemLinkedHeader})
		for _, row := range linked {
			items = append(items, scanItem{kind: itemLinked, row: row})
		}
	}
	items = append(items, scanItem{kind: itemOtherHeader})
	for _, row := range r.others(!u.showSystem) {
		items = append(items, scanItem{kind: itemOther, row: row})
	}
	return items
}

// itemKey identifies a line by its program, so lines keep their identity as
// the lists change.
func (u *scanUI) itemKey(i int) any {
	if i < 0 || i >= len(u.shown) {
		return i
	}
	it := u.shown[i]
	if it.kind == itemLinked || it.kind == itemOther {
		return it.row.Key
	}
	return int(it.kind)
}

// findAllLabel is the button that searches for every program with no repository,
// with how long that takes at the pace the search allowance sets.
func findAllLabel(count int, gap time.Duration) string {
	seconds := int((time.Duration(count) * gap).Seconds() + .999)
	if seconds >= 90 {
		return locales.ScanFindAllMinutes((seconds + 30) / 60)
	}
	return locales.ScanFindAllSeconds(seconds)
}

// scanRepoText is the repository line of a linked row: its name and stars.
func scanRepoText(r scanRow) string {
	text := r.repoName()
	if r.preview != nil && r.preview.Repository.Stars > 0 {
		text += " · " + locales.AddStars(r.preview.Repository.Stars)
	}
	return text
}

func scanVersionText(r scanRow) string {
	if strings.TrimSpace(r.Version) == "" {
		return locales.ScanVersionUnknown()
	}
	return r.Version
}

func scanTone(r scanRow) components.BadgeTone {
	if r.Link.Kind.Verified() {
		return components.BadgeAccent
	}
	return components.BadgeWarning
}
