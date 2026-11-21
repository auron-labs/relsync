package catalog_test

import (
	"errors"
	"testing"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"
)

func discovered() source.Repository {
	return source.Repository{
		Owner: "Acme", Name: "Tool", Description: "Does things", Language: "Go", Stars: 4200,
		AvatarURL: "https://avatars.example/acme", Topics: []string{"cli", "windows"},
	}
}

func TestASavedRepositoryKeepsItsSnapshotAndNoteAcrossARestart(t *testing.T) {
	h := openHarness(t)
	saved, err := h.catalog.SaveForLater(t.Context(), discovered(), "for the weekend")
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == 0 || saved.SavedAt.IsZero() {
		t.Fatalf("saved = %+v", saved)
	}
	if err := h.catalog.SetTryLaterNote(t.Context(), saved.ID, "try after the move"); err != nil {
		t.Fatal(err)
	}

	h.reopen()
	got, ok := h.catalog.FindTryLater(source.RepoRef{Owner: "acme", Repo: "tool"})
	if !ok {
		t.Fatal("the saved repository was not found, ignoring case, after a restart")
	}
	if got.Description != "Does things" || got.Language != "Go" || got.Stars != 4200 || got.AvatarURL != "https://avatars.example/acme" ||
		len(got.Topics) != 2 || got.Topics[1] != "windows" || got.Note != "try after the move" || !got.SavedAt.Equal(saved.SavedAt) {
		t.Errorf("reloaded = %+v", got)
	}
	if repo := got.Repository(); repo.Ref().String() != "Acme/Tool" || repo.Stars != 4200 {
		t.Errorf("snapshot as a repository = %+v", repo)
	}
}

func TestSavingARepositoryTwiceKeepsTheFirstEntry(t *testing.T) {
	h := openHarness(t)
	first, err := h.catalog.SaveForLater(t.Context(), discovered(), "first")
	if err != nil {
		t.Fatal(err)
	}
	again := discovered()
	again.Owner, again.Name = "acme", "tool"
	existing, err := h.catalog.SaveForLater(t.Context(), again, "second")
	if !errors.Is(err, catalog.ErrSavedAlready) || existing.ID != first.ID || existing.Note != "first" {
		t.Fatalf("second save = %+v, %v", existing, err)
	}
	if n := len(h.catalog.TryLaterEntries().Get()); n != 1 {
		t.Errorf("%d entries, want 1", n)
	}
}

func TestRemovingASavedRepositoryForgetsIt(t *testing.T) {
	h := openHarness(t)
	saved, err := h.catalog.SaveForLater(t.Context(), discovered(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.RemoveTryLater(t.Context(), saved.ID); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if n := len(h.catalog.TryLaterEntries().Get()); n != 0 {
		t.Errorf("%d entries after removing and reopening", n)
	}
	if err := h.catalog.SetTryLaterNote(t.Context(), saved.ID, "x"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("note on a removed entry: %v", err)
	}
}

func TestTrackingASavedRepositoryRemovesItFromTryLater(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1.0.0", 1)))
	other := discovered()
	other.Name = "other"
	for _, repo := range []source.Repository{discovered(), other} {
		if _, err := h.catalog.SaveForLater(t.Context(), repo, ""); err != nil {
			t.Fatal(err)
		}
	}
	// The saved spelling differs from the tracked one.
	h.add("tool", catalog.AddOptions{})

	entries := h.catalog.TryLaterEntries().Get()
	if len(entries) != 1 || entries[0].Repo != "other" {
		t.Fatalf("saved after tracking = %+v", entries)
	}
	h.reopen()
	if n := len(h.catalog.TryLaterEntries().Get()); n != 1 {
		t.Errorf("%d saved after a restart, want 1", n)
	}
}
