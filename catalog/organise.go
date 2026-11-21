package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// SetFavorite lists the Item before the others, or stops doing so.
func (c *Catalog) SetFavorite(ctx context.Context, id int64, favorite bool) error {
	return c.updateSynced(ctx, id, `UPDATE items SET favorite = ? WHERE id = ?`, favorite)
}

// SetHidden takes the Item out of the Library's views, counts, update checks
// and notifications, or brings it back.
func (c *Catalog) SetHidden(ctx context.Context, id int64, hidden bool) error {
	return c.updateSynced(ctx, id, `UPDATE items SET hidden = ? WHERE id = ?`, hidden)
}

// SetNote keeps the user's note about an Item.
func (c *Catalog) SetNote(ctx context.Context, id int64, note string) error {
	return c.updateSynced(ctx, id, `UPDATE items SET note = ? WHERE id = ?`, strings.TrimSpace(note))
}

// SetTags keeps the user's tags for an Item, without blanks or repeats.
func (c *Catalog) SetTags(ctx context.Context, id int64, tags []string) error {
	encoded, err := json.Marshal(cleanTags(tags))
	if err != nil {
		return fmt.Errorf("encode tags: %w", err)
	}
	return c.updateSynced(ctx, id, `UPDATE items SET tags = ? WHERE id = ?`, string(encoded))
}

// SetGroup puts an Item in one of the user's groups; 0 returns it to the group
// its category gives it. It returns ErrGroupNotFound for a group that is not
// there.
func (c *Catalog) SetGroup(ctx context.Context, id, groupID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if groupID != 0 && !c.hasGroup(groupID) {
		return ErrGroupNotFound
	}
	result, err := c.db.ExecContext(ctx, `UPDATE items SET group_id = ?, modified_at = ? WHERE id = ?`, groupID, milli(c.now()), id)
	if err != nil {
		return fmt.Errorf("update item %d: %w", id, err)
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	_, err = c.publish(ctx, id)
	return err
}

// ParseTags splits text the user typed into tags: comma separated, trimmed,
// without blanks or repeats (ignoring case).
func ParseTags(text string) []string { return cleanTags(strings.Split(text, ",")) }

// FormatTags is the text ParseTags reads back.
func FormatTags(tags []string) string { return strings.Join(tags, ", ") }

func cleanTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !slices.ContainsFunc(out, func(kept string) bool { return strings.EqualFold(kept, tag) }) {
			out = append(out, tag)
		}
	}
	return out
}
