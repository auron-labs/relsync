// Package sync keeps a Library in step between devices. It defines the synced
// document, the merge of two copies of it, the optional encryption of its text
// and the places the text can be kept. It knows nothing of the database: the
// catalog turns its state into a Document and applies the Changes a round
// works out.
package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Format marks a file as a synced document.
	Format = "release-sync-library-sync"
	// Version is the version of the document written. A document of a newer
	// version is refused; this format is separate from the database schema and
	// from the file the Import & Export page writes.
	Version = 1
)

// Document is what every device shares: the Library's Items, the saved
// repositories and the groups, the removals that still have to reach every
// device, and the devices that have taken part.
type Document struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated"`
	// WrittenBy is the name of the device that last wrote the document.
	WrittenBy string `json:"writtenBy"`
	Items     []Item `json:"items"`
	// TryLater is the list of saved repositories.
	TryLater []TryLaterEntry `json:"tryLater"`
	Groups   []Group         `json:"groups"`
	// Tombstones are removals by key (see Item.Key), with the time of the
	// removal.
	Tombstones map[string]time.Time `json:"tombstones"`
	// Devices are the devices that have written the document, by id.
	Devices map[string]Device `json:"devices"`
}

// Item is what is shared of one tracked repository: what the user chose for it,
// not what is known of it from its site, and nothing of what is installed.
type Item struct {
	Source string `json:"source"`
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Lane   string `json:"lane,omitempty"`
	// Name is the display name.
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Method string `json:"method,omitempty"`
	// Group is the name of the Item's own group, "" for none.
	Group              string    `json:"group,omitempty"`
	Favorite           bool      `json:"favorite,omitempty"`
	Hidden             bool      `json:"hidden,omitempty"`
	IncludePrereleases bool      `json:"includePrereleases,omitempty"`
	HoldUpdates        bool      `json:"holdUpdates,omitempty"`
	AutoUpdate         bool      `json:"autoUpdate,omitempty"`
	TrustUnsigned      bool      `json:"trustUnsigned,omitempty"`
	SkippedVersion     string    `json:"skippedVersion,omitempty"`
	InstallerArgs      string    `json:"installerArgs,omitempty"`
	Note               string    `json:"note,omitempty"`
	Tags               []string  `json:"tags,omitempty"`
	Intent             *Intent   `json:"intent,omitempty"`
	AddedAt            time.Time `json:"added"`
	// ModifiedAt is when the user last changed the Item; the later of two
	// copies wins as a whole.
	ModifiedAt time.Time `json:"modified"`
}

// Intent is the choice of release file kept for an Item.
type Intent struct {
	Product        string   `json:"product,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	Arch           string   `json:"arch,omitempty"`
	ABI            string   `json:"abi,omitempty"`
	CPUVariant     string   `json:"cpuVariant,omitempty"`
	Variant        string   `json:"variant,omitempty"`
	ArchiveMembers []string `json:"archiveMembers,omitempty"`
	Explicit       bool     `json:"explicit,omitempty"`
}

// TryLaterEntry is a repository saved to look at later, with the snapshot of
// what was known of it when it was saved.
type TryLaterEntry struct {
	Source      string    `json:"source"`
	Owner       string    `json:"owner"`
	Repo        string    `json:"repo"`
	Description string    `json:"description,omitempty"`
	Language    string    `json:"language,omitempty"`
	Stars       int       `json:"stars,omitempty"`
	AvatarURL   string    `json:"avatarUrl,omitempty"`
	Topics      []string  `json:"topics,omitempty"`
	Note        string    `json:"note,omitempty"`
	SavedAt     time.Time `json:"saved"`
	ModifiedAt  time.Time `json:"modified"`
}

// Group is a group the user made. Groups are known by their names, ignoring
// case.
type Group struct {
	Name string `json:"name"`
	// CreatedAt is when the group was made or last renamed, so that a group
	// made again after it was deleted is not deleted again by the older removal.
	CreatedAt time.Time `json:"created"`
}

// Device is one of the user's devices, as the others see it.
type Device struct {
	ID string `json:"id"`
	// Name is what the user calls the device.
	Name string `json:"name"`
	// Platform is the operating system: "Windows", "Linux" or "macOS".
	Platform   string    `json:"platform"`
	AppVersion string    `json:"appVersion"`
	LastSeen   time.Time `json:"lastSeen"`
}

// RepoKey names a repository, ignoring case, on one site.
func RepoKey(site, owner, repo string) string {
	return strings.ToLower(site + ":" + owner + "/" + repo)
}

// Key names the Item, and the tombstone that removes it; empty for an Item
// that names no repository.
func (i Item) Key() string { return repoKey("item:", i.Source, i.Owner, i.Repo) }

// Stamp is the later of when the Item was added and when it was last changed.
func (i Item) Stamp() time.Time { return later(i.AddedAt, i.ModifiedAt) }

// Key names the saved repository, and the tombstone that removes it.
func (t TryLaterEntry) Key() string { return repoKey("later:", t.Source, t.Owner, t.Repo) }

// Stamp is the later of when the repository was saved and when it was last
// changed.
func (t TryLaterEntry) Stamp() time.Time { return later(t.SavedAt, t.ModifiedAt) }

// GroupKey names a group by its name, ignoring case and surrounding spaces.
func GroupKey(name string) string {
	if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
		return "group:" + name
	}
	return ""
}

// Key names the group, and the tombstone that removes it.
func (g Group) Key() string { return GroupKey(g.Name) }

// Stamp is when the group was made or last renamed.
func (g Group) Stamp() time.Time { return g.CreatedAt }

// Key is the device's id.
func (d Device) Key() string { return d.ID }

// Stamp is when the device last wrote the document.
func (d Device) Stamp() time.Time { return d.LastSeen }

func repoKey(prefix, site, owner, repo string) string {
	if site == "" || owner == "" || repo == "" {
		return ""
	}
	return prefix + RepoKey(site, owner, repo)
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Errors a document or the file holding it can have. The sync file is never
// overwritten after any of them, except ErrLocked and ErrWrongPassphrase being
// resolved by the right passphrase.
var (
	// ErrUnreadable means the shared text is not a synced document.
	ErrUnreadable = errors.New("the sync file isn't readable, so nothing was changed")
	// ErrNewerFormat means the shared document was written by a newer version
	// of the application.
	ErrNewerFormat = errors.New("the sync file was made by a newer version of this application")
)

// Parse reads a document from text, plain or already decrypted. It returns
// ErrUnreadable for text that is no synced document and ErrNewerFormat for one
// of a newer version. Entries that name no repository or group are dropped.
func Parse(text string) (Document, error) {
	var d Document
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		return Document{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	if d.Format != Format || d.Version < 1 {
		return Document{}, ErrUnreadable
	}
	if d.Version > Version {
		return Document{}, ErrNewerFormat
	}
	return d.clean(), nil
}

// Encode writes the document as text.
func (d Document) Encode() (string, error) {
	encoded, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode the sync document: %w", err)
	}
	return string(encoded) + "\n", nil
}

// clean drops what cannot be used and gives every collection a value, so that
// equal documents encode equally.
func (d Document) clean() Document {
	out := Document{
		Format: Format, Version: Version, UpdatedAt: d.UpdatedAt.UTC(), WrittenBy: d.WrittenBy,
		Items: []Item{}, TryLater: []TryLaterEntry{}, Groups: []Group{},
		Tombstones: map[string]time.Time{}, Devices: map[string]Device{},
	}
	for _, i := range d.Items {
		if i.Key() != "" {
			out.Items = append(out.Items, i.utc())
		}
	}
	for _, t := range d.TryLater {
		if t.Key() != "" {
			out.TryLater = append(out.TryLater, t.utc())
		}
	}
	for _, g := range d.Groups {
		if g.Key() != "" {
			out.Groups = append(out.Groups, g.utc())
		}
	}
	for key, at := range d.Tombstones {
		out.Tombstones[key] = at.UTC()
	}
	for id, device := range d.Devices {
		device.ID = id
		out.Devices[id] = device.utc()
	}
	return out
}

func (i Item) utc() Item {
	i.AddedAt, i.ModifiedAt = i.AddedAt.UTC(), i.ModifiedAt.UTC()
	if len(i.Tags) == 0 {
		i.Tags = nil
	}
	if i.Intent != nil {
		intent := *i.Intent
		if len(intent.ArchiveMembers) == 0 {
			intent.ArchiveMembers = nil
		}
		i.Intent = &intent
	}
	return i
}

func (t TryLaterEntry) utc() TryLaterEntry {
	t.SavedAt, t.ModifiedAt = t.SavedAt.UTC(), t.ModifiedAt.UTC()
	if len(t.Topics) == 0 {
		t.Topics = nil
	}
	return t
}

func (g Group) utc() Group {
	g.Name, g.CreatedAt = strings.TrimSpace(g.Name), g.CreatedAt.UTC()
	return g
}

func (d Device) utc() Device {
	d.LastSeen = d.LastSeen.UTC()
	return d
}
