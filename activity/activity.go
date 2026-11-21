// Package activity keeps the persisted, append-only log shown on the Activity page.
package activity

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/auron-labs/mygo-framework/state"
)

// Limit is how many entries the log keeps; older ones are dropped.
const Limit = 1000

// Level classifies an entry.
type Level string

// The entry levels.
const (
	Info    Level = "info"
	Success Level = "success"
	Warning Level = "warning"
	Error   Level = "error"
)

// Entry is one line of the log.
type Entry struct {
	ID    int64
	At    time.Time
	Level Level
	// ItemID is the Item the entry is about, 0 for none. The stored link is
	// cleared when the Item is removed.
	ItemID  int64
	Message string
}

// Log persists entries and publishes them, newest first, for views. It is safe
// for concurrent use.
type Log struct {
	db  *sql.DB
	now func() time.Time

	mu      sync.Mutex
	entries *state.Cell[[]Entry]
}

// Open loads the log from db.
func Open(ctx context.Context, db *sql.DB) (*Log, error) {
	l := &Log{db: db, now: time.Now}
	loaded, err := l.load(ctx)
	if err != nil {
		return nil, err
	}
	l.entries = state.NewCell(loaded)
	return l, nil
}

// Entries is the log, newest first, as a cell views read and subscribe to.
func (l *Log) Entries() *state.Cell[[]Entry] { return l.entries }

// Add appends an entry about itemID (0 for none).
func (l *Log) Add(ctx context.Context, level Level, itemID int64, message string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := Entry{At: l.now().Truncate(time.Second), Level: level, ItemID: itemID, Message: message}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("add activity: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO activity (at, level, item_id, message)
		VALUES (?, ?, (SELECT id FROM items WHERE id = ?), ?)`,
		entry.At.Unix(), string(level), itemID, message)
	if err != nil {
		return fmt.Errorf("add activity: %w", err)
	}
	if entry.ID, err = result.LastInsertId(); err != nil {
		return fmt.Errorf("add activity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM activity WHERE id NOT IN (SELECT id FROM activity ORDER BY id DESC LIMIT ?)`, Limit); err != nil {
		return fmt.Errorf("trim activity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("add activity: %w", err)
	}
	l.entries.Update(func(entries *[]Entry) {
		next := append([]Entry{entry}, *entries...)
		*entries = next[:min(len(next), Limit)]
	})
	return nil
}

// Clear empties the log.
func (l *Log) Clear(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.db.ExecContext(ctx, `DELETE FROM activity`); err != nil {
		return fmt.Errorf("clear activity: %w", err)
	}
	l.entries.Set(nil)
	return nil
}

func (l *Log) load(ctx context.Context) ([]Entry, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT id, at, level, COALESCE(item_id, 0), message FROM activity ORDER BY id DESC LIMIT ?`, Limit)
	if err != nil {
		return nil, fmt.Errorf("load activity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var entries []Entry
	for rows.Next() {
		var e Entry
		var at int64
		var level string
		if err := rows.Scan(&e.ID, &at, &level, &e.ItemID, &e.Message); err != nil {
			return nil, fmt.Errorf("load activity: %w", err)
		}
		e.At, e.Level = time.Unix(at, 0), Level(level)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load activity: %w", err)
	}
	return entries, nil
}
