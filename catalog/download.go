package catalog

import (
	"slices"

	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

// Offer is what an Item would install: the highest version in its lane that
// has a file for this machine, and the choice made in it.
type Offer struct {
	Release source.Release
	Result  selection.Result
}

// Pick is the file the Offer would install.
func (o Offer) Pick() selection.Candidate {
	pick, _ := o.Result.Pick()
	return pick
}

type offerCache struct {
	offer Offer
	found bool
}

func assetsOf(r source.Release) []selection.Asset {
	assets := make([]selection.Asset, len(r.Assets))
	for i, a := range r.Assets {
		assets[i] = selection.Asset{Name: a.Name, Size: a.Size}
	}
	return assets
}

// KindFor is the Item's kind for a release: the one the user chose, or the
// guess made from the release's files and the repository's topics.
func (e Entry) KindFor(r source.Release) selection.ItemKind {
	if e.Item.Kind != "" {
		return e.Item.Kind
	}
	return selection.GuessKind(assetsOf(r), e.Item.Topics, e.Item.Repo)
}

// DisplayKind is the Item's kind as the Library shows it: its kind for the
// release it offers, or what its repository's topics suggest when there is none.
func (e Entry) DisplayKind() selection.ItemKind {
	if release, ok := e.Offered(); ok {
		return e.KindFor(release)
	}
	return e.KindFor(source.Release{})
}

// Resolve chooses the file to install from one release.
func (e Entry) Resolve(r source.Release) selection.Result {
	return selection.Resolve(selection.Request{
		Assets: assetsOf(r), Repo: e.Item.Repo, Kind: e.KindFor(r), Method: e.Item.Method,
		Policy: e.Policy, Intent: e.Intent,
	})
}

// LatestInstallable walks the tracked releases from the highest version down
// and returns the first with a file for this machine. A newer release with
// nothing installable does not hide it.
func (e Entry) LatestInstallable() (Offer, bool) {
	if e.offer != nil {
		return e.offer.offer, e.offer.found
	}
	return e.findOffer()
}

func (e Entry) findOffer() (Offer, bool) {
	for _, r := range e.byVersion() {
		result := e.Resolve(r)
		if _, ok := result.Pick(); ok {
			return Offer{Release: r, Result: result}, true
		}
	}
	return Offer{}, false
}

// withOffer returns the Entry with its latest installable release worked out.
func (e Entry) withOffer() Entry {
	offer, found := e.findOffer()
	e.offer = &offerCache{offer: offer, found: found}
	return e
}

// Versions lists the tracked releases, highest version first.
func (e Entry) Versions() []source.Release { return e.byVersion() }

// byVersion lists the tracked releases, highest version first.
func (e Entry) byVersion() []source.Release {
	releases := e.Candidates()
	slices.SortStableFunc(releases, func(a, b source.Release) int {
		switch {
		case e.newer(a.Tag, b.Tag):
			return -1
		case e.newer(b.Tag, a.Tag):
			return 1
		}
		return 0
	})
	return releases
}

// Offered is the release to show as the Item's latest: the latest installable
// one, or when nothing is installable the newest release.
func (e Entry) Offered() (source.Release, bool) {
	if offer, ok := e.LatestInstallable(); ok {
		return offer.Release, true
	}
	return e.Latest()
}

// SkippedOver reports a newest release with no download for this machine, and
// the older release the Item offers instead.
func (e Entry) SkippedOver() (newest, offered source.Release, ok bool) {
	offer, found := e.LatestInstallable()
	newest, hasNewest := e.Latest()
	if !found || !hasNewest || newest.Tag == offer.Release.Tag {
		return source.Release{}, source.Release{}, false
	}
	return newest, offer.Release, true
}
