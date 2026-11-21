package hints

import (
	"slices"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/source"
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func ago(days int) time.Time { return now.AddDate(0, 0, -days) }

// repo is a middle-aged, licensed repository that raises no signal.
func repo(change func(*source.Repository)) source.Repository {
	r := source.Repository{
		Owner: "acme", Name: "tool", Stars: 50, License: "MIT", Created: ago(400), Pushed: ago(10),
	}
	if change != nil {
		change(&r)
	}
	return r
}

func signals(hints []Hint) []Signal {
	out := make([]Signal, len(hints))
	for i, h := range hints {
		out[i] = h.Signal
	}
	return out
}

func TestHintsOverRepositoryFacts(t *testing.T) {
	digests := &source.Release{Assets: []source.Asset{{Name: "a.zip"}, {Name: "b.zip", Digest: "sha256:abc"}}}
	bare := &source.Release{Assets: []source.Asset{{Name: "a.zip"}}}
	for _, tc := range []struct {
		name  string
		facts Facts
		want  []Signal
	}{
		{"an ordinary repository raises nothing", Facts{Repository: repo(nil)}, nil},
		{"under 30 days old", Facts{Repository: repo(func(r *source.Repository) { r.Created = ago(29) })}, []Signal{NewRepository}},
		{"exactly 30 days old is not new", Facts{Repository: repo(func(r *source.Repository) { r.Created = ago(30) })}, nil},
		{"two years old", Facts{Repository: repo(func(r *source.Repository) { r.Created = ago(730) })}, []Signal{EstablishedRepository}},
		{"a day short of two years", Facts{Repository: repo(func(r *source.Repository) { r.Created = ago(729) })}, nil},
		{"archived", Facts{Repository: repo(func(r *source.Repository) { r.Archived = true })}, []Signal{Archived}},
		{"archived hides the staleness it implies", Facts{Repository: repo(func(r *source.Repository) { r.Archived, r.Pushed = true, ago(900) })}, []Signal{Archived}},
		{"not pushed for over two years", Facts{Repository: repo(func(r *source.Repository) { r.Pushed = ago(731) })}, []Signal{Stale}},
		{"pushed two years ago exactly", Facts{Repository: repo(func(r *source.Repository) { r.Pushed = ago(730) })}, nil},
		{"never pushed says nothing", Facts{Repository: repo(func(r *source.Repository) { r.Pushed = time.Time{} })}, nil},
		{"four stars", Facts{Repository: repo(func(r *source.Repository) { r.Stars = 4 })}, []Signal{FewStars}},
		{"five stars", Facts{Repository: repo(func(r *source.Repository) { r.Stars = 5 })}, nil},
		{"999 stars", Facts{Repository: repo(func(r *source.Repository) { r.Stars = 999 })}, nil},
		{"1,000 stars", Facts{Repository: repo(func(r *source.Repository) { r.Stars = 1000 })}, []Signal{ManyStars}},
		{"no licence", Facts{Repository: repo(func(r *source.Repository) { r.License = "" })}, []Signal{NoLicense}},
		{"a fork", Facts{Repository: repo(func(r *source.Repository) { r.Fork = true })}, []Signal{Fork}},
		{"checksums on the latest release", Facts{Repository: repo(nil), Latest: digests}, []Signal{Checksums}},
		{"no checksums", Facts{Repository: repo(nil), Latest: bare}, nil},
		{"an account under 60 days old", Facts{Repository: repo(nil), Owner: &source.Owner{Created: ago(59)}}, []Signal{NewOwner}},
		{"an account of 60 days", Facts{Repository: repo(nil), Owner: &source.Owner{Created: ago(60)}}, nil},
		{"an organisation", Facts{Repository: repo(nil), Owner: &source.Owner{Created: ago(3000), Organization: true}}, []Signal{Organization}},
		{"a new organisation is only a warning", Facts{Repository: repo(nil), Owner: &source.Owner{Created: ago(5), Organization: true}}, []Signal{NewOwner}},
		{"an owner with no date says nothing about age", Facts{Repository: repo(nil), Owner: &source.Owner{}}, nil},
		{"details that are not known say nothing", Facts{Repository: source.Repository{Owner: "acme", Name: "tool", Stars: 1, Archived: true}}, nil},
		{"stars and licence are only judged on GitHub", Facts{Repository: repo(func(r *source.Repository) { r.Source, r.Stars, r.License = "gitlab.com", 0, "" })}, nil},
	} {
		tc.facts.Now = now
		if got := signals(Build(tc.facts)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: signals = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWarningsComeFirstAndAtMostSixAreShown(t *testing.T) {
	facts := Facts{
		Now: now, Latest: &source.Release{Assets: []source.Asset{{Digest: "sha256:abc"}}},
		Owner: &source.Owner{Created: ago(10)},
		Repository: repo(func(r *source.Repository) {
			r.Created, r.Archived, r.Stars, r.License, r.Fork = ago(5), true, 2, "", true
		}),
	}
	got := signals(Build(facts))
	// Seven signals hold; the checksums, a good one, are the one cut.
	want := []Signal{NewRepository, Archived, FewStars, NoLicense, NewOwner, Fork}
	if !slices.Equal(got, want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
}

func TestGoodSignalsFollowInfoAndWarnings(t *testing.T) {
	facts := Facts{
		Now: now, Latest: &source.Release{Assets: []source.Asset{{Digest: "sha256:abc"}}},
		Owner: &source.Owner{Created: ago(4000), Organization: true},
		Repository: repo(func(r *source.Repository) {
			r.Created, r.Stars, r.Fork, r.License = ago(2000), 5000, true, ""
		}),
	}
	hints := Build(facts)
	levels := make([]Level, len(hints))
	for i, h := range hints {
		levels[i] = h.Level
	}
	if !slices.IsSorted(levels) || len(hints) != 6 || hints[0].Signal != NoLicense {
		t.Fatalf("hints = %+v", hints)
	}
}
