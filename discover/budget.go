package discover

import "github.com/aaronflorey/release-sync/source"

// The allowance below which optional work stops, so update checks always have
// enough left.
const (
	// prepareFloor stops preparing other lists in the background.
	prepareFloor = 1500
	// filterFloor pauses the look-up of downloads.
	filterFloor = 150
	// prepareSearches is how many searches must be ready before background work
	// takes one, which leaves the user's own searches first.
	prepareSearches = 11
)

// CanPrepare reports whether there is enough allowance to prepare lists the
// user has not asked for yet. It needs a token, and an unknown allowance is
// not a reason to stop.
func CanPrepare(a source.Allowance, hasToken bool) bool {
	return hasToken && (!a.Known || a.Remaining >= prepareFloor)
}

// CanCheckDownloads reports whether there is enough allowance to look up which
// projects have a download for this machine, which costs a request each.
func CanCheckDownloads(a source.Allowance, hasToken bool) bool {
	return hasToken && (!a.Known || a.Remaining >= filterFloor)
}
