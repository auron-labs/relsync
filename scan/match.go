package scan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/source"
)

// The points of a name match.
const (
	exactNamePoints     = 70
	containedNamePoints = 45
	publisherPoints     = 35
	popularPoints       = 5
	// LikelyScore is the score from which a match is likely rather than possible.
	LikelyScore = 100

	// minNameLength is the shortest cleaned name worth searching for.
	minNameLength = 3
	// minContainedLength is the shortest name that may match by being part of another.
	minContainedLength = 4
	// minStars is how popular a repository must be to count as a match.
	minStars = 15
	// popularStars is how popular a repository must be to earn its points.
	popularStars = 500
)

var (
	// ErrNameTooShort is a program whose name says too little to search for.
	ErrNameTooShort = errors.New("the name is too short to search for")
	// ErrNoMatch is a search that found no repository worth offering.
	ErrNoMatch = errors.New("no match found")
)

// Match is a repository found by name, and how well it fits.
type Match struct {
	Repo  source.Repository
	Score int
}

// Kind is the kind of Link the match makes: likely when the name is exact and
// the publisher agrees, possible otherwise.
func (m Match) Kind() LinkKind {
	if m.Score >= LikelyScore {
		return LinkLikely
	}
	return LinkPossible
}

// Score says how well a repository found by searching fits a program, 0 for
// not at all. The repository's name equal to the cleaned name is 70 points and
// one containing the other, with both at least four characters, 45. The
// publisher and owner containing one another adds 35, and at least 500 stars
// adds 5. A repository with fewer than 15 stars is not a match.
func Score(c Candidate, repo source.Repository) int {
	clean, name := nameKey(CleanName(c.Name)), nameKey(repo.Name)
	if len(clean) < minNameLength || repo.Stars < minStars {
		return 0
	}
	var score int
	switch {
	case name == clean:
		score = exactNamePoints
	case len(name) >= minContainedLength && len(clean) >= minContainedLength &&
		(strings.Contains(name, clean) || strings.Contains(clean, name)):
		score = containedNamePoints
	default:
		return 0
	}
	publisher, owner := nameKey(c.Publisher), nameKey(repo.Owner)
	if len(publisher) > 2 && len(owner) > 2 && (strings.Contains(publisher, owner) || strings.Contains(owner, publisher)) {
		score += publisherPoints
	}
	if repo.Stars >= popularStars {
		score += popularPoints
	}
	return score
}

// BestMatch picks the highest scoring repository of the results, the first of
// equals, and reports false when none fits.
func BestMatch(c Candidate, results []source.Repository) (Match, bool) {
	var best Match
	for _, repo := range results {
		if score := Score(c, repo); score > best.Score {
			best = Match{Repo: repo, Score: score}
		}
	}
	return best, best.Score > 0
}

// Matches lists every repository of the results that fits the program, best
// first and the first of equals first.
func Matches(c Candidate, results []source.Repository) []Match {
	var found []Match
	for _, repo := range results {
		if score := Score(c, repo); score > 0 {
			found = append(found, Match{Repo: repo, Score: score})
		}
	}
	slices.SortStableFunc(found, func(a, b Match) int { return b.Score - a.Score })
	return found
}

// Find searches for the program by its cleaned name and returns the best match.
// It fails with ErrNameTooShort or ErrNoMatch when there is nothing to offer,
// and with the search's own error when the search could not be made.
func Find(ctx context.Context, s source.Searcher, c Candidate) (Match, error) {
	name := CleanName(c.Name)
	if len(nameKey(name)) < minNameLength {
		return Match{}, ErrNameTooShort
	}
	results, err := s.SearchRepositories(ctx, name)
	if err != nil {
		return Match{}, fmt.Errorf("search for %q: %w", name, err)
	}
	m, ok := BestMatch(c, results)
	if !ok {
		return Match{}, ErrNoMatch
	}
	return m, nil
}
