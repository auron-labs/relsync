package sync

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"
)

const (
	// TombstoneLife is how long a removal is remembered. A device that has not
	// synced for longer can bring a removed entry back.
	TombstoneLife = 90 * 24 * time.Hour
	// Heartbeat is how often a device rewrites the document when nothing else
	// changed, so the others can see it is alive.
	Heartbeat = 6 * time.Hour
)

// Merge combines two copies of the document into one. It is symmetric (the
// order of a and b does not matter) and idempotent (merging the result with
// either again changes nothing), so devices can sync in any order, any number
// of times, and agree. The copy of an entry changed later wins as a whole; on
// an exact tie the one that encodes larger wins, so both sides choose alike.
// A removal beats an entry that was not added or changed after it, and is
// dropped once an entry outlives it or after TombstoneLife. now only decides
// which removals have expired and is the merged document's time.
func Merge(a, b Document, now time.Time) Document {
	now = now.UTC()
	tombstones := map[string]time.Time{}
	for _, source := range []map[string]time.Time{a.Tombstones, b.Tombstones} {
		for key, at := range source {
			if known, ok := tombstones[key]; !ok || at.After(known) {
				tombstones[key] = at.UTC()
			}
		}
	}

	items := settle(newest(slices.Concat(a.Items, b.Items)), tombstones)
	later := settle(newest(slices.Concat(a.TryLater, b.TryLater)), tombstones)
	groups := settle(newest(slices.Concat(a.Groups, b.Groups)), tombstones)
	devices := newest(slices.Concat(slices.Collect(maps.Values(a.Devices)), slices.Collect(maps.Values(b.Devices))))

	merged := Document{
		Format: Format, Version: Version, UpdatedAt: now,
		Items: sortedValues(items), TryLater: sortedValues(later), Groups: sortedValues(groups),
		Tombstones: map[string]time.Time{}, Devices: devices,
	}
	for key, at := range tombstones {
		if at.After(now.Add(-TombstoneLife)) {
			merged.Tombstones[key] = at
		}
	}
	return merged
}

// entry is a part of the document that is replaced by a later copy of itself.
type entry[T any] interface {
	// Key names what the entry is; an entry with no key is unusable.
	Key() string
	// Stamp is when the entry was last changed.
	Stamp() time.Time
	// utc is the entry with its times in UTC and empty collections made nil.
	utc() T
}

// newest keeps, of the entries with the same key, the one with the latest
// stamp, and the one that encodes larger when the stamps are equal. Entries
// with no key are dropped. Devices are kept by id.
func newest[T entry[T]](entries []T) map[string]T {
	kept := map[string]T{}
	for _, e := range entries {
		e = e.utc()
		key := e.Key()
		if key == "" {
			continue
		}
		if have, ok := kept[key]; !ok || prefer(e, have) {
			kept[key] = e
		}
	}
	return kept
}

// prefer reports that candidate beats have.
func prefer[T entry[T]](candidate, have T) bool {
	if cs, hs := candidate.Stamp(), have.Stamp(); !cs.Equal(hs) {
		return cs.After(hs)
	}
	return bytes.Compare(encoded(candidate), encoded(have)) > 0
}

func encoded(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		// Every field of the document encodes; this cannot happen.
		panic("encode sync document: " + err.Error())
	}
	return out
}

// settle applies the tombstones to the entries. A tombstone at or after an
// entry's stamp removes it; a tombstone older than the entry is dropped, as the
// entry was added or changed after the removal. Devices have no tombstones.
func settle[T entry[T]](entries map[string]T, tombstones map[string]time.Time) map[string]T {
	for key, e := range entries {
		removedAt, ok := tombstones[key]
		switch {
		case !ok:
		case removedAt.Before(e.Stamp()):
			delete(tombstones, key)
		default:
			delete(entries, key)
		}
	}
	return entries
}

func sortedValues[T any](entries map[string]T) []T {
	out := make([]T, 0, len(entries))
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		out = append(out, entries[key])
	}
	return out
}

// Signature is what the document says, without the bookkeeping that changes
// on every sync: when it was written, by whom, and when each device last
// looked. Two documents with the same signature need no rewriting.
func (d Document) Signature() string {
	d = d.clean()
	d.UpdatedAt, d.WrittenBy = time.Time{}, ""
	for id, device := range d.Devices {
		device.LastSeen = time.Time{}
		d.Devices[id] = device
	}
	slices.SortFunc(d.Items, func(a, b Item) int { return strings.Compare(a.Key(), b.Key()) })
	slices.SortFunc(d.TryLater, func(a, b TryLaterEntry) int { return strings.Compare(a.Key(), b.Key()) })
	slices.SortFunc(d.Groups, func(a, b Group) int { return strings.Compare(a.Key(), b.Key()) })
	return string(encoded(d))
}
