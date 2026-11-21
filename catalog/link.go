package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/aaronflorey/release-sync/version"
)

// SetLinkVerified records that the Item's repository is the project its
// software comes from, after the user confirmed it.
func (c *Catalog) SetLinkVerified(ctx context.Context, id int64) error {
	return c.updateItem(ctx, id, `UPDATE items SET link_guessed = ? WHERE id = ?`, false)
}

// ChangeRepository points an Item at another repository, whose releases the
// preview holds. The Item keeps its notes, group, preferences and installed
// version. Its cached releases, the file chosen for it and its skipped and
// announced versions belong to the old repository and are dropped. The new Link
// is verified: the user chose it. It returns ErrExists, with the Entry, when
// another Item already tracks the repository.
func (c *Catalog) ChangeRepository(ctx context.Context, id int64, p Preview) (Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.Get(id)
	if !ok {
		return Entry{}, ErrNotFound
	}
	draft := p.Draft(AddOptions{IncludePrereleases: current.Item.IncludePrereleases})
	if other, found := c.Find(draft.Ref()); found && other.Item.ID != id {
		return other, ErrExists
	}
	topics, err := marshalTopics(draft.Item.Topics)
	if err != nil {
		return Entry{}, err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("change repository of %s: %w", current.FullName(), err)
	}
	defer func() { _ = tx.Rollback() }()
	it := draft.Item
	if _, err := tx.ExecContext(ctx, `UPDATE items SET source = ?, owner = ?, repo = ?, lane = ?, name = ?, description = ?,
		avatar_url = ?, language = ?, topics = ?, stars = ?, category = ?, metadata_fetched = 1, link_guessed = 0, skipped_version = '',
		notified_version = '', repo_missing = 0, etag = ?, last_checked = ?, last_error_kind = ?, last_error = '',
		modified_at = ? WHERE id = ?`, it.Source, it.Owner, it.Repo, it.Lane, it.Name, it.Description, it.AvatarURL, it.Language, topics, it.Stars,
		string(it.Category), it.ETag, unix(c.now()), string(it.LastFailure), milli(c.now()), id); err != nil {
		return Entry{}, fmt.Errorf("change repository of %s: %w", current.FullName(), err)
	}
	// Other devices know the Item by its repository: the old one is removed
	// and the new one stands in its place.
	if oldKey, newKey := itemKey(current.Item), itemKey(it); oldKey != newKey {
		if err := recordRemoval(ctx, tx, oldKey, c.now()); err != nil {
			return Entry{}, fmt.Errorf("change repository of %s: %w", current.FullName(), err)
		}
		if err := forgetRemoval(ctx, tx, newKey); err != nil {
			return Entry{}, fmt.Errorf("change repository of %s: %w", current.FullName(), err)
		}
	}
	if err := replaceReleases(ctx, tx, id, draft.Releases); err != nil {
		return Entry{}, fmt.Errorf("change repository of %s: %w", current.FullName(), err)
	}
	if err := writeIntent(ctx, tx, id, nil); err != nil {
		return Entry{}, fmt.Errorf("change repository of %s: %w", current.FullName(), err)
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("change repository of %s: %w", current.FullName(), err)
	}
	return c.publish(ctx, id)
}

// TagForVersion is the release to record for software at the version seen on
// the machine. It is the release the version belongs to; the latest release
// when the version is unknown or not older than it; otherwise the version as
// it was seen, so the update stays visible. It is "" when nothing is released.
func (e Entry) TagForVersion(seen string) string {
	seen = strings.TrimSpace(seen)
	latest, ok := e.Latest()
	if !ok {
		return ""
	}
	if !strings.ContainsAny(seen, "0123456789") {
		return latest.Tag
	}
	tags := make([]string, len(e.Releases))
	for i, r := range e.Releases {
		tags[i] = r.Tag
	}
	if tag := version.MatchTag(seen, tags); tag != "" {
		return tag
	}
	if e.newer(latest.Tag, seen) {
		return seen
	}
	return latest.Tag
}
