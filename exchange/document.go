// Package exchange moves a Library between machines: it writes the Library as
// a file or a list of links, reads such files and other lists of repositories,
// and adds what they hold. Nothing device-local is exported: no install
// records, paths, tokens or cached releases.
package exchange

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

const (
	// Format marks a file as this application's export.
	Format = "release-sync-library"
	// Version is the version of the format written. Files of older versions
	// are read; a newer one is refused.
	Version = 1
)

// document is the exported Library.
type document struct {
	Format   string         `json:"format"`
	Version  int            `json:"version"`
	Exported time.Time      `json:"exported"`
	Items    []documentItem `json:"items"`
}

type documentItem struct {
	URL                string `json:"url"`
	Name               string `json:"name,omitempty"`
	Kind               string `json:"kind,omitempty"`
	Group              string `json:"group,omitempty"`
	IncludePrereleases bool   `json:"includePrereleases,omitempty"`
}

// Export is the Library as a JSON document: for each Item its repository
// address, name, kind, group and whether it follows pre-releases.
func Export(entries []catalog.Entry, groups []catalog.Group, now time.Time) ([]byte, error) {
	names := make(map[int64]string, len(groups))
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	doc := document{Format: Format, Version: Version, Exported: now.UTC().Truncate(time.Second), Items: make([]documentItem, 0, len(entries))}
	for _, e := range entries {
		doc.Items = append(doc.Items, documentItem{
			URL: source.RepoURL(e.Ref()), Name: e.Title(), Kind: string(e.Item.Kind),
			Group: names[e.Item.GroupID], IncludePrereleases: e.Item.IncludePrereleases,
		})
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode the library: %w", err)
	}
	return append(encoded, '\n'), nil
}

// LinkList is the address of every Item's repository, one to a line.
func LinkList(entries []catalog.Entry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(source.RepoURL(e.Ref()))
		b.WriteByte('\n')
	}
	return b.String()
}

// kindOf reads a kind from a file; anything but a known kind is detected.
func kindOf(s string) selection.ItemKind {
	switch kind := selection.ItemKind(strings.ToLower(strings.TrimSpace(s))); kind {
	case selection.ItemApp, selection.ItemTool:
		return kind
	}
	return ""
}
