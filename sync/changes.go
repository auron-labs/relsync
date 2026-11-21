package sync

import (
	"bytes"
	"maps"
	"slices"
	"time"
)

// Removal asks for an entry to be removed if it has not changed since it was
// sent: Stamp is its stamp in the document that was sent.
type Removal struct {
	Key   string
	Stamp time.Time
}

// Changes are what a device applies to its own library after a round.
type Changes struct {
	// Items, TryLater and Groups are to be created or replaced.
	Items    []Item
	TryLater []TryLaterEntry
	Groups   []Group
	// RemoveItems, RemoveTryLater and RemoveGroups are to be removed, unless the
	// user changed them since the round started.
	RemoveItems    []Removal
	RemoveTryLater []Removal
	RemoveGroups   []Removal
	// Tombstones are removals to record, and ForgetTombstones those that are
	// settled, with the time of the one that was sent.
	Tombstones       map[string]time.Time
	ForgetTombstones []Removal
}

// Empty reports that there is nothing to apply.
func (c Changes) Empty() bool {
	return len(c.Items)+len(c.TryLater)+len(c.Groups)+len(c.RemoveItems)+len(c.RemoveTryLater)+len(c.RemoveGroups)+
		len(c.Tombstones)+len(c.ForgetTombstones) == 0
}

// Plan works out what a device applies of the merged document. sent is the
// document the round started from and current the device's library now, which
// differs by what the user did while the round was under way. The rule is that
// merged only removes what was sent and unchanged since: whatever the user
// added, changed or removed meanwhile is left as it is, and goes out in the
// next round.
func Plan(sent, merged, current Document) Changes {
	var c Changes
	c.Items, c.RemoveItems = planEntries(sent.Items, merged.Items, current.Items)
	c.TryLater, c.RemoveTryLater = planEntries(sent.TryLater, merged.TryLater, current.TryLater)
	c.Groups, c.RemoveGroups = planEntries(sent.Groups, merged.Groups, current.Groups)
	for key, at := range merged.Tombstones {
		if have, ok := current.Tombstones[key]; !ok || at.After(have) {
			if c.Tombstones == nil {
				c.Tombstones = map[string]time.Time{}
			}
			c.Tombstones[key] = at
		}
	}
	for key, at := range sent.Tombstones {
		_, kept := merged.Tombstones[key]
		if have, ok := current.Tombstones[key]; !kept && ok && have.Equal(at) {
			c.ForgetTombstones = append(c.ForgetTombstones, Removal{Key: key, Stamp: at})
		}
	}
	return c
}

func planEntries[T entry[T]](sent, merged, current []T) (upsert []T, remove []Removal) {
	sentBy, mergedBy, currentBy := indexed(sent), indexed(merged), indexed(current)
	for _, key := range sortedKeys(mergedBy) {
		m := mergedBy[key]
		now, have := currentBy[key]
		was, wasSent := sentBy[key]
		switch {
		case !have && wasSent:
			// Removed here since the round started.
		case !have:
			upsert = append(upsert, m)
		case wasSent && !same(now, was):
			// Changed here since the round started.
		case wasSent && !same(now, m), !wasSent && prefer(m, now):
			upsert = append(upsert, m)
		}
	}
	for _, key := range sortedKeys(sentBy) {
		if _, kept := mergedBy[key]; kept {
			continue
		}
		if now, have := currentBy[key]; have && same(now, sentBy[key]) {
			remove = append(remove, Removal{Key: key, Stamp: now.Stamp()})
		}
	}
	return upsert, remove
}

func indexed[T entry[T]](entries []T) map[string]T {
	by := make(map[string]T, len(entries))
	for _, e := range entries {
		if key := e.Key(); key != "" {
			by[key] = e.utc()
		}
	}
	return by
}

// sortedKeys orders the keys, so what is applied does not depend on map order.
func sortedKeys[T any](by map[string]T) []string { return slices.Sorted(maps.Keys(by)) }

// same reports two copies of an entry that say the same.
func same[T entry[T]](a, b T) bool { return bytes.Equal(encoded(a.utc()), encoded(b.utc())) }
