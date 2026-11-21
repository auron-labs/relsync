package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/aaronflorey/release-sync/category"
	"github.com/aaronflorey/release-sync/source"
)

// anonymousFloor is how few anonymous requests may remain before optional
// requests, such as fetching repository metadata, are skipped.
const anonymousFloor = 25

// CheckResult is the outcome of checking one Item.
type CheckResult struct {
	// Failure is why the site could not be asked, nil when the check worked.
	// It is already recorded on the Item.
	Failure error
}

// RateLimited reports a check stopped by the request allowance.
func (r CheckResult) RateLimited() bool {
	var e *source.Error
	return errors.As(r.Failure, &e) && e.Kind == source.KindRateLimited
}

// Check asks the source about an Item and records what it says: repository
// metadata once, then the newest releases. The request is conditional on the
// Item's ETag unless force is set, so an unchanged repository keeps its cached
// releases. A failed request is recorded on the Item and returned in the
// result; the returned error is for a canceled context or a failed write.
func (c *Catalog) Check(ctx context.Context, id int64, force bool) (CheckResult, error) {
	entry, ok := c.Get(id)
	if !ok {
		return CheckResult{}, ErrNotFound
	}
	var repo *source.Repository
	if !entry.Item.MetadataFetched && c.metadataAffordable(entry.Ref()) {
		fetched, err := c.source.Repository(ctx, entry.Ref())
		if err != nil {
			return c.recordFailure(ctx, id, nil, err)
		}
		repo = &fetched
	}
	etag := ""
	if !force && len(entry.Releases) > 0 {
		etag = entry.Item.ETag
	}
	page, err := c.source.Releases(ctx, entry.Ref(), etag)
	if err != nil {
		return c.recordFailure(ctx, id, repo, err)
	}
	if err := c.recordSuccess(ctx, id, repo, page); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{}, nil
}

// metadataAffordable reports whether the allowance can spare a request that no
// check strictly needs.
func (c *Catalog) metadataAffordable(ref source.RepoRef) bool {
	a := source.AllowanceOf(c.source, ref)
	return a.Authenticated || !a.Known || a.Remaining > anonymousFloor
}

func (c *Catalog) recordSuccess(ctx context.Context, id int64, repo *source.Repository, page source.ReleasePage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Get(id)
	if !ok {
		return nil // Stopped being tracked while the request ran.
	}
	if repo != nil {
		applyRepository(&entry.Item, *repo)
	}
	if !page.NotModified {
		entry.Releases = cacheable(page.Releases)
		entry.Item.ETag = page.ETag
	}
	entry.Item.LastChecked = c.now()
	entry.Item.RepoMissing = false
	entry.Item.LastFailure, entry.Item.LastFailureText = FailNone, ""
	if _, found := entry.Latest(); !found {
		entry.Item.LastFailure = FailNoReleases
	}
	return c.saveCheck(ctx, entry, !page.NotModified)
}

func (c *Catalog) recordFailure(ctx context.Context, id int64, repo *source.Repository, cause error) (CheckResult, error) {
	if ctx.Err() != nil {
		return CheckResult{}, fmt.Errorf("check item %d: %w", id, ctx.Err())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Get(id)
	if !ok {
		return CheckResult{Failure: cause}, nil
	}
	if repo != nil {
		applyRepository(&entry.Item, *repo)
	}
	entry.Item.LastChecked = c.now()
	entry.Item.LastFailure = FailOther
	var sourceErr *source.Error
	if errors.As(cause, &sourceErr) && sourceErr.Kind == source.KindNotFound {
		entry.Item.LastFailure, entry.Item.RepoMissing = FailNotFound, true
	}
	entry.Item.LastFailureText = c.describe(cause)
	if err := c.saveCheck(ctx, entry, false); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Failure: cause}, nil
}

func applyRepository(it *Item, repo source.Repository) {
	it.Name, it.Description, it.Language = repo.Name, repo.Description, repo.Language
	it.Topics, it.Stars, it.MetadataFetched = repo.Topics, repo.Stars, true
	it.Category = category.Of(repo.Name, repo.Description, repo.Topics)
	if repo.AvatarURL != "" {
		it.AvatarURL = repo.AvatarURL
	}
}

// saveCheck writes the columns a check owns, so a user's choice made while the
// request ran is not overwritten, and publishes the result. The caller holds mu.
func (c *Catalog) saveCheck(ctx context.Context, entry Entry, releasesChanged bool) error {
	it := entry.Item
	topics, err := marshalTopics(it.Topics)
	if err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save check of %s: %w", entry.FullName(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE items SET name = ?, description = ?, avatar_url = ?, language = ?,
		topics = ?, stars = ?, metadata_fetched = ?, category = ?, etag = ?, last_checked = ?, last_error_kind = ?,
		last_error = ?, repo_missing = ? WHERE id = ?`,
		it.Name, it.Description, it.AvatarURL, it.Language, topics, it.Stars, it.MetadataFetched, string(it.Category),
		it.ETag, unix(it.LastChecked), string(it.LastFailure), it.LastFailureText, it.RepoMissing, it.ID); err != nil {
		return fmt.Errorf("save check of %s: %w", entry.FullName(), err)
	}
	if releasesChanged {
		if err := replaceReleases(ctx, tx, it.ID, entry.Releases); err != nil {
			return fmt.Errorf("save check of %s: %w", entry.FullName(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save check of %s: %w", entry.FullName(), err)
	}
	_, err = c.publish(ctx, it.ID)
	return err
}
