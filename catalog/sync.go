package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aaronflorey/release-sync/selection"
	libsync "github.com/aaronflorey/release-sync/sync"
)

// SyncOutcome is what one sync did.
type SyncOutcome struct {
	// Merged is everything this device and the shared file know, the Library as
	// every device will have it once they have synced.
	Merged libsync.Document
	// Pushed says this device rewrote the shared file.
	Pushed bool
	// FirstSync says there was no shared file yet.
	FirstSync bool
	// Applied is what changed in this Library.
	Applied SyncApplied
}

// SyncApplied is what a sync changed in the Library.
type SyncApplied struct {
	// Added are the new Items, which have no releases yet.
	Added []int64
	// Updated and Removed count the Items, saved repositories and groups
	// replaced and removed.
	Updated, Removed int
}

// Sync runs one sync round: it shares this Library through the transport,
// merges it with what the other devices shared, and applies what they changed.
// What the user changed while the round ran is kept, and shared by the next one.
func (c *Catalog) Sync(ctx context.Context, t libsync.Transport, keys libsync.Keys, self libsync.Device) (SyncOutcome, error) {
	sent, err := c.SyncDocument(ctx)
	if err != nil {
		return SyncOutcome{}, err
	}
	result, err := libsync.Round(ctx, t, sent, keys, self, c.now())
	if err != nil {
		return SyncOutcome{}, fmt.Errorf("sync the library: %w", err)
	}
	outcome := SyncOutcome{Merged: result.Merged, Pushed: result.Pushed, FirstSync: result.FirstSync}
	if !result.Changed {
		return outcome, nil
	}
	current, err := c.SyncDocument(ctx)
	if err != nil {
		return outcome, err
	}
	if outcome.Applied, err = c.ApplySync(ctx, libsync.Plan(sent, result.Merged, current)); err != nil {
		return outcome, err
	}
	return outcome, nil
}

// SyncDocument is what syncing shares of the Library, as a document: the
// Items, saved repositories and groups, and the removals not yet forgotten. It
// has no devices; a round adds this one.
func (c *Catalog) SyncDocument(ctx context.Context) (libsync.Document, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	doc := libsync.Document{Format: libsync.Format, Version: libsync.Version, Tombstones: map[string]time.Time{}}
	groups := c.groups.Get()
	names := make(map[int64]string, len(groups))
	for _, g := range groups {
		names[g.ID] = g.Name
		doc.Groups = append(doc.Groups, libsync.Group{Name: g.Name, CreatedAt: g.CreatedAt.UTC()})
	}
	for _, e := range c.entries.Get() {
		doc.Items = append(doc.Items, syncItem(e, names[e.Item.GroupID]))
	}
	for _, t := range c.tryLater.Get() {
		doc.TryLater = append(doc.TryLater, syncTryLater(t))
	}
	rows, err := c.db.QueryContext(ctx, `SELECT key, deleted_at FROM sync_tombstones`)
	if err != nil {
		return libsync.Document{}, fmt.Errorf("read the removals to sync: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		var at int64
		if err := rows.Scan(&key, &at); err != nil {
			return libsync.Document{}, fmt.Errorf("read the removals to sync: %w", err)
		}
		doc.Tombstones[key] = fromMilli(at).UTC()
	}
	if err := rows.Err(); err != nil {
		return libsync.Document{}, fmt.Errorf("read the removals to sync: %w", err)
	}
	return doc, nil
}

// syncItem is the part of an Entry that is shared. The file chosen for an Item
// is shared only when the user chose it: one the best match gave is what this
// PC found, which another PC may not.
func syncItem(e Entry, group string) libsync.Item {
	it := e.Item
	out := libsync.Item{
		Source: it.Source, Owner: it.Owner, Repo: it.Repo, Lane: it.Lane, Name: it.Name,
		Kind: string(it.Kind), Method: string(it.Method), Group: group,
		Favorite: it.Favorite, Hidden: it.Hidden, IncludePrereleases: it.IncludePrereleases, HoldUpdates: it.HoldUpdates,
		AutoUpdate: it.AutoUpdate, TrustUnsigned: it.TrustUnsigned, SkippedVersion: it.SkippedVersion,
		InstallerArgs: it.InstallerArgs, Note: it.Note, Tags: slices.Clone(it.Tags),
		AddedAt: it.AddedAt.UTC(), ModifiedAt: it.ModifiedAt.UTC(),
	}
	if e.Intent != nil && e.Intent.Explicit {
		out.Intent = &libsync.Intent{
			Product: e.Intent.Product, Kind: string(e.Intent.Kind), Arch: e.Intent.Arch, ABI: e.Intent.ABI,
			CPUVariant: e.Intent.CPUVariant, Variant: e.Intent.Variant, ArchiveMembers: slices.Clone(e.Intent.ArchiveMembers),
			Explicit: true,
		}
	}
	return out
}

func syncTryLater(t TryLater) libsync.TryLaterEntry {
	return libsync.TryLaterEntry{
		Source: t.Source, Owner: t.Owner, Repo: t.Repo, Description: t.Description, Language: t.Language, Stars: t.Stars,
		AvatarURL: t.AvatarURL, Topics: slices.Clone(t.Topics), Note: t.Note,
		SavedAt: t.SavedAt.UTC(), ModifiedAt: t.ModifiedAt.UTC(),
	}
}

// ApplySync applies the changes a round worked out. Anything the user changed
// since the round started is left alone: a replacement is skipped for an Item
// that is newer here, and a removal for one that is no longer as it was sent.
func (c *Catalog) ApplySync(ctx context.Context, changes libsync.Changes) (SyncApplied, error) {
	if changes.Empty() {
		return SyncApplied{}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return SyncApplied{}, fmt.Errorf("apply the synced changes: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	a := applier{c: c, ctx: ctx, tx: tx, groups: slices.Clone(c.groups.Get())}
	if err := a.run(changes); err != nil {
		return SyncApplied{}, fmt.Errorf("apply the synced changes: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SyncApplied{}, fmt.Errorf("apply the synced changes: %w", err)
	}
	if err := c.reloadAll(ctx); err != nil {
		return SyncApplied{}, err
	}
	return a.applied, nil
}

// reloadAll reads the whole Library again into the cells. The caller holds mu.
func (c *Catalog) reloadAll(ctx context.Context) error {
	entries, err := c.load(ctx, 0)
	if err != nil {
		return err
	}
	saved, err := c.loadTryLater(ctx)
	if err != nil {
		return err
	}
	groups, err := c.loadGroups(ctx)
	if err != nil {
		return err
	}
	c.entries.Set(entries)
	c.tryLater.Set(saved)
	c.groups.Set(groups)
	return nil
}

// applier applies one set of changes inside a transaction.
type applier struct {
	c       *Catalog
	ctx     context.Context //nolint:containedctx // Lives for one call.
	tx      *sql.Tx
	groups  []Group
	applied SyncApplied
}

func (a *applier) run(changes libsync.Changes) error {
	steps := []func(libsync.Changes) error{
		a.upsertGroups, a.upsertItems, a.upsertTryLater, a.removeItems, a.removeTryLater, a.removeGroups, a.recordRemovals,
	}
	for _, step := range steps {
		if err := step(changes); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) groupID(name string) int64 {
	if i := slices.IndexFunc(a.groups, func(g Group) bool { return strings.EqualFold(g.Name, name) }); i >= 0 {
		return a.groups[i].ID
	}
	return 0
}

func (a *applier) upsertGroups(changes libsync.Changes) error {
	for _, g := range changes.Groups {
		created := fromMilli(milli(g.CreatedAt))
		if i := slices.IndexFunc(a.groups, func(have Group) bool { return groupKey(have.Name) == g.Key() }); i >= 0 {
			if a.groups[i].CreatedAt.After(created) {
				continue
			}
			if _, err := a.tx.ExecContext(a.ctx, `UPDATE groups SET name = ?, created_at = ? WHERE id = ?`, g.Name, milli(created), a.groups[i].ID); err != nil {
				return fmt.Errorf("update group %q: %w", g.Name, err)
			}
			a.groups[i].Name, a.groups[i].CreatedAt = g.Name, created
			a.applied.Updated++
			continue
		}
		result, err := a.tx.ExecContext(a.ctx, `INSERT INTO groups (name, created_at) VALUES (?, ?)`, g.Name, milli(created))
		if err != nil {
			return fmt.Errorf("add group %q: %w", g.Name, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("add group %q: %w", g.Name, err)
		}
		a.groups = append(a.groups, Group{ID: id, Name: g.Name, CreatedAt: created})
		a.applied.Updated++
	}
	return nil
}

func (a *applier) removeGroups(changes libsync.Changes) error {
	for _, r := range changes.RemoveGroups {
		i := slices.IndexFunc(a.groups, func(g Group) bool { return groupKey(g.Name) == r.Key })
		if i < 0 || !a.groups[i].CreatedAt.UTC().Equal(r.Stamp) {
			continue
		}
		id := a.groups[i].ID
		if _, err := a.tx.ExecContext(a.ctx, `UPDATE items SET group_id = 0 WHERE group_id = ?`, id); err != nil {
			return fmt.Errorf("remove group %d: %w", id, err)
		}
		if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM groups WHERE id = ?`, id); err != nil {
			return fmt.Errorf("remove group %d: %w", id, err)
		}
		a.groups = slices.Delete(a.groups, i, i+1)
		a.applied.Removed++
	}
	return nil
}

func (a *applier) upsertItems(changes libsync.Changes) error {
	names := make(map[int64]string, len(a.groups))
	for _, g := range a.groups {
		names[g.ID] = g.Name
	}
	for _, m := range changes.Items {
		tags, err := json.Marshal(cleanTags(m.Tags))
		if err != nil {
			return fmt.Errorf("encode tags of %s/%s: %w", m.Owner, m.Repo, err)
		}
		local, found := a.c.findKey(m.Key())
		if found && syncItem(local, names[local.Item.GroupID]).Stamp().After(m.Stamp()) {
			continue
		}
		kind, method := syncedKind(m.Kind), syncedMethod(m.Method)
		var id int64
		if found {
			id = local.Item.ID
			_, err = a.tx.ExecContext(a.ctx, `UPDATE items SET name = ?, kind = ?, install_method = ?, group_id = ?, favorite = ?,
				hidden = ?, include_prereleases = ?, hold_updates = ?, auto_update = ?, trust_unsigned = ?, skipped_version = ?,
				installer_args = ?, note = ?, tags = ?, added_at = ?, modified_at = ? WHERE id = ?`,
				m.Name, kind, method, a.groupID(m.Group), m.Favorite, m.Hidden, m.IncludePrereleases, m.HoldUpdates, m.AutoUpdate,
				m.TrustUnsigned, m.SkippedVersion, m.InstallerArgs, m.Note, string(tags), unix(m.AddedAt), milli(m.ModifiedAt), id)
			a.applied.Updated++
		} else {
			var result sql.Result
			result, err = a.tx.ExecContext(a.ctx, `INSERT INTO items (source, owner, repo, lane, name, kind, install_method, group_id,
				favorite, hidden, include_prereleases, hold_updates, auto_update, trust_unsigned, skipped_version, installer_args,
				note, tags, added_at, modified_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				m.Source, m.Owner, m.Repo, m.Lane, m.Name, kind, method, a.groupID(m.Group), m.Favorite, m.Hidden,
				m.IncludePrereleases, m.HoldUpdates, m.AutoUpdate, m.TrustUnsigned, m.SkippedVersion, m.InstallerArgs, m.Note,
				string(tags), unix(m.AddedAt), milli(m.ModifiedAt))
			if err == nil {
				id, err = result.LastInsertId()
			}
			a.applied.Added = append(a.applied.Added, id)
		}
		if err != nil {
			return fmt.Errorf("apply %s/%s: %w", m.Owner, m.Repo, err)
		}
		if err := a.applyIntent(id, found, local, m.Intent); err != nil {
			return fmt.Errorf("apply %s/%s: %w", m.Owner, m.Repo, err)
		}
	}
	return nil
}

// syncedKind and syncedMethod read a choice from a document written by
// another device: one this version does not know is left to be detected.
func syncedKind(kind string) selection.ItemKind {
	switch k := selection.ItemKind(kind); k {
	case selection.ItemApp, selection.ItemTool:
		return k
	case "":
	}
	return ""
}

func syncedMethod(method string) selection.Method {
	switch m := selection.Method(method); m {
	case selection.MethodInstaller, selection.MethodPortable:
		return m
	case selection.MethodAutomatic:
	}
	return selection.MethodAutomatic
}

// applyIntent sets the file chosen for an Item to the shared one. Without a
// shared one, a file the user chose here is forgotten, and one the best match
// gave is kept.
func (a *applier) applyIntent(id int64, found bool, local Entry, shared *libsync.Intent) error {
	if shared == nil {
		if found && local.Intent != nil && local.Intent.Explicit {
			return writeIntent(a.ctx, a.tx, id, nil)
		}
		return nil
	}
	return writeIntent(a.ctx, a.tx, id, &selection.Intent{
		Product: shared.Product, Kind: selection.ArtifactKind(shared.Kind), Arch: shared.Arch, ABI: shared.ABI,
		CPUVariant: shared.CPUVariant, Variant: shared.Variant, ArchiveMembers: shared.ArchiveMembers, Explicit: shared.Explicit,
	})
}

func (a *applier) removeItems(changes libsync.Changes) error {
	names := make(map[int64]string, len(a.groups))
	for _, g := range a.groups {
		names[g.ID] = g.Name
	}
	for _, r := range changes.RemoveItems {
		local, found := a.c.findKey(r.Key)
		if !found || !syncItem(local, names[local.Item.GroupID]).Stamp().Equal(r.Stamp) {
			continue
		}
		if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM items WHERE id = ?`, local.Item.ID); err != nil {
			return fmt.Errorf("remove %s: %w", local.FullName(), err)
		}
		a.applied.Removed++
	}
	return nil
}

func (a *applier) upsertTryLater(changes libsync.Changes) error {
	for _, m := range changes.TryLater {
		topics, err := marshalTopics(m.Topics)
		if err != nil {
			return err
		}
		local, found := a.c.findTryLaterKey(m.Key())
		if found && syncTryLater(local).Stamp().After(m.Stamp()) {
			continue
		}
		if found {
			_, err = a.tx.ExecContext(a.ctx, `UPDATE try_later SET description = ?, language = ?, stars = ?, avatar_url = ?,
				topics = ?, note = ?, saved_at = ?, modified_at = ? WHERE id = ?`,
				m.Description, m.Language, m.Stars, m.AvatarURL, topics, m.Note, unix(m.SavedAt), milli(m.ModifiedAt), local.ID)
		} else {
			_, err = a.tx.ExecContext(a.ctx, `INSERT INTO try_later (source, owner, repo, description, language, stars, avatar_url,
				topics, note, saved_at, modified_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				m.Source, m.Owner, m.Repo, m.Description, m.Language, m.Stars, m.AvatarURL, topics, m.Note,
				unix(m.SavedAt), milli(m.ModifiedAt))
		}
		if err != nil {
			return fmt.Errorf("apply saved %s/%s: %w", m.Owner, m.Repo, err)
		}
		a.applied.Updated++
	}
	return nil
}

func (a *applier) removeTryLater(changes libsync.Changes) error {
	for _, r := range changes.RemoveTryLater {
		local, found := a.c.findTryLaterKey(r.Key)
		if !found || !syncTryLater(local).Stamp().Equal(r.Stamp) {
			continue
		}
		if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM try_later WHERE id = ?`, local.ID); err != nil {
			return fmt.Errorf("remove saved %s: %w", local.FullName(), err)
		}
		a.applied.Removed++
	}
	return nil
}

// recordRemovals keeps the removals the round brought and forgets the ones it
// settled.
func (a *applier) recordRemovals(changes libsync.Changes) error {
	for key, at := range changes.Tombstones {
		if _, err := a.tx.ExecContext(a.ctx, `INSERT INTO sync_tombstones (key, deleted_at) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET deleted_at = excluded.deleted_at WHERE excluded.deleted_at > deleted_at`,
			key, milli(at)); err != nil {
			return fmt.Errorf("record the removal of %s: %w", key, err)
		}
	}
	for _, r := range changes.ForgetTombstones {
		if _, err := a.tx.ExecContext(a.ctx, `DELETE FROM sync_tombstones WHERE key = ? AND deleted_at = ?`, r.Key, milli(r.Stamp)); err != nil {
			return fmt.Errorf("forget the removal of %s: %w", r.Key, err)
		}
	}
	return nil
}

// findKey returns the Item a sync key names. The caller holds mu.
func (c *Catalog) findKey(key string) (Entry, bool) {
	for _, e := range c.entries.Get() {
		if itemKey(e.Item) == key {
			return e, true
		}
	}
	return Entry{}, false
}

// findTryLaterKey returns the saved repository a sync key names. The caller holds mu.
func (c *Catalog) findTryLaterKey(key string) (TryLater, bool) {
	for _, t := range c.tryLater.Get() {
		if tryLaterKey(t) == key {
			return t, true
		}
	}
	return TryLater{}, false
}
