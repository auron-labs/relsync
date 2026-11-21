package exchange

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/aaronflorey/release-sync/selection"
	"github.com/aaronflorey/release-sync/source"
)

// ErrNewerFormat means the file was exported by a newer version of this
// application than the one reading it.
var ErrNewerFormat = errors.New("the file was exported by a newer version")

// Links reads repository addresses. A source.Registry does, knowing which
// hosts are in use.
type Links interface {
	// ParseLink reads an address or a bare owner/repo.
	ParseLink(text string) (source.RepoRef, bool)
	// ParseAddress reads an address, which names its site.
	ParseAddress(text string) (source.RepoRef, bool)
}

// Entry is one repository a file asks for.
type Entry struct {
	Ref source.RepoRef
	// Group is the name of the group the file puts the Item in, "" for none.
	Group string
	// Kind is what the file says the Item is, "" to detect it.
	Kind        selection.ItemKind
	Prereleases bool
}

// Parsed is what a file or pasted text holds.
type Parsed struct {
	Entries []Entry
	// Skipped counts the entries of a list that did not name a repository.
	Skipped int
}

// Parse reads the repositories in text: this application's export, the
// ReleaseSync export (a document with an "apps" array, or a bare array),
// bin's export, whose entries are Tools, or any other text, in which every
// repository address counts and so does a line that is a bare owner/repo.
// Each repository appears once. The only error is ErrNewerFormat.
func Parse(text string, links Links) (Parsed, error) {
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	if trimmed := strings.TrimSpace(text); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		parsed, recognised, err := parseJSON(trimmed, links)
		if err != nil || recognised {
			return parsed, err
		}
	}
	return parseText(text, links), nil
}

// collector gathers entries, once for each repository.
type collector struct {
	parsed Parsed
	seen   map[string]bool
}

func (c *collector) add(e Entry) {
	key := strings.ToLower(e.Ref.SourceKey() + "|" + e.Ref.Owner + "/" + e.Ref.Repo)
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.parsed.Entries = append(c.parsed.Entries, e)
}

// parseJSON reads a list of entries from a JSON document. recognised is false
// for JSON that is no list of repositories, which is then read as text.
func parseJSON(text string, links Links) (_ Parsed, recognised bool, _ error) {
	var root any
	if err := json.Unmarshal(stripTrailingCommas([]byte(text)), &root); err != nil {
		return Parsed{}, false, nil //nolint:nilerr // Not valid JSON, so it is plain text.
	}
	var list []any
	switch v := root.(type) {
	case []any:
		list = v
	case map[string]any:
		if format, _ := v["format"].(string); format == Format {
			if version, _ := v["version"].(float64); int(version) > Version {
				return Parsed{}, true, ErrNewerFormat
			}
			list, _ = v["items"].([]any)
			break
		}
		apps, found := v["apps"].([]any)
		if !found {
			return Parsed{}, false, nil
		}
		list = apps
	default:
		return Parsed{}, false, nil
	}
	var c collector
	for _, item := range list {
		object, _ := item.(map[string]any)
		entry, ok := entryOf(object, links)
		if !ok {
			c.parsed.Skipped++
			continue
		}
		c.add(entry)
	}
	return c.parsed, true, nil
}

// binKeys are fields of bin's export that no other format has.
var binKeys = []string{"remote_name", "provider", "package_path", "hash", "install_mode"}

// entryOf reads one entry of a list.
func entryOf(object map[string]any, links Links) (Entry, bool) {
	address, _ := object["url"].(string)
	ref, ok := links.ParseLink(address)
	if !ok {
		return Entry{}, false
	}
	e := Entry{Ref: ref}
	if group, ok := object["group"].(string); ok {
		e.Group = strings.TrimSpace(group)
	} else if categories, ok := object["categories"].([]any); ok && len(categories) > 0 {
		first, _ := categories[0].(string)
		e.Group = strings.TrimSpace(first)
	}
	kind, _ := object["kind"].(string)
	e.Kind = kindOf(kind)
	for _, key := range binKeys {
		if _, isBin := object[key]; isBin && e.Kind == "" {
			e.Kind = selection.ItemTool
		}
	}
	e.Prereleases = object["includePrereleases"] == true || additionalSettingsPrereleases(object["additionalSettings"])
	return e, true
}

// additionalSettingsPrereleases reads the reference export's nested settings,
// which are a JSON object or a string holding one.
func additionalSettingsPrereleases(v any) bool {
	if s, ok := v.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(s), &decoded) != nil {
			return false
		}
		v = decoded
	}
	settings, _ := v.(map[string]any)
	return settings["includePrereleases"] == true
}

// stripTrailingCommas removes the comma that precedes a closing bracket,
// which people's hand-edited lists have and JSON does not allow.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escaped := false, false
	for i, b := range data {
		switch {
		case inString:
			inString, escaped = b != '"' || escaped, b == '\\' && !escaped
		case b == '"':
			inString = true
		case b == ',' && closesNext(data[i+1:]):
			continue
		}
		out = append(out, b)
	}
	return out
}

// closesNext reports that the next thing in data, ignoring spaces, is a closing bracket.
func closesNext(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
		case ']', '}':
			return true
		default:
			return false
		}
	}
	return false
}

// listMarker is what starts a line of a list: a bullet, a number or a task box.
var listMarker = regexp.MustCompile(`^\s*(?:[-*+>]|\d+[.)])\s+(?:\[[ xX]\]\s+)?`)

// parseText finds repositories in any text: an address wherever one stands,
// and a bare owner/repo only as a whole line or as the first field of a
// line of a table, so that prose is not mistaken for a list.
func parseText(text string, links Links) Parsed {
	var c collector
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		found := false
		for _, token := range strings.FieldsFunc(line, isSeparator) {
			if ref, ok := links.ParseAddress(strings.TrimRight(token, ".,:;!?*_")); ok {
				c.add(Entry{Ref: ref})
				found = true
			}
		}
		if found {
			continue
		}
		field := firstField(listMarker.ReplaceAllString(line, ""))
		if ref, ok := links.ParseLink(field); ok {
			c.add(Entry{Ref: ref})
		}
	}
	return c.parsed
}

// isSeparator marks what sets an address apart in prose, Markdown and tables.
func isSeparator(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(`,;|"'()[]<>{}`, r) }

// firstField is the first cell of a table row, or the whole line.
func firstField(line string) string {
	if i := strings.IndexAny(line, ",;\t|"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}
