package discover

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aaronflorey/release-sync/source"
)

// How long cached data stays in the database at all.
const (
	listKeptFor  = 14 * 24 * time.Hour
	checkKeptFor = 14 * 24 * time.Hour
)

// store keeps the Discover caches in SQLite. Each query finishes before the
// next starts: the pool has one connection.
type store struct{ db *sql.DB }

// cachedList is a downloaded list and when it was downloaded.
type cachedList struct {
	fetched      time.Time
	repositories []source.Repository
}

func (s store) list(ctx context.Context, key string) (cachedList, bool, error) {
	var fetched int64
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT fetched_at, repositories FROM discover_lists WHERE list_key = ?`, key).
		Scan(&fetched, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return cachedList{}, false, nil
	}
	if err != nil {
		return cachedList{}, false, fmt.Errorf("load list %s: %w", key, err)
	}
	var repos []source.Repository
	if err := json.Unmarshal([]byte(encoded), &repos); err != nil {
		// A list a past version wrote is only a cache: download it again.
		log.Printf("discover: ignoring unreadable cached list %s: %v", key, err)
		return cachedList{}, false, nil
	}
	return cachedList{fetched: time.Unix(fetched, 0), repositories: repos}, len(repos) > 0, nil
}

func (s store) saveList(ctx context.Context, key string, l cachedList, now time.Time) error {
	encoded, err := json.Marshal(l.repositories)
	if err != nil {
		return fmt.Errorf("encode list %s: %w", key, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save list %s: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO discover_lists (list_key, fetched_at, repositories) VALUES (?, ?, ?)
		ON CONFLICT (list_key) DO UPDATE SET fetched_at = excluded.fetched_at, repositories = excluded.repositories`,
		key, l.fetched.Unix(), string(encoded)); err != nil {
		return fmt.Errorf("save list %s: %w", key, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM discover_lists WHERE fetched_at < ?`, now.Add(-listKeptFor).Unix()); err != nil {
		return fmt.Errorf("drop old lists: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save list %s: %w", key, err)
	}
	return nil
}

func (s store) snapshots(ctx context.Context, key string) ([]Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT taken_at, ranks FROM rank_snapshots WHERE list_key = ? ORDER BY taken_at`, key)
	if err != nil {
		return nil, fmt.Errorf("load rankings of %s: %w", key, err)
	}
	defer func() { _ = rows.Close() }()
	var history []Snapshot
	for rows.Next() {
		var at int64
		var encoded string
		if err := rows.Scan(&at, &encoded); err != nil {
			return nil, fmt.Errorf("load rankings of %s: %w", key, err)
		}
		snap := Snapshot{At: time.Unix(at, 0)}
		if err := json.Unmarshal([]byte(encoded), &snap.Ranks); err != nil {
			log.Printf("discover: ignoring unreadable ranking of %s: %v", key, err)
			continue
		}
		history = append(history, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load rankings of %s: %w", key, err)
	}
	return history, nil
}

func (s store) addSnapshot(ctx context.Context, key string, snap Snapshot) error {
	encoded, err := json.Marshal(snap.Ranks)
	if err != nil {
		return fmt.Errorf("encode ranking of %s: %w", key, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save ranking of %s: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO rank_snapshots (list_key, taken_at, ranks) VALUES (?, ?, ?)`,
		key, snap.At.Unix(), string(encoded)); err != nil {
		return fmt.Errorf("save ranking of %s: %w", key, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rank_snapshots WHERE list_key = ? AND taken_at NOT IN
		(SELECT taken_at FROM rank_snapshots WHERE list_key = ? ORDER BY taken_at DESC LIMIT ?)`,
		key, key, snapshotsKept); err != nil {
		return fmt.Errorf("trim rankings of %s: %w", key, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save ranking of %s: %w", key, err)
	}
	return nil
}

func checkKey(r source.Repository) string { return strings.ToLower(r.Owner + "/" + r.Name) }

// answers returns the remembered answers for repositories no older than
// notBefore, by lower-cased owner/repo.
func (s store) answers(ctx context.Context, machine string, repos []source.Repository, notBefore time.Time) (map[string]bool, error) {
	found := map[string]bool{}
	for _, r := range repos {
		var has bool
		err := s.db.QueryRowContext(ctx, `SELECT has_download FROM download_checks
			WHERE source = ? AND owner = ? AND repo = ? AND machine = ? AND checked_at >= ?`,
			source.GitHubName, r.Owner, r.Name, machine, notBefore.Unix()).Scan(&has)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load the download check of %s/%s: %w", r.Owner, r.Name, err)
		}
		found[checkKey(r)] = has
	}
	return found, nil
}

func (s store) saveAnswers(ctx context.Context, machine string, answers map[string]bool, repos []source.Repository, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save download checks: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range repos {
		has, ok := answers[checkKey(r)]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO download_checks (source, owner, repo, machine, has_download, checked_at)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (source, owner, repo, machine) DO UPDATE SET
			has_download = excluded.has_download, checked_at = excluded.checked_at`,
			source.GitHubName, r.Owner, r.Name, machine, has, now.Unix()); err != nil {
			return fmt.Errorf("save the download check of %s/%s: %w", r.Owner, r.Name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM download_checks WHERE checked_at < ?`, now.Add(-checkKeptFor).Unix()); err != nil {
		return fmt.Errorf("drop old download checks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save download checks: %w", err)
	}
	return nil
}
