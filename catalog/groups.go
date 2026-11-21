package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/auron-labs/mygo-framework/state"
)

var (
	// ErrGroupName means a group's name is empty.
	ErrGroupName = errors.New("group needs a name")
	// ErrGroupExists means another group already has the name, ignoring case.
	ErrGroupExists = errors.New("a group with that name already exists")
	// ErrGroupNotFound means there is no group with the id.
	ErrGroupNotFound = errors.New("group not found")
)

// Group is a collection the user made. Items refer to it by id, so renaming it
// touches only this row.
type Group struct {
	ID   int64
	Name string
	// CreatedAt is when the group was made or last renamed, zero for a group
	// from before the Library kept the time.
	CreatedAt time.Time
}

// Groups is every group, by name, as a cell views read and subscribe to.
func (c *Catalog) Groups() *state.Cell[[]Group] { return c.groups }

func (c *Catalog) hasGroup(id int64) bool {
	return slices.ContainsFunc(c.groups.Get(), func(g Group) bool { return g.ID == id })
}

// CreateGroup makes a group. It returns ErrGroupName for a blank name and
// ErrGroupExists when another group has the name.
func (c *Catalog) CreateGroup(ctx context.Context, name string) (Group, error) {
	name = strings.TrimSpace(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ValidateGroupName(name, 0); err != nil {
		return Group{}, err
	}
	created := c.now()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, fmt.Errorf("create group %q: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO groups (name, created_at) VALUES (?, ?)`, name, milli(created))
	if err != nil {
		return Group{}, fmt.Errorf("create group %q: %w", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Group{}, fmt.Errorf("create group %q: %w", name, err)
	}
	if err := forgetRemoval(ctx, tx, groupKey(name)); err != nil {
		return Group{}, fmt.Errorf("create group %q: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return Group{}, fmt.Errorf("create group %q: %w", name, err)
	}
	g := Group{ID: id, Name: name, CreatedAt: fromMilli(milli(created))}
	c.setGroups(func(groups []Group) []Group { return append(groups, g) })
	return g, nil
}

// RenameGroup changes a group's name. Its Items stay in it. A name that differs
// only in case from the group's own is fine.
func (c *Catalog) RenameGroup(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasGroup(id) {
		return ErrGroupNotFound
	}
	if err := c.ValidateGroupName(name, id); err != nil {
		return err
	}
	renamed := c.now()
	old := c.groupByID(id)
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rename group %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE groups SET name = ?, created_at = ? WHERE id = ?`, name, milli(renamed), id); err != nil {
		return fmt.Errorf("rename group %d: %w", id, err)
	}
	// Other devices know the group by its name, so the old name is removed and
	// the Items in the group are marked as changed to carry the new one.
	if groupKey(old.Name) != groupKey(name) {
		if err := recordRemoval(ctx, tx, groupKey(old.Name), renamed); err != nil {
			return fmt.Errorf("rename group %d: %w", id, err)
		}
		if err := forgetRemoval(ctx, tx, groupKey(name)); err != nil {
			return fmt.Errorf("rename group %d: %w", id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE items SET modified_at = ? WHERE group_id = ?`, milli(renamed), id); err != nil {
		return fmt.Errorf("rename group %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rename group %d: %w", id, err)
	}
	c.setGroups(func(groups []Group) []Group {
		i := slices.IndexFunc(groups, func(g Group) bool { return g.ID == id })
		groups[i].Name, groups[i].CreatedAt = name, fromMilli(milli(renamed))
		return groups
	})
	c.touchGroupItems(id, renamed)
	return nil
}

// DeleteGroup removes a group and puts its Items back in the groups their
// categories give them. Nothing is uninstalled.
func (c *Catalog) DeleteGroup(ctx context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasGroup(id) {
		return ErrGroupNotFound
	}
	removed := c.now()
	old := c.groupByID(id)
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete group %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := c.removeGroup(ctx, tx, old, removed); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete group %d: %w", id, err)
	}
	c.forgetGroup(id, removed)
	return nil
}

// removeGroup deletes a group's row and puts its Items back in their
// categories, marking them as changed at the time given, and records the
// removal for syncing.
func (c *Catalog) removeGroup(ctx context.Context, tx *sql.Tx, g Group, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE items SET group_id = 0, modified_at = ? WHERE group_id = ?`, milli(at), g.ID); err != nil {
		return fmt.Errorf("delete group %d: %w", g.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, g.ID); err != nil {
		return fmt.Errorf("delete group %d: %w", g.ID, err)
	}
	if err := recordRemoval(ctx, tx, groupKey(g.Name), at); err != nil {
		return fmt.Errorf("delete group %d: %w", g.ID, err)
	}
	return nil
}

// forgetGroup drops a deleted group from the cells, and moves its Items back
// to their categories.
func (c *Catalog) forgetGroup(id int64, at time.Time) {
	c.setGroups(func(groups []Group) []Group {
		return slices.DeleteFunc(groups, func(g Group) bool { return g.ID == id })
	})
	c.entries.Update(func(entries *[]Entry) {
		next := slices.Clone(*entries)
		for i := range next {
			if next[i].Item.GroupID == id {
				next[i].Item.GroupID, next[i].Item.ModifiedAt = 0, fromMilli(milli(at))
			}
		}
		*entries = next
	})
}

// touchGroupItems marks the Items in a group as changed, in the cell.
func (c *Catalog) touchGroupItems(id int64, at time.Time) {
	c.entries.Update(func(entries *[]Entry) {
		next := slices.Clone(*entries)
		for i := range next {
			if next[i].Item.GroupID == id {
				next[i].Item.ModifiedAt = fromMilli(milli(at))
			}
		}
		*entries = next
	})
}

func (c *Catalog) groupByID(id int64) Group {
	for _, g := range c.groups.Get() {
		if g.ID == id {
			return g
		}
	}
	return Group{}
}

// ValidateGroupName reports why name cannot be the name of a group: it is
// blank (ErrGroupName) or another group has it (ErrGroupExists). self is the
// group being renamed, which may keep its own name, or 0.
func (c *Catalog) ValidateGroupName(name string, self int64) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrGroupName
	}
	for _, g := range c.groups.Get() {
		if g.ID != self && strings.EqualFold(g.Name, name) {
			return ErrGroupExists
		}
	}
	return nil
}

// setGroups publishes the groups change makes, kept in name order. change gets
// a copy it may modify.
func (c *Catalog) setGroups(change func([]Group) []Group) {
	c.groups.Update(func(groups *[]Group) {
		next := change(slices.Clone(*groups))
		slices.SortFunc(next, compareGroups)
		*groups = next
	})
}

func compareGroups(a, b Group) int {
	if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}

func (c *Catalog) loadGroups(ctx context.Context) ([]Group, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id, name, created_at FROM groups`)
	if err != nil {
		return nil, fmt.Errorf("load groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var groups []Group
	for rows.Next() {
		var g Group
		var created int64
		if err := rows.Scan(&g.ID, &g.Name, &created); err != nil {
			return nil, fmt.Errorf("load groups: %w", err)
		}
		g.CreatedAt = fromMilli(created)
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load groups: %w", err)
	}
	slices.SortFunc(groups, compareGroups)
	return groups, nil
}
