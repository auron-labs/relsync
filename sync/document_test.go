package sync

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestADocumentSurvivesBeingWrittenAndRead(t *testing.T) {
	doc := Document{
		Format: Format, Version: Version, UpdatedAt: at(5), WrittenBy: "Desk",
		Items: []Item{{
			Source: "github", Owner: "o", Repo: "r", Lane: "cli-", Name: "R", Kind: "tool", Method: "portable", Group: "Games",
			Favorite: true, Hidden: true, IncludePrereleases: true, HoldUpdates: true, AutoUpdate: true, TrustUnsigned: true,
			SkippedVersion: "v1", InstallerArgs: "/S", Note: "n", Tags: []string{"a", "b"},
			Intent:  &Intent{Product: "p", Kind: "archive", Arch: "x64", ArchiveMembers: []string{"bin/r.exe"}, Explicit: true},
			AddedAt: at(1), ModifiedAt: at(2),
		}},
		TryLater:   []TryLaterEntry{{Source: "github", Owner: "o", Repo: "s", Stars: 3, Topics: []string{"x"}, SavedAt: at(1), ModifiedAt: at(3)}},
		Groups:     []Group{{Name: "Games", CreatedAt: at(0)}},
		Tombstones: map[string]time.Time{"item:github:o/gone": at(4)},
		Devices:    map[string]Device{"pc": {ID: "pc", Name: "Desk", Platform: "Windows", AppVersion: "1.0", LastSeen: at(5)}},
	}
	text, err := doc.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded(got)) != string(encoded(doc.clean())) {
		t.Errorf("read back:\n%s\nwant:\n%s", encoded(got), encoded(doc.clean()))
	}
}

func TestEmptyCollectionsAreWrittenAsEmptyNotMissing(t *testing.T) {
	text, err := Document{}.clean().Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"items": []`, `"tryLater": []`, `"groups": []`, `"tombstones": {}`, `"devices": {}`} {
		if !strings.Contains(text, want) {
			t.Errorf("document has no %s: %s", want, text)
		}
	}
}

func TestParseRefusesWhatIsNotADocumentAndWhatIsNewer(t *testing.T) {
	for name, c := range map[string]struct {
		text string
		want error
	}{
		"not JSON":           {"hello", ErrUnreadable},
		"a list":             {"[]", ErrUnreadable},
		"another format":     {`{"format": "something-else", "version": 1}`, ErrUnreadable},
		"no format":          {`{"version": 1}`, ErrUnreadable},
		"no version":         {`{"format": "` + Format + `"}`, ErrUnreadable},
		"a wrong field":      {`{"format": "` + Format + `", "version": 1, "items": "none"}`, ErrUnreadable},
		"a newer version":    {`{"format": "` + Format + `", "version": 2}`, ErrNewerFormat},
		"the current format": {`{"format": "` + Format + `", "version": 1}`, nil},
	} {
		_, err := Parse(c.text)
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: Parse = %v, want %v", name, err, c.want)
		}
	}
}

func TestParseDropsEntriesThatNameNothingAndIgnoresWhatItDoesNotKnow(t *testing.T) {
	text := `{"format": "` + Format + `", "version": 1, "someday": true,
		"items": [{"source": "github", "owner": "o", "repo": "r", "future": 1}, {"source": "github", "owner": "", "repo": "r"}, {"owner": "o", "repo": "r"}],
		"tryLater": [{"source": "github", "owner": "o"}],
		"groups": [{"name": "  "}, {"name": " Games "}]}`
	doc, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) != 1 || len(doc.TryLater) != 0 || len(doc.Groups) != 1 || doc.Groups[0].Name != "Games" {
		t.Errorf("document = %+v, want the one usable item and group", doc)
	}
}

func TestKeysNameTheSameRepositoryWhateverTheCase(t *testing.T) {
	a := Item{Source: "github", Owner: "Acme", Repo: "Tool"}
	b := Item{Source: "GitHub", Owner: "acme", Repo: "TOOL"}
	if a.Key() != b.Key() || a.Key() == "" {
		t.Errorf("keys %q and %q differ", a.Key(), b.Key())
	}
	if (Item{Owner: "o", Repo: "r"}).Key() != "" || (Group{Name: " "}).Key() != "" {
		t.Error("an entry that names nothing has a key")
	}
	if a.Key() == (TryLaterEntry{Source: "github", Owner: "Acme", Repo: "Tool"}).Key() {
		t.Error("an Item and a saved repository share a key, so one removal would remove both")
	}
}
