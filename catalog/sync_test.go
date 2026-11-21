package catalog_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
	libsync "github.com/aaronflorey/release-sync/sync"
)

var syncDay = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// minutes is the time a number of minutes into the simulated day.
func minutes(n float64) time.Time { return syncDay.Add(time.Duration(n * float64(time.Minute))) }

// syncDevice is a simulated device: a Library of its own on a temporary
// database, whose clock the test sets, sharing a file with the others.
type syncDevice struct {
	*harness
	self  libsync.Device
	cloud libsync.Transport
	now   time.Time
	// during runs between reading the shared file and applying the result.
	during func()
}

func newSyncDevice(t *testing.T, id string, cloud libsync.Transport) *syncDevice {
	t.Helper()
	d := &syncDevice{
		harness: openHarness(t), cloud: cloud, now: minutes(0),
		self: libsync.Device{ID: id, Name: id, Platform: "Linux", AppVersion: "1.0"},
	}
	d.catalog.SetClock(func() time.Time { return d.now })
	for _, name := range []string{"one", "two", "three", "four"} {
		d.source.publish("acme", name, page(`"e"`, rel("v1.0.0", 1)))
	}
	return d
}

// at moves the device's clock.
func (d *syncDevice) at(n float64) *syncDevice {
	d.now = minutes(n)
	return d
}

// track adds a repository to the Library at the current time.
func (d *syncDevice) track(name string) catalog.Entry { return d.add(name, catalog.AddOptions{}) }

func (d *syncDevice) entry(name string) catalog.Entry {
	d.t.Helper()
	e, ok := d.catalog.Find(source.RepoRef{Owner: "acme", Repo: name})
	if !ok {
		d.t.Fatalf("%s does not track %s", d.self.ID, name)
	}
	return e
}

func (d *syncDevice) tracks(name string) bool {
	_, ok := d.catalog.Find(source.RepoRef{Owner: "acme", Repo: name})
	return ok
}

func (d *syncDevice) tracked() []string {
	entries := d.catalog.Entries().Get()
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Item.Repo)
	}
	slices.Sort(names)
	return names
}

func (d *syncDevice) groupNames() []string {
	groups := d.catalog.Groups().Get()
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	slices.Sort(names)
	return names
}

func (d *syncDevice) sync(n float64) catalog.SyncOutcome {
	d.t.Helper()
	d.at(n)
	cloud := d.cloud
	if d.during != nil {
		cloud = &hookedTransport{Transport: cloud, afterRead: d.during}
		d.during = nil
	}
	outcome, err := d.catalog.Sync(context.Background(), cloud, libsync.Keys{}, d.self)
	if err != nil {
		d.t.Fatalf("%s syncing: %v", d.self.ID, err)
	}
	return outcome
}

// document is the Library as it would be shared, without the devices.
func (d *syncDevice) document() string {
	d.t.Helper()
	doc, err := d.catalog.SyncDocument(context.Background())
	if err != nil {
		d.t.Fatal(err)
	}
	return doc.Signature()
}

// hookedTransport runs a function once the shared file has been read, like the
// user changing the Library while a sync is under way.
type hookedTransport struct {
	libsync.Transport
	afterRead func()
}

func (h *hookedTransport) Read(ctx context.Context) (string, bool, error) {
	text, found, err := h.Transport.Read(ctx)
	h.afterRead()
	if err != nil {
		return "", false, fmt.Errorf("read the shared file: %w", err)
	}
	return text, found, nil
}

func sameList(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func trio(t *testing.T) (pc, mac, thor *syncDevice, cloud *libsync.Memory) {
	t.Helper()
	cloud = &libsync.Memory{}
	return newSyncDevice(t, "pc", cloud), newSyncDevice(t, "mac", cloud), newSyncDevice(t, "thor", cloud), cloud
}

func TestEditsAreMarkedAsChangedButChecksAndInstallsAreNot(t *testing.T) {
	d := newSyncDevice(t, "pc", &libsync.Memory{})
	added := d.at(1).track("one")
	if !added.Item.ModifiedAt.Equal(minutes(1)) {
		t.Fatalf("a new Item was modified at %v, want when it was added", added.Item.ModifiedAt)
	}

	steps := map[string]func(id int64) error{
		"a note":              func(id int64) error { return d.catalog.SetNote(t.Context(), id, "note") },
		"tags":                func(id int64) error { return d.catalog.SetTags(t.Context(), id, []string{"a"}) },
		"a favourite":         func(id int64) error { return d.catalog.SetFavorite(t.Context(), id, true) },
		"hiding":              func(id int64) error { return d.catalog.SetHidden(t.Context(), id, true) },
		"pre-releases":        func(id int64) error { return d.catalog.SetIncludePrereleases(t.Context(), id, true) },
		"holding updates":     func(id int64) error { return d.catalog.SetHoldUpdates(t.Context(), id, true) },
		"automatic updates":   func(id int64) error { return d.catalog.SetAutoUpdate(t.Context(), id, true) },
		"trusting unsigned":   func(id int64) error { return d.catalog.SetTrustUnsigned(t.Context(), id, true) },
		"a skipped version":   func(id int64) error { return d.catalog.SetSkippedVersion(t.Context(), id, "v1.0.0") },
		"installer arguments": func(id int64) error { return d.catalog.SetInstallerArgs(t.Context(), id, "/S") },
		"the kind":            func(id int64) error { return d.catalog.SetKind(t.Context(), id, selection.ItemTool) },
		"the install method":  func(id int64) error { return d.catalog.SetMethod(t.Context(), id, selection.MethodPortable) },
		"the chosen file": func(id int64) error {
			return d.catalog.SetIntent(t.Context(), id, &selection.Intent{Product: "x", Explicit: true})
		},
		"the group":             func(id int64) error { return d.catalog.SetGroup(t.Context(), id, 0) },
		"the chosen file again": func(id int64) error { return d.catalog.SetIntent(t.Context(), id, nil) },
	}
	n := 2.0
	for _, name := range []string{
		"a note", "tags", "a favourite", "hiding", "pre-releases", "holding updates", "automatic updates",
		"trusting unsigned", "a skipped version", "installer arguments", "the kind", "the install method", "the chosen file", "the group",
		"the chosen file again",
	} {
		d.at(n)
		if err := steps[name](added.Item.ID); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := d.get(added.Item.ID).Item.ModifiedAt; !got.Equal(minutes(n)) {
			t.Errorf("after %s the Item was modified at %v, want %v", name, got, minutes(n))
		}
		n++
	}

	// What nobody chose is not a change worth sharing.
	d.at(100)
	d.check(added.Item.ID, true)
	if err := d.catalog.SetNotifiedVersions(t.Context(), map[int64]string{added.Item.ID: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if got := d.get(added.Item.ID).Item.ModifiedAt; !got.Equal(minutes(n - 1)) {
		t.Errorf("a check changed the time the Item was modified to %v", got)
	}
}

func TestRemovalsAreRecordedAndAnythingBackIsNoLongerRemoved(t *testing.T) {
	d := newSyncDevice(t, "pc", &libsync.Memory{})
	one := d.at(1).track("one")
	group, err := d.catalog.CreateGroup(t.Context(), "Games")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := d.catalog.SaveForLater(t.Context(), source.Repository{Owner: "acme", Name: "two"}, "")
	if err != nil {
		t.Fatal(err)
	}
	removals := func() map[string]time.Time {
		doc, err := d.catalog.SyncDocument(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return doc.Tombstones
	}
	if got := removals(); len(got) != 0 {
		t.Fatalf("removals before anything was removed = %v", got)
	}

	d.at(5)
	if err := d.catalog.Remove(t.Context(), one.Item.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.catalog.DeleteGroup(t.Context(), group.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.catalog.RemoveTryLater(t.Context(), saved.ID); err != nil {
		t.Fatal(err)
	}
	got := removals()
	for _, key := range []string{"item:github:acme/one", "group:games", "later:github:acme/two"} {
		if !got[key].Equal(minutes(5)) {
			t.Errorf("removal of %s = %v, want it recorded at the time of the removal; all: %v", key, got[key], got)
		}
	}

	d.at(6).track("one")
	if _, err := d.catalog.CreateGroup(t.Context(), "GAMES"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.catalog.SaveForLater(t.Context(), source.Repository{Owner: "acme", Name: "two"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := removals(); len(got) != 0 {
		t.Errorf("removals after adding everything back = %v, want none", got)
	}
}

func TestTrackingARepositoryRemovesItFromTheSavedOnesAndSaysSo(t *testing.T) {
	d := newSyncDevice(t, "pc", &libsync.Memory{})
	if _, err := d.catalog.SaveForLater(t.Context(), source.Repository{Owner: "acme", Name: "two"}, ""); err != nil {
		t.Fatal(err)
	}
	d.at(3).track("two")
	doc, err := d.catalog.SyncDocument(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Tombstones["later:github:acme/two"]; !got.Equal(minutes(3)) {
		t.Errorf("removal of the saved repository = %v, want it recorded", got)
	}
}

func TestAnItemArrivesOnAnotherDeviceWithWhatTheUserChoseAndNoInstall(t *testing.T) {
	pc, mac, _, _ := trio(t)
	group, err := pc.catalog.CreateGroup(t.Context(), "Games")
	if err != nil {
		t.Fatal(err)
	}
	e := pc.at(1).add("one", catalog.AddOptions{GroupID: group.ID})
	id := e.Item.ID
	for _, set := range []error{
		pc.at(2).catalog.SetNote(t.Context(), id, "my note"),
		pc.catalog.SetTags(t.Context(), id, []string{"emu", "fast"}),
		pc.catalog.SetFavorite(t.Context(), id, true),
		pc.catalog.SetHidden(t.Context(), id, true),
		pc.catalog.SetIncludePrereleases(t.Context(), id, true),
		pc.catalog.SetHoldUpdates(t.Context(), id, true),
		pc.catalog.SetAutoUpdate(t.Context(), id, true),
		pc.catalog.SetTrustUnsigned(t.Context(), id, true),
		pc.catalog.SetSkippedVersion(t.Context(), id, "v0.9"),
		pc.catalog.SetInstallerArgs(t.Context(), id, "/VERYSILENT"),
		pc.catalog.SetKind(t.Context(), id, selection.ItemTool),
		pc.catalog.SetIntent(t.Context(), id, &selection.Intent{
			Product: "tool", Kind: selection.ArtifactArchive, Arch: "x64", ArchiveMembers: []string{"bin/tool.exe"}, Explicit: true,
		}),
		// What only this PC has: where it is installed.
		pc.catalog.MarkInstalled(t.Context(), id, "v1.0.0"),
		pc.catalog.SetInstallFolder(t.Context(), id, `C:\Apps\one`),
	} {
		if set != nil {
			t.Fatal(set)
		}
	}
	pc.sync(3)

	got := mac.sync(4)
	if len(got.Applied.Added) != 1 {
		t.Fatalf("applied = %+v, want one new Item", got.Applied)
	}
	arrived := mac.entry("one")
	item := arrived.Item
	if item.Note != "my note" || !slices.Equal(item.Tags, []string{"emu", "fast"}) || !item.Favorite || !item.Hidden ||
		!item.IncludePrereleases || !item.HoldUpdates || !item.AutoUpdate || !item.TrustUnsigned || item.SkippedVersion != "v0.9" ||
		item.InstallerArgs != "/VERYSILENT" || item.Kind != selection.ItemTool || item.Name != "one" || item.Owner != "acme" {
		t.Errorf("the Item that arrived = %+v", item)
	}
	if arrived.Intent == nil || !arrived.Intent.Explicit || arrived.Intent.Product != "tool" || arrived.Intent.Arch != "x64" ||
		!slices.Equal(arrived.Intent.ArchiveMembers, []string{"bin/tool.exe"}) {
		t.Errorf("the chosen file that arrived = %+v", arrived.Intent)
	}
	if names := mac.groupNames(); !slices.Equal(names, []string{"Games"}) {
		t.Fatalf("groups on the other device = %v", names)
	}
	if arrived.Item.GroupID == 0 || mac.catalog.Groups().Get()[0].ID != arrived.Item.GroupID {
		t.Errorf("the Item is not in the group that arrived: %+v", arrived.Item)
	}
	if arrived.Install != nil || arrived.Item.InstallFolder != "" || len(arrived.Releases) != 0 || arrived.Item.MetadataFetched {
		t.Errorf("what is installed on one device reached another, or the Item came with releases: %+v", arrived)
	}
	if !arrived.Item.AddedAt.Equal(pc.entry("one").Item.AddedAt) || !arrived.Item.ModifiedAt.Equal(pc.entry("one").Item.ModifiedAt) {
		t.Errorf("times on the other device = %v / %v, want the originals", arrived.Item.AddedAt, arrived.Item.ModifiedAt)
	}
	// Nothing was lost on the way, so the PC has nothing new to learn from the Mac.
	if pc.document() != mac.document() {
		t.Errorf("the Item changed on the way:\n%s\n%s", pc.document(), mac.document())
	}
	if pc.sync(5).Pushed || mac.sync(6).Pushed {
		t.Error("devices that agree rewrote the shared file")
	}
}

func TestAChoiceThisVersionDoesNotKnowIsLeftToBeDetected(t *testing.T) {
	cloud := &libsync.Memory{}
	d := newSyncDevice(t, "pc", cloud)
	doc := libsync.Document{Format: libsync.Format, Version: libsync.Version, Items: []libsync.Item{{
		Source: "github", Owner: "acme", Repo: "one", Kind: "spaceship", Method: "teleport", AddedAt: minutes(1), ModifiedAt: minutes(1),
	}}}
	text, err := doc.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := cloud.Write(t.Context(), text); err != nil {
		t.Fatal(err)
	}
	d.sync(2)
	if e := d.entry("one"); e.Item.Kind != "" || e.Item.Method != selection.MethodAutomatic {
		t.Errorf("kind %q and method %q arrived as they were written", e.Item.Kind, e.Item.Method)
	}
}

func TestSyncedDevicesAgreeAndASecondRoundChangesNothing(t *testing.T) {
	pc, mac, _, cloud := trio(t)
	group, _ := pc.catalog.CreateGroup(t.Context(), "Games")
	one := pc.at(1).add("one", catalog.AddOptions{GroupID: group.ID})
	if err := pc.at(2).catalog.SetNote(t.Context(), one.Item.ID, "note"); err != nil {
		t.Fatal(err)
	}
	pc.at(3).track("two")
	if _, err := pc.catalog.SaveForLater(t.Context(), source.Repository{Owner: "acme", Name: "three", Description: "d", Stars: 5, Topics: []string{"x"}}, "later"); err != nil {
		t.Fatal(err)
	}
	pc.sync(4)
	mac.sync(5)
	pc.sync(6)

	if pc.document() != mac.document() {
		t.Fatalf("the devices disagree:\n%s\n%s", pc.document(), mac.document())
	}
	_, writes := cloud.Counts()
	if got := mac.sync(7); got.Pushed || got.Applied.Updated+got.Applied.Removed+len(got.Applied.Added) != 0 {
		t.Errorf("a round with nothing new = %+v", got)
	}
	if got := pc.sync(8); got.Pushed {
		t.Error("a round with nothing new rewrote the shared file")
	}
	if _, after := cloud.Counts(); after != writes {
		t.Errorf("the file was written %d more times by rounds with nothing new", after-writes)
	}
	saved := mac.catalog.TryLaterEntries().Get()
	if len(saved) != 1 || saved[0].Description != "d" || saved[0].Stars != 5 || saved[0].Note != "later" || !slices.Equal(saved[0].Topics, []string{"x"}) {
		t.Errorf("the saved repository on the other device = %+v", saved)
	}
}

func TestADeletionReachesAStaleDeviceAndInstalledFilesAreNotTouched(t *testing.T) {
	pc, mac, thor, _ := trio(t)
	pc.at(1).track("one")
	if err := pc.catalog.MarkInstalled(t.Context(), pc.entry("one").Item.ID, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	pc.sync(2)
	mac.sync(3)
	thor.sync(4)
	if err := thor.catalog.MarkInstalled(t.Context(), thor.entry("one").Item.ID, "v1.0.0"); err != nil {
		t.Fatal(err)
	}

	mac.at(10)
	if err := mac.catalog.Remove(t.Context(), mac.entry("one").Item.ID); err != nil {
		t.Fatal(err)
	}
	mac.sync(11)
	pc.sync(12)
	sameList(t, "pc after the deletion", pc.tracked(), []string{})

	thor.sync(1000)
	sameList(t, "the stale device after catching up", thor.tracked(), []string{})
	pc.sync(1001)
	sameList(t, "pc after the stale device synced", pc.tracked(), []string{})
}

func TestAnItemAddedAgainAfterItsDeletionComesBack(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.at(1).track("one")
	pc.sync(2)
	mac.sync(3)
	mac.at(10)
	if err := mac.catalog.Remove(t.Context(), mac.entry("one").Item.ID); err != nil {
		t.Fatal(err)
	}
	mac.sync(11)
	pc.sync(12)

	pc.at(20).track("one")
	pc.sync(21)
	mac.sync(22)
	sameList(t, "mac", mac.tracked(), []string{"one"})
}

func TestAnEditMadeAfterAnotherDevicesDeletionKeepsTheItem(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.at(1).track("one")
	pc.sync(2)
	mac.sync(3)
	mac.at(10)
	if err := mac.catalog.Remove(t.Context(), mac.entry("one").Item.ID); err != nil {
		t.Fatal(err)
	}
	mac.sync(11)

	if err := pc.at(30).catalog.SetNote(t.Context(), pc.entry("one").Item.ID, "still wanted"); err != nil {
		t.Fatal(err)
	}
	pc.sync(31)
	mac.sync(32)
	if !mac.tracks("one") || mac.entry("one").Item.Note != "still wanted" {
		t.Errorf("mac tracks %v, want the Item back with the note", mac.tracked())
	}
}

func TestTheNewestEditWinsWhicheverDeviceSyncsFirst(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.at(1).track("one")
	pc.sync(2)
	mac.sync(3)
	if err := pc.at(10).catalog.SetNote(t.Context(), pc.entry("one").Item.ID, "from the PC"); err != nil {
		t.Fatal(err)
	}
	if err := mac.at(20).catalog.SetNote(t.Context(), mac.entry("one").Item.ID, "from the Mac"); err != nil {
		t.Fatal(err)
	}
	pc.sync(21)
	mac.sync(22)
	pc.sync(23)
	if got := pc.entry("one").Item.Note; got != "from the Mac" {
		t.Errorf("pc note = %q", got)
	}
	if got := mac.entry("one").Item.Note; got != "from the Mac" {
		t.Errorf("mac note = %q", got)
	}
}

func TestWhatTheUserDoesWhileASyncIsUnderWayIsKept(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.at(1).track("one")
	pc.at(1).track("two")
	pc.sync(2)
	mac.sync(3)
	mac.at(4)
	if err := mac.catalog.Remove(t.Context(), mac.entry("one").Item.ID); err != nil {
		t.Fatal(err)
	}
	if err := mac.catalog.SetNote(t.Context(), mac.entry("two").Item.ID, "from the Mac"); err != nil {
		t.Fatal(err)
	}
	mac.sync(5)

	pc.during = func() {
		pc.at(6).track("three")
		if err := pc.catalog.SetNote(t.Context(), pc.entry("one").Item.ID, "edited during"); err != nil {
			t.Error(err)
		}
		if err := pc.catalog.SetNote(t.Context(), pc.entry("two").Item.ID, "also during"); err != nil {
			t.Error(err)
		}
	}
	pc.sync(7)
	sameList(t, "pc", pc.tracked(), []string{"one", "three", "two"})
	if got := pc.entry("two").Item.Note; got != "also during" {
		t.Errorf("the note typed during the sync = %q, want it kept over the Mac's", got)
	}

	pc.sync(8)
	mac.sync(9)
	sameList(t, "mac after the next round", mac.tracked(), []string{"one", "three", "two"})
	if got := mac.entry("two").Item.Note; got != "also during" {
		t.Errorf("mac's note = %q", got)
	}
}

func TestAChosenFileIsSharedButAMatchTheAppMadeIsNot(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.at(1).track("one")
	mac.at(1).track("one")
	// The PC chose a file; the Mac has only what the best match gave it.
	if err := pc.at(2).catalog.SetIntent(t.Context(), pc.entry("one").Item.ID, &selection.Intent{Product: "chosen", Explicit: true}); err != nil {
		t.Fatal(err)
	}
	if err := mac.catalog.SetIntent(t.Context(), mac.entry("one").Item.ID, &selection.Intent{Product: "automatic"}); err != nil {
		t.Fatal(err)
	}
	mac.at(1)
	pc.sync(3)
	mac.sync(4)
	if got := mac.entry("one").Intent; got == nil || got.Product != "chosen" {
		t.Errorf("mac's file = %+v, want the one the PC chose", got)
	}

	// An Item changed on the PC without a chosen file leaves the Mac's own match alone.
	pc2, mac2, _, _ := trio(t)
	pc2.at(1).track("two")
	mac2.at(1).track("two")
	if err := mac2.catalog.SetIntent(t.Context(), mac2.entry("two").Item.ID, &selection.Intent{Product: "automatic"}); err != nil {
		t.Fatal(err)
	}
	mac2.at(1)
	if err := pc2.at(5).catalog.SetNote(t.Context(), pc2.entry("two").Item.ID, "newer"); err != nil {
		t.Fatal(err)
	}
	pc2.sync(6)
	mac2.sync(7)
	if got := mac2.entry("two"); got.Item.Note != "newer" || got.Intent == nil || got.Intent.Product != "automatic" {
		t.Errorf("mac's Item = %+v, intent %+v: its own best match should have been kept", got.Item, got.Intent)
	}
}

func TestGroupsAreSharedByNameAndRenamingOneCarriesItsItems(t *testing.T) {
	pc, mac, _, _ := trio(t)
	group, err := pc.at(1).catalog.CreateGroup(t.Context(), "Emulators")
	if err != nil {
		t.Fatal(err)
	}
	pc.at(1).add("one", catalog.AddOptions{GroupID: group.ID})
	pc.sync(2)
	mac.sync(3)
	sameList(t, "mac groups", mac.groupNames(), []string{"Emulators"})

	if err := pc.at(10).catalog.RenameGroup(t.Context(), group.ID, "Retro"); err != nil {
		t.Fatal(err)
	}
	pc.sync(11)
	mac.sync(12)
	sameList(t, "mac groups after the rename", mac.groupNames(), []string{"Retro"})
	if got := mac.entry("one").Item.GroupID; got == 0 || mac.catalog.Groups().Get()[0].ID != got {
		t.Errorf("the Item on the Mac is in group %d, want the renamed group", got)
	}
}

func TestADeletedGroupStaysDeletedButOneMadeAgainSurvives(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.at(0)
	if _, err := pc.catalog.CreateGroup(t.Context(), "Emulators"); err != nil {
		t.Fatal(err)
	}
	tools, err := pc.catalog.CreateGroup(t.Context(), "Tools")
	if err != nil {
		t.Fatal(err)
	}
	pc.sync(1)
	mac.sync(2)

	mac.at(10)
	macTools := mac.catalog.Groups().Get()[1]
	if err := mac.catalog.DeleteGroup(t.Context(), macTools.ID); err != nil {
		t.Fatal(err)
	}
	mac.sync(11)
	pc.sync(12)
	sameList(t, "pc groups", pc.groupNames(), []string{"Emulators"})
	_ = tools

	if _, err := pc.at(20).catalog.CreateGroup(t.Context(), "tools"); err != nil {
		t.Fatal(err)
	}
	pc.sync(21)
	mac.sync(22)
	sameList(t, "mac groups", mac.groupNames(), []string{"Emulators", "tools"})
}

func TestDeletingAGroupMovesItsItemsBackOnTheOtherDevicesToo(t *testing.T) {
	pc, mac, _, _ := trio(t)
	group, _ := pc.at(1).catalog.CreateGroup(t.Context(), "Games")
	pc.at(1).add("one", catalog.AddOptions{GroupID: group.ID})
	pc.sync(2)
	mac.sync(3)
	if err := pc.at(10).catalog.DeleteGroup(t.Context(), group.ID); err != nil {
		t.Fatal(err)
	}
	pc.sync(11)
	mac.sync(12)
	if got := mac.entry("one").Item.GroupID; got != 0 || len(mac.groupNames()) != 0 {
		t.Errorf("after the deletion the Item is in group %d and the groups are %v", got, mac.groupNames())
	}
}

func TestSavedRepositoriesSyncTheirNotesAndRemovals(t *testing.T) {
	pc, _, thor, _ := trio(t)
	for _, name := range []string{"one", "two"} {
		if _, err := pc.at(0).catalog.SaveForLater(t.Context(), source.Repository{Owner: "acme", Name: name}, ""); err != nil {
			t.Fatal(err)
		}
	}
	pc.sync(1)
	thor.sync(2)
	if got := len(thor.catalog.TryLaterEntries().Get()); got != 2 {
		t.Fatalf("thor has %d saved repositories, want 2", got)
	}

	one, _ := thor.catalog.FindTryLater(source.RepoRef{Owner: "acme", Repo: "one"})
	if err := thor.at(10).catalog.RemoveTryLater(t.Context(), one.ID); err != nil {
		t.Fatal(err)
	}
	two, _ := pc.catalog.FindTryLater(source.RepoRef{Owner: "acme", Repo: "two"})
	if err := pc.at(11).catalog.SetTryLaterNote(t.Context(), two.ID, "check the Android build"); err != nil {
		t.Fatal(err)
	}
	thor.sync(12)
	pc.sync(13)
	thor.sync(14)

	left := pc.catalog.TryLaterEntries().Get()
	if len(left) != 1 || left[0].Repo != "two" {
		t.Fatalf("pc saved = %+v, want only two", left)
	}
	if got := thor.catalog.TryLaterEntries().Get()[0].Note; got != "check the Android build" {
		t.Errorf("thor's note = %q", got)
	}
}

func TestSyncingReportsTheNewItemsSoTheyCanBeChecked(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.at(1).track("one")
	pc.sync(2)
	got := mac.sync(3)
	if len(got.Applied.Added) != 1 || got.Applied.Added[0] != mac.entry("one").Item.ID {
		t.Fatalf("added = %v, want the id of the new Item", got.Applied.Added)
	}
	// A check fills in the releases the Item arrived without.
	if res := mac.check(got.Applied.Added[0], false); res.Failure != nil {
		t.Fatal(res.Failure)
	}
	if e := mac.entry("one"); len(e.Releases) == 0 || !e.Item.MetadataFetched {
		t.Errorf("after the check: %+v", e)
	}
	// The check changes nothing worth sharing.
	if mac.sync(4).Pushed {
		t.Error("checking a new Item made the next sync rewrite the shared file")
	}
}

func TestAnUnreadableSharedFileIsLeftAloneAndNothingChangesHere(t *testing.T) {
	cloud := &libsync.Memory{}
	if err := cloud.Write(t.Context(), "{ not json"); err != nil {
		t.Fatal(err)
	}
	d := newSyncDevice(t, "pc", cloud)
	d.at(1).track("one")
	_, err := d.catalog.Sync(t.Context(), cloud, libsync.Keys{}, d.self)
	if err == nil {
		t.Fatal("syncing with an unreadable file worked")
	}
	if text, _ := cloud.Text(); text != "{ not json" {
		t.Errorf("the file was overwritten with %q", text)
	}
	sameList(t, "tracked", d.tracked(), []string{"one"})
}

func TestARemovalSurvivesARestartUntilItIsNinetyDaysOld(t *testing.T) {
	pc, _, _, _ := trio(t)
	// The clock is the real one: a removal this old is forgotten as the Library opens.
	pc.now = time.Now()
	pc.track("one")
	one := pc.entry("one")
	if err := pc.catalog.Remove(t.Context(), one.Item.ID); err != nil {
		t.Fatal(err)
	}
	pc.reopen()
	doc, err := pc.catalog.SyncDocument(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Tombstones["item:github:acme/one"]; !ok {
		t.Errorf("the removal was lost on a restart: %v", doc.Tombstones)
	}

	old := newSyncDevice(t, "old", &libsync.Memory{})
	old.now = time.Now().Add(-100 * 24 * time.Hour)
	old.track("two")
	if err := old.catalog.Remove(t.Context(), old.entry("two").Item.ID); err != nil {
		t.Fatal(err)
	}
	old.reopen()
	if doc, err := old.catalog.SyncDocument(t.Context()); err != nil || len(doc.Tombstones) != 0 {
		t.Errorf("a removal 100 days old after a restart = %v, %v, want it forgotten", doc.Tombstones, err)
	}
}

func TestRandomEditsOnThreeLibrariesConverge(t *testing.T) {
	for seed := range uint64(6) {
		r := rand.New(rand.NewPCG(seed, 3)) //nolint:gosec // Seeded, so a failing case repeats.
		pc, mac, thor, _ := trio(t)
		devices := []*syncDevice{pc, mac, thor}
		clock := 1.0
		for range 24 {
			clock += float64(r.IntN(3))
			d := devices[r.IntN(len(devices))].at(clock)
			randomEdit(t, r, d)
			if r.IntN(3) == 0 {
				devices[r.IntN(len(devices))].sync(clock)
			}
		}
		for range 2 {
			for _, d := range devices {
				clock++
				d.sync(clock)
			}
		}
		if pc.document() != mac.document() || pc.document() != thor.document() {
			t.Fatalf("seed %d: the libraries did not converge:\n%s\n%s\n%s", seed, pc.document(), mac.document(), thor.document())
		}
	}
}

// randomEdit makes one random change to a device's Library.
func randomEdit(t *testing.T, r *rand.Rand, d *syncDevice) {
	t.Helper()
	name := []string{"one", "two", "three", "four"}[r.IntN(4)]
	ctx := t.Context()
	switch r.IntN(6) {
	case 0:
		if !d.tracks(name) {
			d.track(name)
		}
	case 1:
		if d.tracks(name) {
			if err := d.catalog.SetNote(ctx, d.entry(name).Item.ID, fmt.Sprint("note ", r.IntN(3))); err != nil {
				t.Fatal(err)
			}
		}
	case 2:
		if d.tracks(name) {
			if err := d.catalog.Remove(ctx, d.entry(name).Item.ID); err != nil {
				t.Fatal(err)
			}
		}
	case 3:
		if _, ok := d.catalog.FindTryLater(source.RepoRef{Owner: "acme", Repo: name}); !ok && !d.tracks(name) {
			if _, err := d.catalog.SaveForLater(ctx, source.Repository{Owner: "acme", Name: name}, ""); err != nil {
				t.Fatal(err)
			}
		}
	case 4:
		if saved, ok := d.catalog.FindTryLater(source.RepoRef{Owner: "acme", Repo: name}); ok {
			if err := d.catalog.RemoveTryLater(ctx, saved.ID); err != nil {
				t.Fatal(err)
			}
		}
	case 5:
		group := []string{"Games", "Tools"}[r.IntN(2)]
		for _, g := range d.catalog.Groups().Get() {
			if strings.EqualFold(g.Name, group) {
				if err := d.catalog.DeleteGroup(ctx, g.ID); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		if _, err := d.catalog.CreateGroup(ctx, group); err != nil {
			t.Fatal(err)
		}
	}
}

func TestARemovalMadeWhileASyncWasUnderWayIsNotUndone(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.at(1).track("one")
	pc.sync(2)
	mac.sync(3)
	if err := mac.at(4).catalog.SetNote(t.Context(), mac.entry("one").Item.ID, "edited on the Mac"); err != nil {
		t.Fatal(err)
	}
	mac.sync(5)

	pc.during = func() {
		if err := pc.at(6).catalog.Remove(t.Context(), pc.entry("one").Item.ID); err != nil {
			t.Error(err)
		}
	}
	pc.sync(7)
	sameList(t, "pc", pc.tracked(), []string{})

	pc.sync(8)
	mac.sync(9)
	sameList(t, "mac after the next round", mac.tracked(), []string{})
}
