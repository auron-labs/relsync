package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/source"
)

// ErrSavedAlready means the repository is already saved for later.
var ErrSavedAlready = errors.New("repository is already saved for later")

// TryLater is a repository saved to look at later: its identity, what was
// known about it when it was saved, and the user's note.
type TryLater struct {
	ID          int64
	Source      string
	Owner       string
	Repo        string
	Description string
	Language    string
	Stars       int
	AvatarURL   string
	Topics      []string
	Note        string
	SavedAt     time.Time
	// ModifiedAt is when the user last changed the note, zero if never.
	ModifiedAt time.Time
}

// Ref names the saved repository.
func (t TryLater) Ref() source.RepoRef { return source.NewRef(t.Source, t.Owner, t.Repo) }

// FullName is owner/repo.
func (t TryLater) FullName() string { return t.Ref().String() }

// Repository is the saved snapshot as the metadata of a repository. What the
// snapshot does not keep, such as forks or the creation date, is zero.
func (t TryLater) Repository() source.Repository {
	return source.Repository{
		Owner: t.Owner, Name: t.Repo, Description: t.Description, Language: t.Language,
		Topics: t.Topics, Stars: t.Stars, AvatarURL: t.AvatarURL, URL: source.RepoURL(t.Ref()),
	}
}

// TryLaterEntries is every saved repository, oldest first, as a cell views
// read and subscribe to.
func (c *Catalog) TryLaterEntries() *state.Cell[[]TryLater] { return c.tryLater }

// FindTryLater returns the saved repository, ignoring case.
func (c *Catalog) FindTryLater(ref source.RepoRef) (TryLater, bool) {
	for _, t := range c.tryLater.Get() {
		if sameRepository(t.Ref(), ref) {
			return t, true
		}
	}
	return TryLater{}, false
}

// SaveForLater keeps a repository, with a snapshot of its metadata and a note.
// It returns ErrSavedAlready, with the saved entry, when it is saved already.
func (c *Catalog) SaveForLater(ctx context.Context, repo source.Repository, note string) (TryLater, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.FindTryLater(repo.Ref()); ok {
		return existing, ErrSavedAlready
	}
	topics, err := marshalTopics(repo.Topics)
	if err != nil {
		return TryLater{}, err
	}
	saved := TryLater{
		Source: source.GitHubName, Owner: repo.Owner, Repo: repo.Name, Description: repo.Description,
		Language: repo.Language, Stars: repo.Stars, AvatarURL: repo.AvatarURL, Topics: slices.Clone(repo.Topics),
		Note: note, SavedAt: c.now().Truncate(time.Second),
	}
	saved.ModifiedAt = fromMilli(milli(c.now()))
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return TryLater{}, fmt.Errorf("save %s for later: %w", repo.Ref(), err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO try_later
		(source, owner, repo, description, language, stars, avatar_url, topics, note, saved_at, modified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		saved.Source, saved.Owner, saved.Repo, saved.Description, saved.Language, saved.Stars, saved.AvatarURL,
		topics, saved.Note, unix(saved.SavedAt), milli(saved.ModifiedAt))
	if err != nil {
		return TryLater{}, fmt.Errorf("save %s for later: %w", repo.Ref(), err)
	}
	if saved.ID, err = result.LastInsertId(); err != nil {
		return TryLater{}, fmt.Errorf("save %s for later: %w", repo.Ref(), err)
	}
	if err := forgetRemoval(ctx, tx, tryLaterKey(saved)); err != nil {
		return TryLater{}, fmt.Errorf("save %s for later: %w", repo.Ref(), err)
	}
	if err := tx.Commit(); err != nil {
		return TryLater{}, fmt.Errorf("save %s for later: %w", repo.Ref(), err)
	}
	c.tryLater.Update(func(entries *[]TryLater) { *entries = append(slices.Clone(*entries), saved) })
	return saved, nil
}

// SetTryLaterNote changes the note of a saved repository.
func (c *Catalog) SetTryLaterNote(ctx context.Context, id int64, note string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	modified := c.now()
	result, err := c.db.ExecContext(ctx, `UPDATE try_later SET note = ?, modified_at = ? WHERE id = ?`, note, milli(modified), id)
	if err != nil {
		return fmt.Errorf("save note of saved repository %d: %w", id, err)
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	c.tryLater.Update(func(entries *[]TryLater) {
		next := slices.Clone(*entries)
		if i := slices.IndexFunc(next, func(t TryLater) bool { return t.ID == id }); i >= 0 {
			next[i].Note, next[i].ModifiedAt = note, fromMilli(milli(modified))
		}
		*entries = next
	})
	return nil
}

// RemoveTryLater forgets a saved repository.
func (c *Catalog) RemoveTryLater(ctx context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("remove saved repository %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM try_later WHERE id = ?`, id); err != nil {
		return fmt.Errorf("remove saved repository %d: %w", id, err)
	}
	if saved, ok := c.savedByID(id); ok {
		if err := recordRemoval(ctx, tx, tryLaterKey(saved), c.now()); err != nil {
			return fmt.Errorf("remove saved repository %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("remove saved repository %d: %w", id, err)
	}
	c.tryLater.Update(func(entries *[]TryLater) {
		*entries = slices.DeleteFunc(slices.Clone(*entries), func(t TryLater) bool { return t.ID == id })
	})
	return nil
}

// forgetTryLater drops the saved repository named by ref from the cell, once
// the transaction that deleted its row has committed.
func (c *Catalog) forgetTryLater(ref source.RepoRef) {
	c.tryLater.Update(func(entries *[]TryLater) {
		*entries = slices.DeleteFunc(slices.Clone(*entries), func(t TryLater) bool {
			return sameRepository(t.Ref(), ref)
		})
	})
}

func (c *Catalog) savedByID(id int64) (TryLater, bool) {
	for _, t := range c.tryLater.Get() {
		if t.ID == id {
			return t, true
		}
	}
	return TryLater{}, false
}

// deleteTryLater removes the saved repository named by ref, if there is one,
// and records the removal for syncing at the time given.
func deleteTryLater(ctx context.Context, db execer, ref source.RepoRef, at time.Time) error {
	result, err := db.ExecContext(ctx, `DELETE FROM try_later WHERE source = ? AND owner = ? COLLATE NOCASE AND repo = ? COLLATE NOCASE`,
		ref.SourceKey(), ref.Owner, ref.Repo)
	if err != nil {
		return fmt.Errorf("remove %s from the saved repositories: %w", ref, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove %s from the saved repositories: %w", ref, err)
	}
	if n == 0 {
		return nil
	}
	return recordRemoval(ctx, db, repoKey("later:", ref.SourceKey(), ref.Owner, ref.Repo), at)
}

func (c *Catalog) loadTryLater(ctx context.Context) ([]TryLater, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id, source, owner, repo, description, language, stars, avatar_url,
		topics, note, saved_at, modified_at FROM try_later ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load saved repositories: %w", err)
	}
	return scanTryLater(rows)
}

func scanTryLater(rows *sql.Rows) ([]TryLater, error) {
	defer func() { _ = rows.Close() }()
	var saved []TryLater
	for rows.Next() {
		var t TryLater
		var topics string
		var at, modified int64
		if err := rows.Scan(&t.ID, &t.Source, &t.Owner, &t.Repo, &t.Description, &t.Language, &t.Stars, &t.AvatarURL,
			&topics, &t.Note, &at, &modified); err != nil {
			return nil, fmt.Errorf("load saved repositories: %w", err)
		}
		if err := json.Unmarshal([]byte(topics), &t.Topics); err != nil {
			return nil, fmt.Errorf("decode topics of saved %s/%s: %w", t.Owner, t.Repo, err)
		}
		t.SavedAt, t.ModifiedAt = fromUnix(at), fromMilli(modified)
		saved = append(saved, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load saved repositories: %w", err)
	}
	return saved, nil
}
