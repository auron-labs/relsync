package sync

import (
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
)

// seeds is how many random cases each property is checked against.
const seeds = 400

var (
	universeRepos  = []string{"a/one", "b/two", "c/three", "d/four"}
	universeGroups = []string{"Games", "games", "Tools", "Media"}
	universeNotes  = []string{"", "x", "y", "z"}
)

func splitRepo(repo string) (owner, name string) {
	for i := range repo {
		if repo[i] == '/' {
			return repo[:i], repo[i+1:]
		}
	}
	return repo, ""
}

// randomDocument is a document with entries, removals and devices drawn from a
// small universe, with times that often tie, so that the cases that matter
// (equal stamps, a removal at the same moment as an edit) are common.
func randomDocument(r *rand.Rand, id string) Document {
	stamp := func() time.Time { return at(float64(r.IntN(6))) }
	doc := Document{Format: Format, Version: Version, Tombstones: map[string]time.Time{}, Devices: map[string]Device{}}
	for _, repo := range universeRepos {
		owner, name := splitRepo(repo)
		if r.IntN(2) == 0 {
			i := repoItem(owner, name, stamp())
			i.ModifiedAt = stamp()
			i.Note = universeNotes[r.IntN(len(universeNotes))]
			i.Favorite = r.IntN(2) == 0
			doc.Items = append(doc.Items, i)
		}
		if r.IntN(2) == 0 {
			l := TryLaterEntry{Source: "github", Owner: owner, Repo: name, SavedAt: stamp(), ModifiedAt: stamp()}
			l.Note = universeNotes[r.IntN(len(universeNotes))]
			doc.TryLater = append(doc.TryLater, l)
		}
		if r.IntN(3) == 0 {
			doc.Tombstones[repoItem(owner, name, time.Time{}).Key()] = stamp()
		}
		if r.IntN(3) == 0 {
			doc.Tombstones[TryLaterEntry{Source: "github", Owner: owner, Repo: name}.Key()] = stamp()
		}
	}
	for _, name := range universeGroups {
		if r.IntN(2) == 0 {
			doc.Groups = append(doc.Groups, Group{Name: name, CreatedAt: stamp()})
		}
		if r.IntN(3) == 0 {
			doc.Tombstones[GroupKey(name)] = stamp()
		}
	}
	for _, device := range []string{"pc", "mac", "thor"} {
		if r.IntN(2) == 0 {
			doc.Devices[device] = Device{ID: device, Name: device + id, Platform: "Windows", LastSeen: stamp()}
		}
	}
	return doc
}

// same reports two documents that say the same, apart from when they were written.
func sameDocument(a, b Document) bool {
	return a.Signature() == b.Signature() && deviceTimes(a) == deviceTimes(b)
}

// deviceTimes is when each device was last seen, which a signature leaves out.
func deviceTimes(d Document) string {
	return strconv.Itoa(len(d.Devices)) + string(encoded(d.clean().Devices))
}

func TestMergeIsSymmetricIdempotentAndAssociative(t *testing.T) {
	now := at(10)
	for seed := range uint64(seeds) {
		r := rand.New(rand.NewPCG(seed, 1)) //nolint:gosec // Seeded, so a failing case repeats.
		a, b, c := randomDocument(r, "a"), randomDocument(r, "b"), randomDocument(r, "c")
		ab := Merge(a, b, now)

		if ba := Merge(b, a, now); !sameDocument(ab, ba) {
			t.Fatalf("seed %d: merging a with b and b with a differ:\n%s\n%s", seed, ab.Signature(), ba.Signature())
		}
		for name, again := range map[string]Document{"with a": Merge(ab, a, now), "with b": Merge(ab, b, now), "with itself": Merge(ab, ab, now)} {
			if !sameDocument(ab, again) {
				t.Fatalf("seed %d: merging the result %s changed it:\n%s\n%s", seed, name, ab.Signature(), again.Signature())
			}
		}
		left, right := Merge(ab, c, now), Merge(a, Merge(b, c, now), now)
		if !sameDocument(left, right) {
			t.Fatalf("seed %d: (a+b)+c and a+(b+c) differ:\n%s\n%s", seed, left.Signature(), right.Signature())
		}
	}
}

func TestMergeDropsRemovalsOlderThanNinetyDaysAndKeepsRecentOnes(t *testing.T) {
	now := at(60 * 24 * 100)
	a := Document{Tombstones: map[string]time.Time{"item:github:old/old": at(60 * 24 * 5), "item:github:new/new": at(60 * 24 * 95)}}
	merged := Merge(a, Document{}, now)
	if _, ok := merged.Tombstones["item:github:old/old"]; ok {
		t.Error("a removal 95 days old was kept")
	}
	if _, ok := merged.Tombstones["item:github:new/new"]; !ok {
		t.Error("a removal 5 days old was dropped")
	}
}

// randomDevices simulates three devices making random edits at a clock that
// moves on, syncing in random order, and then everyone syncing until quiet.
func TestRandomEditsAcrossThreeDevicesConverge(t *testing.T) {
	for seed := range uint64(seeds) {
		r := rand.New(rand.NewPCG(seed, 2)) //nolint:gosec // Seeded, so a failing case repeats.
		cloud := &Memory{}
		devices := []*device{newDevice(t, "pc", "Windows", cloud), newDevice(t, "mac", "macOS", cloud), newDevice(t, "thor", "Linux", cloud)}
		clock := 0.0
		for range 40 {
			clock += float64(r.IntN(3)) // Often the same moment as another device.
			d := devices[r.IntN(len(devices))]
			randomOperation(r, d, at(clock))
			if r.IntN(3) == 0 {
				e := devices[r.IntN(len(devices))]
				if _, err := e.sync(at(clock)); err != nil {
					t.Fatalf("seed %d: %v", seed, err)
				}
			}
		}
		for range 2 {
			for _, d := range devices {
				clock++
				if _, err := d.sync(at(clock)); err != nil {
					t.Fatalf("seed %d: %v", seed, err)
				}
			}
		}
		want := devices[0].snapshot()
		for _, d := range devices[1:] {
			got := d.snapshot()
			if got.Signature() != want.Signature() {
				t.Fatalf("seed %d: %s and %s did not converge:\n%s\n%s", seed, devices[0].self.ID, d.self.ID, want.Signature(), got.Signature())
			}
		}
	}
}

// randomOperation makes one random change on the device at when.
func randomOperation(r *rand.Rand, d *device, when time.Time) {
	repo := universeRepos[r.IntN(len(universeRepos))]
	owner, name := splitRepo(repo)
	_, have := d.item(owner, name)
	switch r.IntN(7) {
	case 0:
		if !have {
			d.add(owner, name, when)
		}
	case 1:
		if have {
			d.edit(owner, name, when, universeNotes[r.IntN(len(universeNotes))])
		}
	case 2:
		if have {
			d.remove(owner, name, when)
		}
	case 3:
		if !hasLater(d, owner, name) {
			d.save(owner, name, when)
		}
	case 4:
		if hasLater(d, owner, name) {
			d.unsave(owner, name, when)
		}
	case 5:
		if group := universeGroups[r.IntN(len(universeGroups))]; !hasGroup(d, group) {
			d.addGroup(group, when)
		}
	case 6:
		if group := universeGroups[r.IntN(len(universeGroups))]; hasGroup(d, group) {
			d.removeGroup(group, when)
		}
	}
}

func hasLater(d *device, owner, name string) bool {
	key := TryLaterEntry{Source: "github", Owner: owner, Repo: name}.Key()
	for _, l := range d.lib.TryLater {
		if l.Key() == key {
			return true
		}
	}
	return false
}

func hasGroup(d *device, name string) bool {
	for _, g := range d.lib.Groups {
		if g.Key() == GroupKey(name) {
			return true
		}
	}
	return false
}
