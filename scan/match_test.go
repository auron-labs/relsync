package scan

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/source"
)

func repo(owner, name string, stars int) source.Repository {
	return source.Repository{Owner: owner, Name: name, Stars: stars}
}

func TestScoreFollowsTheTable(t *testing.T) {
	program := Candidate{Name: "7-Zip 26.02 (x64)", Publisher: "Igor Pavlov"}
	for _, tc := range []struct {
		name string
		repo source.Repository
		want int
	}{
		{"an exact name", repo("someone", "7zip", 100), 70},
		{"an exact name and the publisher", repo("igorpavlov", "7zip", 100), 70 + 35},
		{"an exact name, the publisher and a popular repository", repo("igor-pavlov", "7-zip", 900), 70 + 35 + 5},
		{"an exact name and a popular repository", repo("someone", "7zip", 500), 70 + 5},
		{"a name that contains it", repo("someone", "7zip-extras", 100), 45},
		{"a name it contains", repo("someone", "7zip", 100), 70},
		{"a contained name and the publisher", repo("pavlov", "7zip-extras", 100), 45 + 35},
		{"too few stars", repo("igorpavlov", "7zip", 14), 0},
		{"exactly enough stars", repo("someone", "7zip", 15), 70},
		{"another name", repo("igorpavlov", "zlib", 5000), 0},
	} {
		if got := Score(program, tc.repo); got != tc.want {
			t.Errorf("%s: Score = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestShortNamesMatchOnlyWhenEqual(t *testing.T) {
	if got := Score(Candidate{Name: "Git"}, repo("x", "git-lfs", 1000)); got != 0 {
		t.Errorf("a short name contained in another scored %d", got)
	}
	if got := Score(Candidate{Name: "Git"}, repo("git", "git", 1000)); got != exactNamePoints+popularPoints {
		t.Errorf("an exact short name scored %d", got)
	}
	if got := Score(Candidate{Name: "Go"}, repo("golang", "go", 1000)); got != 0 {
		t.Errorf("a name of two letters scored %d", got)
	}
}

func TestALikelyMatchNeedsAnExactNameAndAMatchingPublisher(t *testing.T) {
	program := Candidate{Name: "Apollo", Publisher: "Acme Software"}
	for _, tc := range []struct {
		name string
		repo source.Repository
		want LinkKind
	}{
		{"exact name and publisher", repo("acme", "apollo", 40), LinkLikely},
		{"exact name alone", repo("stranger", "apollo", 5000), LinkPossible},
		{"publisher and a contained name", repo("acme", "apollo-desktop", 5000), LinkPossible},
	} {
		got, ok := BestMatch(program, []source.Repository{tc.repo})
		if !ok || got.Kind() != tc.want {
			t.Errorf("%s: %+v, %v; want %v", tc.name, got, ok, tc.want)
		}
	}
}

func TestBestMatchTakesTheHighestScoreAndTheFirstOfEquals(t *testing.T) {
	program := Candidate{Name: "Tool", Publisher: "Acme"}
	results := []source.Repository{repo("stranger", "tool", 100), repo("acme", "tool", 100), repo("other", "tool", 100)}
	got, ok := BestMatch(program, results)
	if !ok || got.Repo.Owner != "acme" {
		t.Errorf("best = %+v, %v", got, ok)
	}
	got, _ = BestMatch(Candidate{Name: "Tool"}, results)
	if got.Repo.Owner != "stranger" {
		t.Errorf("first of equals = %+v", got)
	}
	if _, ok := BestMatch(program, []source.Repository{repo("a", "unrelated", 9000)}); ok {
		t.Error("an unrelated repository matched")
	}
}

func TestMatchesListsEveryFittingRepositoryBestFirst(t *testing.T) {
	program := Candidate{Name: "Tool", Publisher: "Acme"}
	got := Matches(program, []source.Repository{
		repo("stranger", "tool", 100), repo("a", "unrelated", 9000), repo("acme", "tool", 100), repo("b", "tool", 14), repo("c", "tool-extras", 100),
	})
	owners := make([]string, len(got))
	for i, m := range got {
		owners[i] = m.Repo.Owner
	}
	if !slices.Equal(owners, []string{"acme", "stranger", "c"}) {
		t.Errorf("matches = %v", owners)
	}
}

type searcher struct {
	results []source.Repository
	err     error
	queries []string
}

func (s *searcher) SearchRepositories(_ context.Context, name string) ([]source.Repository, error) {
	s.queries = append(s.queries, name)
	return s.results, s.err
}

func TestFindSearchesForTheCleanedName(t *testing.T) {
	s := &searcher{results: []source.Repository{repo("ip7z", "7zip", 900)}}
	got, err := Find(t.Context(), s, Candidate{Name: "7-Zip 26.02 (x64)", Publisher: "Igor Pavlov"})
	if err != nil || got.Repo.Name != "7zip" || len(s.queries) != 1 || s.queries[0] != "7-Zip" {
		t.Fatalf("got %+v, %v after %v", got, err, s.queries)
	}
}

func TestFindSaysWhyThereIsNothingToOffer(t *testing.T) {
	s := &searcher{}
	if _, err := Find(t.Context(), s, Candidate{Name: "Go"}); !errors.Is(err, ErrNameTooShort) || len(s.queries) != 0 {
		t.Errorf("short name: %v after %v", err, s.queries)
	}
	if _, err := Find(t.Context(), s, Candidate{Name: "Something"}); !errors.Is(err, ErrNoMatch) {
		t.Errorf("no results: %v", err)
	}
	limited := &searcher{err: &source.Error{Kind: source.KindRateLimited}}
	var se *source.Error
	if _, err := Find(t.Context(), limited, Candidate{Name: "Something"}); !errors.As(err, &se) || se.Kind != source.KindRateLimited {
		t.Errorf("limited search: %v", err)
	}
}

func TestPacerSpacesRequestsAndStopsWaitingWhenCanceled(t *testing.T) {
	p := NewPacer(40 * time.Millisecond)
	start := time.Now()
	for range 3 {
		if err := p.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(start); took < 80*time.Millisecond {
		t.Errorf("three requests took %v", took)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	slow := NewPacer(time.Hour)
	_ = slow.Wait(t.Context()) // The first is immediate.
	if err := slow.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled wait = %v", err)
	}
	if SearchGap(true) >= SearchGap(false) {
		t.Error("a token should allow searches closer together")
	}
}
