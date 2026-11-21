package catalog

import (
	"context"
	"fmt"
	"time"

	libsync "github.com/aaronflorey/release-sync/sync"
)

// The times syncing compares are kept in Unix milliseconds, so two edits in the
// same second are still ordered.

func milli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMilli(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.UnixMilli(n)
}

// stampItem marks an Item as changed by the user at the given time.
func stampItem(ctx context.Context, db execer, id int64, at time.Time) error {
	if _, err := db.ExecContext(ctx, `UPDATE items SET modified_at = ? WHERE id = ?`, milli(at), id); err != nil {
		return fmt.Errorf("mark item %d as changed: %w", id, err)
	}
	return nil
}

// recordRemoval remembers that the user removed what key names, so that a sync
// can carry the removal to the other devices.
func recordRemoval(ctx context.Context, db execer, key string, at time.Time) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_tombstones (key, deleted_at) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET deleted_at = excluded.deleted_at`, key, milli(at)); err != nil {
		return fmt.Errorf("record the removal of %s: %w", key, err)
	}
	return nil
}

// forgetRemoval drops the record of a removal, for something that is back.
func forgetRemoval(ctx context.Context, db execer, key string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM sync_tombstones WHERE key = ?`, key); err != nil {
		return fmt.Errorf("forget the removal of %s: %w", key, err)
	}
	return nil
}

func itemKey(it Item) string {
	return libsync.Item{Source: it.Source, Owner: it.Owner, Repo: it.Repo}.Key()
}

func tryLaterKey(t TryLater) string {
	return libsync.TryLaterEntry{Source: t.Source, Owner: t.Owner, Repo: t.Repo}.Key()
}

func groupKey(name string) string { return libsync.GroupKey(name) }

func repoKey(prefix, site, owner, repo string) string {
	return prefix + libsync.RepoKey(site, owner, repo)
}
