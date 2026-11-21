package background

import "github.com/aaronflorey/release-sync/catalog"

// Unannounced are the Items whose latest update the user has not been told
// about. A held Item or a skipped version has no update to act on, so it is
// never among them.
func Unannounced(entries []catalog.Entry) []catalog.Entry {
	var out []catalog.Entry
	for _, e := range entries {
		if !e.HasUpdate() {
			continue
		}
		if offer, ok := e.LatestInstallable(); ok && offer.Release.Tag != e.Item.NotifiedVersion {
			out = append(out, e)
		}
	}
	return out
}
