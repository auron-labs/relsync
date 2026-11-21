package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/category"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
	libsync "github.com/aaronflorey/release-sync/sync"
	"github.com/aaronflorey/release-sync/version"
)

const (
	// maxReleases is how many releases are cached per Item.
	maxReleases = 30
	// maxNotes bounds the release notes cached per release.
	maxNotes = 5000
)

var (
	// ErrNotFound means the Item is not in the Library.
	ErrNotFound = errors.New("item not found")
	// ErrExists means the repository is already tracked.
	ErrExists = errors.New("repository is already tracked")
	// ErrNotInstalled means the Item has no install this application made.
	ErrNotInstalled = errors.New("item is not installed by this application")
	// ErrNoRelease means there is no release to take a version from.
	ErrNoRelease = errors.New("no release known")
)

// Catalog is the Library's persistent store. It publishes every Item as an
// Entry in a cell that views read and subscribe to, and is safe for
// concurrent use.
type Catalog struct {
	db     *sql.DB
	source source.Source
	// describe turns a failed check into text for the user.
	describe func(error) string
	now      func() time.Time
	// policy is what the machine and settings say about choosing a file. It
	// changes with the settings, apart from the writes mu serializes.
	policy atomic.Pointer[selection.Policy]

	// mu serializes writes with the cell update that follows them.
	mu       sync.Mutex
	entries  *state.Cell[[]Entry]
	tryLater *state.Cell[[]TryLater]
	groups   *state.Cell[[]Group]
}

// Open loads the Library from db. describe explains a failed check to the
// user; nil uses the error's own text.
func Open(ctx context.Context, db *sql.DB, src source.Source, describe func(error) string) (*Catalog, error) {
	if describe == nil {
		describe = func(err error) string { return err.Error() }
	}
	c := &Catalog{db: db, source: src, describe: describe, now: time.Now}
	c.policy.Store(&selection.Policy{PreferInstallers: true})
	// A removal older than the sync forgets it is no use to anyone.
	if _, err := db.ExecContext(ctx, `DELETE FROM sync_tombstones WHERE deleted_at < ?`, milli(c.now().Add(-libsync.TombstoneLife))); err != nil {
		return nil, fmt.Errorf("forget old removals: %w", err)
	}
	loaded, err := c.load(ctx, 0)
	if err != nil {
		return nil, err
	}
	c.entries = state.NewCell(loaded)
	saved, err := c.loadTryLater(ctx)
	if err != nil {
		return nil, err
	}
	c.tryLater = state.NewCell(saved)
	groups, err := c.loadGroups(ctx)
	if err != nil {
		return nil, err
	}
	c.groups = state.NewCell(groups)
	return c, nil
}

// Allowance is the request allowance of the credentials the Library's source uses.
func (c *Catalog) Allowance() source.Allowance { return c.source.Allowance() }

// Entries is the whole Library, oldest Item first, as a cell views read and
// subscribe to.
func (c *Catalog) Entries() *state.Cell[[]Entry] { return c.entries }

// Policy is what the machine and settings currently say about choosing a file.
func (c *Catalog) Policy() selection.Policy { return *c.policy.Load() }

// SetPolicy changes what the machine and settings say about choosing a file,
// and chooses again for every Item.
func (c *Catalog) SetPolicy(p selection.Policy) {
	if c.Policy() == p {
		return
	}
	c.policy.Store(&p)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries.Update(func(entries *[]Entry) {
		next := slices.Clone(*entries)
		for i := range next {
			next[i].Policy = p
			next[i] = next[i].withOffer()
		}
		*entries = next
	})
}

// Get returns the Entry with the given id.
func (c *Catalog) Get(id int64) (Entry, bool) {
	for _, e := range c.entries.Get() {
		if e.Item.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Find returns the tracked Entry for a repository, ignoring case.
func (c *Catalog) Find(ref source.RepoRef) (Entry, bool) {
	for _, e := range c.entries.Get() {
		if sameRepository(e.Ref(), ref) {
			return e, true
		}
	}
	return Entry{}, false
}

// sameRepository reports two names of one repository: on the same site, with
// the same owner and name, ignoring case.
func sameRepository(a, b source.RepoRef) bool {
	return a.SourceKey() == b.SourceKey() && strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Repo, b.Repo)
}

// Preview is what the site says about a repository, before it is tracked.
type Preview struct {
	Repository source.Repository
	Page       source.ReleasePage
	policy     selection.Policy
	// bare says the repository's metadata was not asked for: Repository is
	// only its name.
	bare bool
}

// Preview asks the source about a repository without tracking it.
func (c *Catalog) Preview(ctx context.Context, ref source.RepoRef) (Preview, error) {
	repo, err := c.source.Repository(ctx, ref)
	if err != nil {
		return Preview{}, fmt.Errorf("preview %s: %w", ref, err)
	}
	return c.PreviewRepository(ctx, repo)
}

// PreviewReleases is Preview without asking for the repository's metadata,
// which a later check fetches, so one request is spent instead of two. A
// repository on a host of unknown kind needs the metadata call to be found,
// so it gets the full Preview.
func (c *Catalog) PreviewReleases(ctx context.Context, ref source.RepoRef) (Preview, error) {
	if ref.Unresolved() {
		return c.Preview(ctx, ref)
	}
	page, err := c.source.Releases(ctx, ref, "")
	if err != nil {
		return Preview{}, fmt.Errorf("preview %s: %w", ref, err)
	}
	repo := source.Repository{Source: ref.Source, Owner: ref.Owner, Name: ref.Repo}
	return Preview{Repository: repo, Page: page, policy: *c.policy.Load(), bare: true}, nil
}

// PreviewRepository is Preview for a repository whose metadata is known
// already, as a search result's is, so only its releases are asked for.
func (c *Catalog) PreviewRepository(ctx context.Context, repo source.Repository) (Preview, error) {
	page, err := c.source.Releases(ctx, repo.Ref(), "")
	if err != nil {
		return Preview{}, fmt.Errorf("preview %s: %w", repo.Ref(), err)
	}
	return Preview{Repository: repo, Page: page, policy: *c.policy.Load()}, nil
}

// Draft is the Entry that tracking the previewed repository would create.
// Its Item has no id yet.
func (p Preview) Draft(o AddOptions) Entry {
	releases := cacheable(p.Page.Releases)
	item := Item{
		Source: p.Repository.Ref().SourceKey(), Owner: p.Repository.Owner, Repo: p.Repository.Name,
		Lane: laneOf(releases, o.IncludePrereleases), Name: p.Repository.Name,
		Description: p.Repository.Description, AvatarURL: p.Repository.AvatarURL,
		Language: p.Repository.Language, Topics: p.Repository.Topics, Stars: p.Repository.Stars,
		MetadataFetched: !p.bare, IncludePrereleases: o.IncludePrereleases, ETag: p.Page.ETag, Kind: o.Kind,
		InstallFolder: o.InstallFolder, LinkGuessed: o.LinkGuessed, GroupID: o.GroupID,
		Category: category.Of(p.Repository.Name, p.Repository.Description, p.Repository.Topics),
	}
	entry := Entry{Item: item, Releases: releases, Intent: o.Intent, Policy: p.policy}
	if _, ok := entry.Latest(); !ok {
		entry.Item.LastFailure = FailNoReleases
	}
	return entry.withOffer()
}

// laneOf picks the lane to follow when the releases fall into more than one
// tag-prefix family: the family of the newest release. One family needs no lane.
func laneOf(releases []source.Release, includePrereleases bool) string {
	usable := candidates(releases, includePrereleases, "")
	if len(usable) == 0 {
		return ""
	}
	lane := version.Lane(usable[0].Tag)
	for _, r := range usable[1:] {
		if version.Lane(r.Tag) != lane {
			return lane
		}
	}
	return ""
}

// AddOptions are the choices made when tracking a repository.
type AddOptions struct {
	IncludePrereleases bool
	// Kind is what the user chose the Item to be; empty detects it.
	Kind selection.ItemKind
	// Intent is the file the user chose; nil takes the best match.
	Intent *selection.Intent
	// InstallFolder is where a Portable app is to be installed; empty is the
	// default place.
	InstallFolder string
	// AlreadyHave records that the user has the software. InstalledVersion is
	// the version they have; blank means the latest.
	AlreadyHave      bool
	InstalledVersion string
	// Install records what is on the machine, for software this application did
	// not install: where it is and how it is started or removed. It only applies
	// with AlreadyHave.
	Install *install.Record
	// LinkGuessed says the repository was matched to the software by name, so
	// it has not been confirmed as the project the software comes from.
	LinkGuessed bool
	// GroupID is the group to put the Item in; 0 leaves it in its category's.
	GroupID int64
}

// Add tracks a previewed repository. It returns ErrExists, with the tracked
// Entry, when the repository is already in the Library.
func (c *Catalog) Add(ctx context.Context, p Preview, o AddOptions) (Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	draft := p.Draft(o)
	if existing, ok := c.Find(draft.Ref()); ok {
		return existing, ErrExists
	}
	if o.GroupID != 0 && !c.hasGroup(o.GroupID) {
		return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), ErrGroupNotFound)
	}
	draft.Item.AddedAt = c.now()
	draft.Item.LastChecked = draft.Item.AddedAt
	draft.Item.ModifiedAt = draft.Item.AddedAt

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), err)
	}
	defer func() { _ = tx.Rollback() }()
	id, err := insertItem(ctx, tx, draft.Item)
	if err != nil {
		return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), err)
	}
	if err := replaceReleases(ctx, tx, id, draft.Releases); err != nil {
		return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), err)
	}
	if o.Intent != nil {
		if err := writeIntent(ctx, tx, id, o.Intent); err != nil {
			return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), err)
		}
	}
	// A repository that is tracked no longer waits to be tried, and is no
	// longer one that was removed.
	if err := deleteTryLater(ctx, tx, draft.Ref(), draft.Item.AddedAt); err != nil {
		return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), err)
	}
	if err := forgetRemoval(ctx, tx, itemKey(draft.Item)); err != nil {
		return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), err)
	}
	if o.AlreadyHave {
		tag := strings.TrimSpace(o.InstalledVersion)
		if latest, ok := draft.Latest(); tag == "" && ok {
			tag = latest.Tag
		}
		if err := writeAdoption(ctx, tx, id, tag, draft.Item.AddedAt, o.Install); err != nil {
			return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("add %s: %w", draft.FullName(), err)
	}
	c.forgetTryLater(draft.Ref())
	return c.publish(ctx, id)
}

// Remove stops tracking an Item, and records the removal for syncing.
// Whatever is installed is unaffected.
func (c *Catalog) Remove(ctx context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, tracked := c.Get(id)
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("remove item %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id); err != nil {
		return fmt.Errorf("remove item %d: %w", id, err)
	}
	if tracked {
		if err := recordRemoval(ctx, tx, itemKey(entry.Item), c.now()); err != nil {
			return fmt.Errorf("remove item %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("remove item %d: %w", id, err)
	}
	c.entries.Update(func(entries *[]Entry) {
		*entries = slices.DeleteFunc(slices.Clone(*entries), func(e Entry) bool { return e.Item.ID == id })
	})
	return nil
}

// MarkInstalled records that the user has an Item at tag; blank means the
// latest version.
func (c *Catalog) MarkInstalled(ctx context.Context, id int64, tag string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.Get(id)
	if !ok {
		return ErrNotFound
	}
	if tag == "" {
		latest, found := entry.Latest()
		if !found {
			return fmt.Errorf("mark %s installed: %w", entry.FullName(), ErrNoRelease)
		}
		tag = latest.Tag
	}
	if err := upsertInstall(ctx, c.db, id, tag, c.now()); err != nil {
		return fmt.Errorf("mark %s installed: %w", entry.FullName(), err)
	}
	_, err := c.publish(ctx, id)
	return err
}

// SetSkippedVersion skips one release tag; blank stops skipping.
func (c *Catalog) SetSkippedVersion(ctx context.Context, id int64, tag string) error {
	return c.updateSynced(ctx, id, `UPDATE items SET skipped_version = ? WHERE id = ?`, tag)
}

// SetHoldUpdates ignores or stops ignoring updates for an Item.
func (c *Catalog) SetHoldUpdates(ctx context.Context, id int64, hold bool) error {
	return c.updateSynced(ctx, id, `UPDATE items SET hold_updates = ? WHERE id = ?`, hold)
}

// SetAutoUpdate makes an Item install its updates without asking, or stops
// doing so. Turning it on makes the update the Item has now new again, so the
// next check handles it instead of leaving it as already announced.
func (c *Catalog) SetAutoUpdate(ctx context.Context, id int64, auto bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, err := c.db.ExecContext(ctx, `UPDATE items SET auto_update = ?,
		notified_version = CASE WHEN ? THEN '' ELSE notified_version END, modified_at = ? WHERE id = ?`,
		auto, auto, milli(c.now()), id)
	if err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	_, err = c.publish(ctx, id)
	return err
}

// SetNotifiedVersions records, for each Item, the release tag the user has now
// been told about.
func (c *Catalog) SetNotifiedVersions(ctx context.Context, tags map[int64]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record notified versions: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for id, tag := range tags {
		if _, err := tx.ExecContext(ctx, `UPDATE items SET notified_version = ? WHERE id = ?`, tag, id); err != nil {
			return fmt.Errorf("record notified version of item %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record notified versions: %w", err)
	}
	for id := range tags {
		if _, err := c.publish(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// ForgetNotifiedVersions makes every Item's current update new again.
func (c *Catalog) ForgetNotifiedVersions(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.db.ExecContext(ctx, `UPDATE items SET notified_version = ''`); err != nil {
		return fmt.Errorf("forget notified versions: %w", err)
	}
	loaded, err := c.load(ctx, 0)
	if err != nil {
		return err
	}
	c.entries.Set(loaded)
	return nil
}

// SetTrustUnsigned makes an Item's unsigned programs install without a
// question. It never excuses a bad checksum or an invalid signature.
func (c *Catalog) SetTrustUnsigned(ctx context.Context, id int64, trust bool) error {
	return c.updateSynced(ctx, id, `UPDATE items SET trust_unsigned = ? WHERE id = ?`, trust)
}

// SetIncludePrereleases makes an Item track pre-releases or not.
func (c *Catalog) SetIncludePrereleases(ctx context.Context, id int64, include bool) error {
	return c.updateSynced(ctx, id, `UPDATE items SET include_prereleases = ? WHERE id = ?`, include)
}

// SetKind says what an Item is, empty to detect it again. The chosen file no
// longer applies to a different kind of Item, so it is forgotten.
func (c *Catalog) SetKind(ctx context.Context, id int64, kind selection.ItemKind) error {
	return c.updateChoice(ctx, id, `UPDATE items SET kind = ? WHERE id = ?`, string(kind), true)
}

// SetMethod limits how an App may be installed, empty for any way. The chosen
// file is forgotten, as it may be one the method rules out.
func (c *Catalog) SetMethod(ctx context.Context, id int64, method selection.Method) error {
	return c.updateChoice(ctx, id, `UPDATE items SET install_method = ? WHERE id = ?`, string(method), true)
}

// SetIntent keeps the file the user chose for an Item; nil goes back to taking
// the best match.
func (c *Catalog) SetIntent(ctx context.Context, id int64, intent *selection.Intent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.Get(id); !ok {
		return ErrNotFound
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set file of item %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeIntent(ctx, tx, id, intent); err != nil {
		return fmt.Errorf("set file of item %d: %w", id, err)
	}
	if err := stampItem(ctx, tx, id, c.now()); err != nil {
		return fmt.Errorf("set file of item %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set file of item %d: %w", id, err)
	}
	_, err = c.publish(ctx, id)
	return err
}

// updateChoice sets one column of an Item and, when clearIntent is set, forgets
// its chosen file in the same transaction.
func (c *Catalog) updateChoice(ctx context.Context, id int64, statement, value string, clearIntent bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, statement, value, id)
	if err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	if clearIntent {
		if err := writeIntent(ctx, tx, id, nil); err != nil {
			return fmt.Errorf("update item %d: %w", id, err)
		}
	}
	if err := stampItem(ctx, tx, id, c.now()); err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	_, err = c.publish(ctx, id)
	return err
}

// updateSynced is updateItem for a column that syncing shares, so the Item is
// marked as changed now.
func (c *Catalog) updateSynced(ctx context.Context, id int64, statement string, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, statement, value, id)
	if err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	if err := stampItem(ctx, tx, id, c.now()); err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	_, err = c.publish(ctx, id)
	return err
}

// updateItem runs a statement that sets one column of an Item that syncing
// does not share, taking the value and the id, and publishes the result.
func (c *Catalog) updateItem(ctx context.Context, id int64, statement string, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, err := c.db.ExecContext(ctx, statement, value, id)
	if err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	_, err = c.publish(ctx, id)
	return err
}

// publish reloads one Item from the database into the cell. The caller holds mu.
func (c *Catalog) publish(ctx context.Context, id int64) (Entry, error) {
	loaded, err := c.load(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if len(loaded) == 0 {
		return Entry{}, ErrNotFound
	}
	entry := loaded[0]
	c.entries.Update(func(entries *[]Entry) {
		next := slices.Clone(*entries)
		if i := slices.IndexFunc(next, func(e Entry) bool { return e.Item.ID == id }); i >= 0 {
			next[i] = entry
		} else {
			next = append(next, entry)
		}
		*entries = next
	})
	return entry, nil
}

// cacheable keeps the newest releases and bounds their notes.
func cacheable(releases []source.Release) []source.Release {
	kept := slices.Clone(releases[:min(len(releases), maxReleases)])
	for i := range kept {
		kept[i].Notes = truncate(kept[i].Notes, maxNotes)
	}
	return kept
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// execer is a database or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func marshalTopics(topics []string) (string, error) {
	if topics == nil {
		topics = []string{}
	}
	encoded, err := json.Marshal(topics)
	if err != nil {
		return "", fmt.Errorf("encode topics: %w", err)
	}
	return string(encoded), nil
}

func insertItem(ctx context.Context, db execer, it Item) (int64, error) {
	topics, err := marshalTopics(it.Topics)
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO items (
		source, owner, repo, lane, name, description, avatar_url, language, topics, stars, metadata_fetched,
		include_prereleases, kind, install_method, install_folder, link_guessed, etag, last_checked, last_error_kind,
		last_error, added_at, category, group_id, modified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.Source, it.Owner, it.Repo, it.Lane, it.Name, it.Description, it.AvatarURL, it.Language, topics,
		it.Stars, it.MetadataFetched, it.IncludePrereleases, string(it.Kind), string(it.Method), it.InstallFolder, it.LinkGuessed,
		it.ETag, unix(it.LastChecked), string(it.LastFailure), it.LastFailureText, unix(it.AddedAt),
		string(it.Category), it.GroupID, milli(it.ModifiedAt))
	if err != nil {
		return 0, fmt.Errorf("insert item: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert item: %w", err)
	}
	return id, nil
}

func replaceReleases(ctx context.Context, db execer, id int64, releases []source.Release) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM releases WHERE item_id = ?`, id); err != nil {
		return fmt.Errorf("replace releases: %w", err)
	}
	for position, r := range releases {
		assets, err := json.Marshal(r.Assets)
		if err != nil {
			return fmt.Errorf("encode assets of %s: %w", r.Tag, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO releases
			(item_id, position, tag, name, notes, published_at, prerelease, url, assets)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, position, r.Tag, r.Name, r.Notes, unix(r.Published), r.Prerelease, r.URL, string(assets)); err != nil {
			return fmt.Errorf("replace releases: %w", err)
		}
	}
	return nil
}

// writeAdoption records software the user already has. A blank tag records
// nothing, as there is no version to say they have.
func writeAdoption(ctx context.Context, db execer, id int64, tag string, at time.Time, rec *install.Record) error {
	switch {
	case tag == "":
		return nil
	case rec != nil:
		return writeInstall(ctx, db, id, tag, at, *rec)
	}
	return upsertInstall(ctx, db, id, tag, at)
}

func upsertInstall(ctx context.Context, db execer, id int64, tag string, at time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO installs (item_id, tag, installed_at) VALUES (?, ?, ?)
		ON CONFLICT (item_id) DO UPDATE SET tag = excluded.tag, installed_at = excluded.installed_at`,
		id, tag, unix(at))
	if err != nil {
		return fmt.Errorf("record install: %w", err)
	}
	return nil
}

// writeIntent stores an Item's chosen file, or forgets it when intent is nil.
func writeIntent(ctx context.Context, db execer, id int64, intent *selection.Intent) error {
	if intent == nil {
		if _, err := db.ExecContext(ctx, `DELETE FROM selection_intents WHERE item_id = ?`, id); err != nil {
			return fmt.Errorf("forget chosen file: %w", err)
		}
		return nil
	}
	members, err := json.Marshal(append([]string{}, intent.ArchiveMembers...))
	if err != nil {
		return fmt.Errorf("encode chosen programs: %w", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO selection_intents
		(item_id, product, artifact_kind, architecture, abi, cpu_variant, variant, archive_members, explicit)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (item_id) DO UPDATE SET product = excluded.product, artifact_kind = excluded.artifact_kind,
		architecture = excluded.architecture, abi = excluded.abi, cpu_variant = excluded.cpu_variant,
		variant = excluded.variant, archive_members = excluded.archive_members, explicit = excluded.explicit`,
		id, intent.Product, string(intent.Kind), intent.Arch, intent.ABI, intent.CPUVariant, intent.Variant,
		string(members), intent.Explicit)
	if err != nil {
		return fmt.Errorf("store chosen file: %w", err)
	}
	return nil
}

// load reads one Item, or every Item when id is 0, with releases and installs.
// Each query finishes before the next starts: the pool has one connection.
func (c *Catalog) load(ctx context.Context, id int64) ([]Entry, error) {
	items, err := c.loadItems(ctx, id)
	if err != nil {
		return nil, err
	}
	releases, err := c.loadReleases(ctx, id)
	if err != nil {
		return nil, err
	}
	installs, err := c.loadInstalls(ctx, id)
	if err != nil {
		return nil, err
	}
	intents, err := c.loadIntents(ctx, id)
	if err != nil {
		return nil, err
	}
	policy := *c.policy.Load()
	entries := make([]Entry, len(items))
	for i, it := range items {
		entries[i] = Entry{
			Item: it, Releases: releases[it.ID], Install: installs[it.ID], Intent: intents[it.ID], Policy: policy,
		}.withOffer()
	}
	return entries, nil
}

func (c *Catalog) loadItems(ctx context.Context, id int64) ([]Item, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id, source, owner, repo, lane, name, description, avatar_url,
		language, topics, stars, metadata_fetched, include_prereleases, kind, install_method, hold_updates,
		trust_unsigned, skipped_version, install_folder, install_error, installer_args, install_for_everyone, repo_missing,
		auto_update, notified_version, link_guessed, etag, last_checked, last_error_kind,
		last_error, added_at, category, group_id, favorite, hidden, note, tags, modified_at
		FROM items WHERE ? = 0 OR id = ? ORDER BY id`, id, id)
	if err != nil {
		return nil, fmt.Errorf("load items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []Item
	for rows.Next() {
		var it Item
		var topics, tags, failure, kind, method, categoryKey string
		var checked, added, modified int64
		if err := rows.Scan(&it.ID, &it.Source, &it.Owner, &it.Repo, &it.Lane, &it.Name, &it.Description,
			&it.AvatarURL, &it.Language, &topics, &it.Stars, &it.MetadataFetched, &it.IncludePrereleases, &kind, &method,
			&it.HoldUpdates, &it.TrustUnsigned, &it.SkippedVersion, &it.InstallFolder, &it.InstallError, &it.InstallerArgs,
			&it.InstallForEveryone, &it.RepoMissing, &it.AutoUpdate, &it.NotifiedVersion, &it.LinkGuessed, &it.ETag,
			&checked, &failure, &it.LastFailureText, &added, &categoryKey, &it.GroupID, &it.Favorite, &it.Hidden,
			&it.Note, &tags, &modified); err != nil {
			return nil, fmt.Errorf("load items: %w", err)
		}
		if err := json.Unmarshal([]byte(topics), &it.Topics); err != nil {
			return nil, fmt.Errorf("decode topics of %s/%s: %w", it.Owner, it.Repo, err)
		}
		if err := json.Unmarshal([]byte(tags), &it.Tags); err != nil {
			return nil, fmt.Errorf("decode tags of %s/%s: %w", it.Owner, it.Repo, err)
		}
		it.Category = category.Key(categoryKey)
		if it.Category == "" {
			// An Item added before categories existed has none stored yet; its
			// metadata is not fetched again, so it is worked out here.
			it.Category = category.Of(it.Repo, it.Description, it.Topics)
		}
		it.LastChecked, it.LastFailure, it.AddedAt, it.ModifiedAt = fromUnix(checked), FailureKind(failure), fromUnix(added), fromMilli(modified)
		it.Kind, it.Method = selection.ItemKind(kind), selection.Method(method)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load items: %w", err)
	}
	return items, nil
}

func (c *Catalog) loadReleases(ctx context.Context, id int64) (map[int64][]source.Release, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT item_id, tag, name, notes, published_at, prerelease, url, assets
		FROM releases WHERE ? = 0 OR item_id = ? ORDER BY item_id, position`, id, id)
	if err != nil {
		return nil, fmt.Errorf("load releases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	releases := map[int64][]source.Release{}
	for rows.Next() {
		var item, published int64
		var r source.Release
		var assets string
		if err := rows.Scan(&item, &r.Tag, &r.Name, &r.Notes, &published, &r.Prerelease, &r.URL, &assets); err != nil {
			return nil, fmt.Errorf("load releases: %w", err)
		}
		if err := json.Unmarshal([]byte(assets), &r.Assets); err != nil {
			return nil, fmt.Errorf("decode assets of %s: %w", r.Tag, err)
		}
		r.Published = fromUnix(published)
		releases[item] = append(releases[item], r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load releases: %w", err)
	}
	return releases, nil
}

func (c *Catalog) loadInstalls(ctx context.Context, id int64) (map[int64]*Install, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT item_id, tag, installed_at, method, asset_name, folder, main_exe,
		dedicated, shortcut, sha256, uninstall_key, package_name, scope, entry_version FROM installs
		WHERE ? = 0 OR item_id = ?`, id, id)
	if err != nil {
		return nil, fmt.Errorf("load installs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	installs := map[int64]*Install{}
	for rows.Next() {
		var item, at int64
		var method, scope string
		var in Install
		if err := rows.Scan(&item, &in.Tag, &at, &method, &in.Asset, &in.Folder, &in.MainExe, &in.Dedicated,
			&in.Shortcut, &in.SHA256, &in.UninstallKey, &in.Package, &scope, &in.EntryVersion); err != nil {
			return nil, fmt.Errorf("load installs: %w", err)
		}
		in.At, in.Method, in.Scope = fromUnix(at), install.Method(method), platform.Scope(scope)
		installs[item] = &in
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load installs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("load installs: %w", err)
	}
	return installs, c.loadCommands(ctx, id, installs)
}

// loadCommands fills in the commands of the installs of Tools.
func (c *Catalog) loadCommands(ctx context.Context, id int64, installs map[int64]*Install) error {
	rows, err := c.db.QueryContext(ctx, `SELECT item_id, command FROM install_files
		WHERE command <> '' AND (? = 0 OR item_id = ?) ORDER BY item_id, path`, id, id)
	if err != nil {
		return fmt.Errorf("load commands: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item int64
		var command string
		if err := rows.Scan(&item, &command); err != nil {
			return fmt.Errorf("load commands: %w", err)
		}
		if in := installs[item]; in != nil {
			in.Commands = append(in.Commands, command)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load commands: %w", err)
	}
	return nil
}

func (c *Catalog) loadIntents(ctx context.Context, id int64) (map[int64]*selection.Intent, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT item_id, product, artifact_kind, architecture, abi, cpu_variant,
		variant, archive_members, explicit FROM selection_intents WHERE ? = 0 OR item_id = ?`, id, id)
	if err != nil {
		return nil, fmt.Errorf("load chosen files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	intents := map[int64]*selection.Intent{}
	for rows.Next() {
		var item int64
		var kind, members string
		var in selection.Intent
		if err := rows.Scan(&item, &in.Product, &kind, &in.Arch, &in.ABI, &in.CPUVariant, &in.Variant,
			&members, &in.Explicit); err != nil {
			return nil, fmt.Errorf("load chosen files: %w", err)
		}
		if err := json.Unmarshal([]byte(members), &in.ArchiveMembers); err != nil {
			return nil, fmt.Errorf("decode chosen programs of item %d: %w", item, err)
		}
		if len(in.ArchiveMembers) == 0 {
			in.ArchiveMembers = nil
		}
		in.Kind = selection.ArtifactKind(kind)
		intents[item] = &in
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load chosen files: %w", err)
	}
	return intents, nil
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
