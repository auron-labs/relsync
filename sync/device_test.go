package sync

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

// t0 is the start of every simulated day.
var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// at is the time minutes after t0.
func at(minutes float64) time.Time { return t0.Add(time.Duration(minutes * float64(time.Minute))) }

// device is a simulated device: a library kept as a document, and the sync
// round applied to it as the catalog applies it.
type device struct {
	t     *testing.T
	self  Device
	cloud Transport
	keys  Keys
	// lib is the device's library: its Items, saved repositories, groups and
	// the removals not yet forgotten.
	lib Document
	// devices are the devices the last round saw.
	devices map[string]Device
	// during runs between reading the shared file and applying the result,
	// like the user editing while a sync is under way.
	during func()
}

func newDevice(t *testing.T, id, platform string, cloud Transport) *device {
	t.Helper()
	return &device{
		t: t, self: Device{ID: id, Name: id, Platform: platform, AppVersion: "1.0"}, cloud: cloud,
		lib: Document{Format: Format, Version: Version, Tombstones: map[string]time.Time{}},
	}
}

func repoItem(owner, repo string, when time.Time) Item {
	return Item{Source: "github", Owner: owner, Repo: repo, AddedAt: when, ModifiedAt: when}
}

func (d *device) item(owner, repo string) (Item, bool) {
	key := repoItem(owner, repo, time.Time{}).Key()
	for _, i := range d.lib.Items {
		if i.Key() == key {
			return i, true
		}
	}
	return Item{}, false
}

func (d *device) itemKeys() []string {
	keys := make([]string, 0, len(d.lib.Items))
	for _, i := range d.lib.Items {
		keys = append(keys, i.Owner+"/"+i.Repo)
	}
	slices.Sort(keys)
	return keys
}

func (d *device) laterKeys() []string {
	keys := make([]string, 0, len(d.lib.TryLater))
	for _, l := range d.lib.TryLater {
		keys = append(keys, l.Owner+"/"+l.Repo)
	}
	slices.Sort(keys)
	return keys
}

func (d *device) groupNames() []string {
	names := make([]string, 0, len(d.lib.Groups))
	for _, g := range d.lib.Groups {
		names = append(names, g.Name)
	}
	slices.Sort(names)
	return names
}

// add tracks a repository, forgetting a removal of it as the catalog does.
func (d *device) add(owner, repo string, when time.Time) {
	i := repoItem(owner, repo, when)
	d.lib.Items = append(d.lib.Items, i)
	delete(d.lib.Tombstones, i.Key())
}

// edit changes an Item's note.
func (d *device) edit(owner, repo string, when time.Time, note string) {
	key := repoItem(owner, repo, time.Time{}).Key()
	for n, i := range d.lib.Items {
		if i.Key() == key {
			d.lib.Items[n].Note, d.lib.Items[n].ModifiedAt = note, when
			return
		}
	}
	d.t.Fatalf("%s has no %s/%s to edit", d.self.ID, owner, repo)
}

func (d *device) remove(owner, repo string, when time.Time) {
	key := repoItem(owner, repo, time.Time{}).Key()
	d.lib.Items = slices.DeleteFunc(d.lib.Items, func(i Item) bool { return i.Key() == key })
	d.lib.Tombstones[key] = when
}

func (d *device) save(owner, repo string, when time.Time) {
	e := TryLaterEntry{Source: "github", Owner: owner, Repo: repo, SavedAt: when, ModifiedAt: when}
	d.lib.TryLater = append(d.lib.TryLater, e)
	delete(d.lib.Tombstones, e.Key())
}

func (d *device) unsave(owner, repo string, when time.Time) {
	key := TryLaterEntry{Source: "github", Owner: owner, Repo: repo}.Key()
	d.lib.TryLater = slices.DeleteFunc(d.lib.TryLater, func(e TryLaterEntry) bool { return e.Key() == key })
	d.lib.Tombstones[key] = when
}

func (d *device) note(owner, repo string, when time.Time, note string) {
	key := TryLaterEntry{Source: "github", Owner: owner, Repo: repo}.Key()
	for n, e := range d.lib.TryLater {
		if e.Key() == key {
			d.lib.TryLater[n].Note, d.lib.TryLater[n].ModifiedAt = note, when
		}
	}
}

func (d *device) addGroup(name string, when time.Time) {
	g := Group{Name: name, CreatedAt: when}
	d.lib.Groups = append(d.lib.Groups, g)
	delete(d.lib.Tombstones, g.Key())
}

func (d *device) removeGroup(name string, when time.Time) {
	key := GroupKey(name)
	d.lib.Groups = slices.DeleteFunc(d.lib.Groups, func(g Group) bool { return g.Key() == key })
	d.lib.Tombstones[key] = when
}

// sync runs a round at the given time and applies it, as the catalog does.
func (d *device) sync(now time.Time) (Result, error) {
	sent := d.snapshot()
	result, err := Round(context.Background(), d.cloud, sent, d.keys, d.self, now)
	if err != nil {
		return Result{}, err
	}
	if d.during != nil {
		d.during()
		d.during = nil
	}
	if result.Changed {
		d.apply(Plan(sent, result.Merged, d.snapshot()))
	}
	d.devices = result.Merged.Devices
	return result, nil
}

// snapshot is a copy of the library, which later edits do not change.
func (d *device) snapshot() Document {
	s := d.lib
	s.Items, s.TryLater, s.Groups = slices.Clone(s.Items), slices.Clone(s.TryLater), slices.Clone(s.Groups)
	s.Tombstones = maps.Clone(s.Tombstones)
	return s
}

// apply is what the catalog does with Changes, on a document.
func (d *device) apply(c Changes) {
	upsert(&d.lib.Items, c.Items)
	upsert(&d.lib.TryLater, c.TryLater)
	upsert(&d.lib.Groups, c.Groups)
	remove(&d.lib.Items, c.RemoveItems)
	remove(&d.lib.TryLater, c.RemoveTryLater)
	remove(&d.lib.Groups, c.RemoveGroups)
	for key, at := range c.Tombstones {
		if have, ok := d.lib.Tombstones[key]; !ok || at.After(have) {
			d.lib.Tombstones[key] = at
		}
	}
	for _, r := range c.ForgetTombstones {
		if d.lib.Tombstones[r.Key].Equal(r.Stamp) {
			delete(d.lib.Tombstones, r.Key)
		}
	}
}

func upsert[T entry[T]](library *[]T, changes []T) {
	for _, c := range changes {
		if i := slices.IndexFunc(*library, func(have T) bool { return have.Key() == c.Key() }); i >= 0 {
			(*library)[i] = c
		} else {
			*library = append(*library, c)
		}
	}
}

func remove[T entry[T]](library *[]T, removals []Removal) {
	for _, r := range removals {
		*library = slices.DeleteFunc(*library, func(have T) bool { return have.Key() == r.Key && have.Stamp().Equal(r.Stamp) })
	}
}

// mustSync runs a round that must work.
func (d *device) mustSync(now time.Time) Result {
	d.t.Helper()
	result, err := d.sync(now)
	if err != nil {
		d.t.Fatalf("%s syncing at %v: %v", d.self.ID, now.Sub(t0), err)
	}
	return result
}

func (d *device) writes() int {
	d.t.Helper()
	_, writes := d.cloud.(*Memory).Counts()
	return writes
}

func equalStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}
