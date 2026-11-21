package activity_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/auron-labs/mygo-framework/database"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/database/migrations"
)

func openDB(t *testing.T, path string) *database.Database {
	t.Helper()
	db, err := database.Open(t.Context(), path, migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestEntriesAreNewestFirstAndSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db := openDB(t, path)
	log, err := activity.Open(t.Context(), db.SQL)
	if err != nil {
		t.Fatal(err)
	}
	notified := 0
	log.Entries().Subscribe(func([]activity.Entry) { notified++ })
	for i, level := range []activity.Level{activity.Info, activity.Success, activity.Error} {
		if err := log.Add(t.Context(), level, 0, fmt.Sprintf("message %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if notified == 0 {
		t.Error("subscribers were not told")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := activity.Open(t.Context(), openDB(t, path).SQL)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Entries().Get()
	if len(got) != 3 || got[0].Message != "message 2" || got[0].Level != activity.Error || got[2].Message != "message 0" {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].At.IsZero() || got[0].ID <= got[1].ID {
		t.Errorf("entry = %+v", got[0])
	}
}

func TestLogKeepsTheNewest1000(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "app.db"))
	log, err := activity.Open(t.Context(), db.SQL)
	if err != nil {
		t.Fatal(err)
	}
	for i := range activity.Limit + 5 {
		if err := log.Add(t.Context(), activity.Info, 0, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	entries := log.Entries().Get()
	if len(entries) != activity.Limit || entries[0].Message != fmt.Sprintf("m%d", activity.Limit+4) || entries[len(entries)-1].Message != "m5" {
		t.Fatalf("%d entries, first %q, last %q", len(entries), entries[0].Message, entries[len(entries)-1].Message)
	}
	var stored int
	if err := db.SQL.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM activity`).Scan(&stored); err != nil || stored != activity.Limit {
		t.Errorf("%d rows stored (%v)", stored, err)
	}
}

func TestClearEmptiesTheLogForGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db := openDB(t, path)
	log, err := activity.Open(t.Context(), db.SQL)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Add(t.Context(), activity.Warning, 0, "something"); err != nil {
		t.Fatal(err)
	}
	if err := log.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(log.Entries().Get()) != 0 {
		t.Error("entries remain after Clear")
	}
	reopened, err := activity.Open(t.Context(), db.SQL)
	if err != nil || len(reopened.Entries().Get()) != 0 {
		t.Errorf("after reload: %v, %v", reopened.Entries().Get(), err)
	}
}

func TestEntryAboutAnItemOutlivesTheItemWithoutIt(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "app.db"))
	ctx := t.Context()
	if _, err := db.SQL.ExecContext(ctx, `INSERT INTO items (id, source, owner, repo, added_at) VALUES (7, 'github', 'a', 'b', 1)`); err != nil {
		t.Fatal(err)
	}
	log, err := activity.Open(ctx, db.SQL)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Add(ctx, activity.Info, 7, "Added a/b"); err != nil {
		t.Fatal(err)
	}
	if got := log.Entries().Get()[0]; got.ItemID != 7 {
		t.Fatalf("entry = %+v", got)
	}
	if _, err := db.SQL.ExecContext(ctx, `DELETE FROM items WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	reopened, err := activity.Open(ctx, db.SQL)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Entries().Get(); len(got) != 1 || got[0].ItemID != 0 || got[0].Message != "Added a/b" {
		t.Errorf("entries after the item was removed = %+v", got)
	}
}
