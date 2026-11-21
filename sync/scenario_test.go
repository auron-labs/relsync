package sync

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func trio(t *testing.T) (pc, mac, thor *device, cloud *Memory) {
	t.Helper()
	cloud = &Memory{}
	return newDevice(t, "pc", "Windows", cloud), newDevice(t, "mac", "macOS", cloud), newDevice(t, "thor", "Linux", cloud), cloud
}

func TestTheFirstSyncCreatesTheFileAndASecondDeviceReceivesIt(t *testing.T) {
	pc, mac, _, cloud := trio(t)
	pc.add("neovim", "neovim", at(0))
	first := pc.mustSync(at(1))
	if !first.FirstSync || !first.Pushed {
		t.Fatalf("first sync = %+v, want a first sync that wrote the file", first)
	}
	if _, found := cloud.Text(); !found {
		t.Fatal("the first sync wrote nothing")
	}

	got := mac.mustSync(at(2))
	if !got.Changed {
		t.Error("the second device was not told it received something")
	}
	equalStrings(t, "items on the second device", mac.itemKeys(), []string{"neovim/neovim"})
}

func TestItemsAddedOnThreeDevicesMeetInTheMiddle(t *testing.T) {
	pc, mac, thor, _ := trio(t)
	pc.add("a", "one", at(0))
	mac.add("b", "two", at(0))
	thor.add("c", "three", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	thor.mustSync(at(3))
	pc.mustSync(at(4))
	mac.mustSync(at(5))

	want := []string{"a/one", "b/two", "c/three"}
	for _, d := range []*device{pc, mac, thor} {
		equalStrings(t, d.self.ID+" items", d.itemKeys(), want)
	}
}

func TestTheNewestEditWinsWhicheverDeviceSyncsFirst(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	pc.edit("a", "one", at(10), "from the PC")
	mac.edit("a", "one", at(20), "from the Mac")

	pc.mustSync(at(21))
	mac.mustSync(at(22))
	pc.mustSync(at(23))
	for _, d := range []*device{pc, mac} {
		if i, _ := d.item("a", "one"); i.Note != "from the Mac" {
			t.Errorf("%s has the note %q, want the later edit", d.self.ID, i.Note)
		}
	}
}

func TestADeletionReachesADeviceThatWasOfflineAndItDoesNotBringTheItemBack(t *testing.T) {
	pc, mac, thor, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	thor.mustSync(at(3))

	mac.remove("a", "one", at(10))
	mac.mustSync(at(11))
	pc.mustSync(at(12))
	equalStrings(t, "pc items", pc.itemKeys(), []string{})
	equalStrings(t, "the offline device still has its copy", thor.itemKeys(), []string{"a/one"})

	thor.mustSync(at(1000))
	equalStrings(t, "thor items after catching up", thor.itemKeys(), []string{})
	pc.mustSync(at(1001))
	equalStrings(t, "pc items after thor synced", pc.itemKeys(), []string{})
}

func TestAddingAnItemAgainAfterItWasDeletedKeepsIt(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	mac.remove("a", "one", at(10))
	mac.mustSync(at(11))
	pc.mustSync(at(12))
	equalStrings(t, "pc items after the deletion", pc.itemKeys(), []string{})

	pc.add("a", "one", at(20))
	pc.mustSync(at(21))
	mac.mustSync(at(22))
	equalStrings(t, "pc items", pc.itemKeys(), []string{"a/one"})
	equalStrings(t, "mac items", mac.itemKeys(), []string{"a/one"})
}

func TestAnItemTheDeletingDeviceAddsAgainComesBackEverywhere(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	mac.remove("a", "one", at(10))
	mac.mustSync(at(11))
	mac.add("a", "one", at(20))
	mac.mustSync(at(21))
	pc.mustSync(at(22))
	equalStrings(t, "pc items", pc.itemKeys(), []string{"a/one"})
}

func TestAnEditMadeAfterAnotherDeviceDeletedTheItemKeepsIt(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	mac.remove("a", "one", at(10))
	mac.mustSync(at(11))

	pc.edit("a", "one", at(30), "still wanted")
	pc.mustSync(at(31))
	mac.mustSync(at(32))
	if i, ok := mac.item("a", "one"); !ok || i.Note != "still wanted" {
		t.Errorf("mac has %+v, %v: the later edit should have kept the item", i, ok)
	}
}

func TestAnEditMadeBeforeAnotherDeviceDeletedTheItemLosesToTheDeletion(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	pc.edit("a", "one", at(5), "edited early")
	mac.remove("a", "one", at(10))
	mac.mustSync(at(11))

	pc.mustSync(at(12))
	equalStrings(t, "pc items", pc.itemKeys(), []string{})
}

func TestSavedRepositoriesAndTheirNotesSync(t *testing.T) {
	pc, _, thor, _ := trio(t)
	pc.save("ryujinx", "ryujinx", at(0))
	pc.save("dolphin-emu", "dolphin", at(0))
	pc.mustSync(at(1))
	thor.mustSync(at(2))
	if got := len(thor.lib.TryLater); got != 2 {
		t.Fatalf("thor has %d saved repositories, want 2", got)
	}

	thor.unsave("ryujinx", "ryujinx", at(10))
	pc.note("dolphin-emu", "dolphin", at(11), "check the Android build")
	thor.mustSync(at(12))
	pc.mustSync(at(13))
	thor.mustSync(at(14))
	equalStrings(t, "pc saved repositories", pc.laterKeys(), []string{"dolphin-emu/dolphin"})
	if got := thor.lib.TryLater[0].Note; got != "check the Android build" {
		t.Errorf("thor's note = %q", got)
	}
}

func TestSavingAgainAfterRemovingKeepsTheRepository(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.save("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	mac.unsave("a", "one", at(5))
	mac.mustSync(at(6))
	mac.save("a", "one", at(10))
	mac.mustSync(at(11))
	pc.mustSync(at(12))
	equalStrings(t, "pc saved repositories", pc.laterKeys(), []string{"a/one"})
}

func TestADeletedGroupStaysDeletedButOneMadeAgainSurvives(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.addGroup("Emulators", at(0))
	pc.addGroup("Tools", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	mac.removeGroup("Tools", at(10))
	mac.mustSync(at(11))
	pc.mustSync(at(12))
	equalStrings(t, "pc groups", pc.groupNames(), []string{"Emulators"})

	pc.addGroup("tools", at(20))
	pc.mustSync(at(21))
	mac.mustSync(at(22))
	equalStrings(t, "mac groups", mac.groupNames(), []string{"Emulators", "tools"})
}

func TestGroupsAreTheSameGroupIgnoringCase(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.addGroup("Games", at(0))
	mac.addGroup("games", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	pc.mustSync(at(3))
	if len(pc.lib.Groups) != 1 || len(mac.lib.Groups) != 1 || pc.lib.Groups[0].Name != mac.lib.Groups[0].Name {
		t.Errorf("groups: pc %v, mac %v, want one group both agree on", pc.groupNames(), mac.groupNames())
	}
}

func TestEveryDeviceAppearsInTheDeviceList(t *testing.T) {
	pc, mac, thor, _ := trio(t)
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	thor.mustSync(at(3))
	pc.mustSync(at(4))
	names := make([]string, 0, len(pc.devices))
	for _, d := range pc.devices {
		names = append(names, d.Name+"/"+d.Platform)
	}
	slices.Sort(names)
	equalStrings(t, "devices", names, []string{"mac/macOS", "pc/Windows", "thor/Linux"})
	if got := pc.devices["mac"].LastSeen; !got.Equal(at(2)) {
		t.Errorf("the Mac was last seen at %v, want the time of its sync", got.Sub(t0))
	}
}

func TestNothingIsRewrittenWhenNothingChangedExceptTheOccasionalCheckIn(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	pc.mustSync(at(3))
	writes := pc.writes()

	if quiet := pc.mustSync(at(10)); quiet.Pushed {
		t.Error("a sync with nothing new rewrote the file")
	}
	mac.mustSync(at(11))
	if got := pc.writes(); got != writes {
		t.Errorf("the file was written %d times by syncs with nothing new", got-writes)
	}
	if late := pc.mustSync(at(60 * 7)); !late.Pushed {
		t.Error("the PC did not check in after more than six hours")
	}
}

func TestACheckInIsDueOnlyAfterSixHours(t *testing.T) {
	pc, _, _, _ := trio(t)
	pc.mustSync(at(0))
	if pc.mustSync(at(60*6 - 1)).Pushed {
		t.Error("checked in before six hours were up")
	}
	if !pc.mustSync(at(60*6 + 1)).Pushed {
		t.Error("did not check in after six hours")
	}
}

func TestAnUnreadableFileIsNeverOverwritten(t *testing.T) {
	for name, text := range map[string]string{
		"not JSON":             "{ this is not json",
		"JSON of another kind": `{"hello": "world"}`,
		"a list":               `[1, 2, 3]`,
	} {
		t.Run(name, func(t *testing.T) {
			pc, _, _, cloud := trio(t)
			if err := cloud.Write(context.Background(), text); err != nil {
				t.Fatal(err)
			}
			pc.add("a", "one", at(0))

			_, err := pc.sync(at(1))
			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("error = %v, want ErrUnreadable", err)
			}
			if got, _ := cloud.Text(); got != text {
				t.Errorf("the file was overwritten with %q", got)
			}
		})
	}
}

func TestAFileFromANewerFormatIsNeverOverwritten(t *testing.T) {
	pc, _, _, cloud := trio(t)
	newer := `{"format": "` + Format + `", "version": 99, "items": []}`
	if err := cloud.Write(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.sync(at(1)); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("error = %v, want ErrNewerFormat", err)
	}
	if got, _ := cloud.Text(); got != newer {
		t.Errorf("the file was overwritten with %q", got)
	}
}

func TestAnEmptyFileIsTreatedAsNoFileYet(t *testing.T) {
	pc, _, _, cloud := trio(t)
	if err := cloud.Write(context.Background(), "  \n"); err != nil {
		t.Fatal(err)
	}
	if first := pc.mustSync(at(1)); !first.FirstSync || !first.Pushed {
		t.Errorf("result = %+v, want a first sync", first)
	}
}

func TestRemovalsAreForgottenAfterNinetyDays(t *testing.T) {
	pc, _, _, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.remove("a", "one", at(1))
	pc.mustSync(at(2))
	if len(pc.lib.Tombstones) != 1 {
		t.Fatalf("tombstones after the first sync = %v, want the removal", pc.lib.Tombstones)
	}

	pc.mustSync(at(60 * 24 * 91))
	if len(pc.lib.Tombstones) != 0 {
		t.Errorf("tombstones after 91 days = %v, want none", pc.lib.Tombstones)
	}
}

func TestWhatTheUserDidWhileASyncWasUnderWayIsKept(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.add("keep", "edited", at(0))
	pc.add("gone", "elsewhere", at(0))
	pc.add("gone", "meanwhile", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	mac.remove("gone", "elsewhere", at(3))
	mac.remove("gone", "meanwhile", at(3))
	mac.mustSync(at(4))

	// While the PC syncs, the user adds an Item, edits one the sync would
	// leave alone, and edits one the Mac removed.
	pc.during = func() {
		pc.add("added", "during", at(5))
		pc.edit("keep", "edited", at(5), "edited during")
		pc.edit("gone", "meanwhile", at(5), "edited during")
	}
	pc.mustSync(at(6))

	equalStrings(t, "pc items", pc.itemKeys(), []string{"added/during", "gone/meanwhile", "keep/edited"})
	if i, _ := pc.item("keep", "edited"); i.Note != "edited during" {
		t.Errorf("the edit made during the sync was replaced: %q", i.Note)
	}

	// The next round shares what was kept.
	pc.mustSync(at(7))
	mac.mustSync(at(8))
	equalStrings(t, "mac items", mac.itemKeys(), []string{"added/during", "gone/meanwhile", "keep/edited"})
}

func TestARemovalMadeWhileASyncWasUnderWayIsNotUndone(t *testing.T) {
	pc, mac, _, _ := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	mac.edit("a", "one", at(3), "edited on the Mac")
	mac.mustSync(at(4))

	pc.during = func() { pc.remove("a", "one", at(5)) }
	pc.mustSync(at(6))
	equalStrings(t, "pc items", pc.itemKeys(), []string{})

	pc.mustSync(at(7))
	mac.mustSync(at(8))
	equalStrings(t, "mac items", mac.itemKeys(), []string{})
}

func TestEncryptedSyncingBetweenDevices(t *testing.T) {
	pc, mac, thor, cloud := trio(t)
	pc.keys = Keys{Passphrase: "correct horse", Encrypt: true}
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))

	text, _ := cloud.Text()
	if !IsEncrypted(text) || strings.Contains(text, "one") {
		t.Fatalf("the shared file is not encrypted: %s", text)
	}

	if _, err := mac.sync(at(2)); !errors.Is(err, ErrLocked) {
		t.Errorf("a device with no passphrase got %v, want ErrLocked", err)
	}
	thor.keys = Keys{Passphrase: "not the passphrase", Encrypt: true}
	if _, err := thor.sync(at(3)); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("a device with the wrong passphrase got %v, want ErrWrongPassphrase", err)
	}
	if after, _ := cloud.Text(); after != text {
		t.Error("the file was overwritten by a device that could not open it")
	}

	mac.keys = Keys{Passphrase: "correct horse", Encrypt: true}
	mac.mustSync(at(4))
	equalStrings(t, "mac items", mac.itemKeys(), []string{"a/one"})
}

func TestTurningEncryptionOnAndOffRewritesTheFile(t *testing.T) {
	pc, _, _, cloud := trio(t)
	pc.add("a", "one", at(0))
	pc.mustSync(at(1))
	if text, _ := cloud.Text(); IsEncrypted(text) {
		t.Fatal("the first file was encrypted without a passphrase")
	}

	pc.keys = Keys{Passphrase: "correct horse", Encrypt: true}
	if result := pc.mustSync(at(2)); !result.Pushed {
		t.Error("turning encryption on did not rewrite the file")
	}
	if text, _ := cloud.Text(); !IsEncrypted(text) {
		t.Error("the file is not encrypted after turning encryption on")
	}

	// Turning it off needs the passphrase once, to read the file.
	pc.keys = Keys{Passphrase: "correct horse"}
	if result := pc.mustSync(at(3)); !result.Pushed {
		t.Error("turning encryption off did not rewrite the file")
	}
	if text, _ := cloud.Text(); IsEncrypted(text) || !strings.Contains(text, `"repo": "one"`) {
		t.Errorf("the file is still encrypted, or lost its content: %s", text)
	}
}

func TestAPassphraseThatIsTooShortIsRefused(t *testing.T) {
	pc, _, _, cloud := trio(t)
	pc.keys = Keys{Passphrase: "short", Encrypt: true}
	if _, err := pc.sync(at(1)); !errors.Is(err, ErrPassphraseShort) {
		t.Fatalf("error = %v, want ErrPassphraseShort", err)
	}
	if _, found := cloud.Text(); found {
		t.Error("a file was written with a passphrase that is too short")
	}
}

func TestTheTransportsOwnFailuresAreReportedAndNothingIsApplied(t *testing.T) {
	pc, _, _, _ := trio(t)
	pc.cloud = failingTransport{err: ErrUnreachable}
	pc.add("a", "one", at(0))
	if _, err := pc.sync(at(1)); !errors.Is(err, ErrUnreachable) {
		t.Errorf("error = %v, want the transport's", err)
	}
	equalStrings(t, "pc items", pc.itemKeys(), []string{"a/one"})
}

type failingTransport struct{ err error }

func (f failingTransport) Read(context.Context) (string, bool, error) { return "", false, f.err }
func (f failingTransport) Write(context.Context, string) error        { return f.err }

func TestADeviceKeepsItsOwnNameAndAppVersionInTheList(t *testing.T) {
	pc, _, _, _ := trio(t)
	pc.mustSync(at(1))
	pc.self.Name, pc.self.AppVersion = "Desk", "2.0"
	if !pc.mustSync(at(2)).Pushed {
		t.Fatal("renaming the device did not rewrite the file")
	}
	if got := pc.devices["pc"]; got.Name != "Desk" || got.AppVersion != "2.0" {
		t.Errorf("device = %+v", got)
	}
}

func TestTimesFromADocumentWrittenInAnotherZoneAreTheSameMoments(t *testing.T) {
	zone := time.FixedZone("far", 13*3600)
	a := Document{Items: []Item{repoItem("o", "r", t0.In(zone))}}
	b := Document{Items: []Item{repoItem("o", "r", t0)}}
	if a.Signature() != b.Signature() {
		t.Error("the same moment in two zones makes two different documents")
	}
}
