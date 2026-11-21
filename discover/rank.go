package discover

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/aaronflorey/release-sync/source"
)

const (
	// snapshotSize is how many ranks a saved ranking keeps.
	snapshotSize = 60
	// snapshotEvery is the least time between two saved rankings of a list.
	snapshotEvery = 20 * time.Hour
	// snapshotsKept is how many saved rankings of a list are kept.
	snapshotsKept = 12
	// baselineMinAge is how old a ranking must be to be compared with.
	baselineMinAge = 18 * time.Hour
	weekMinAge     = 6 * 24 * time.Hour
	// youngestAge floors the age Trending divides stars by, so a project made
	// yesterday is not ranked on one day's stars.
	youngestAge = 7.0
	// unknownAge is the age assumed for a repository with no creation date.
	unknownAge = 365.0
)

// Window is how far back a ranking is compared with.
type Window string

// The comparison windows.
const (
	Day  Window = "day"
	Week Window = "week"
)

// Rank is where a project stood in a saved ranking.
type Rank struct {
	Position int `json:"position"`
	Stars    int `json:"stars"`
}

// Snapshot is a saved ranking: the top of a list at one moment, by lower-cased
// owner/repo.
type Snapshot struct {
	At    time.Time       `json:"at"`
	Ranks map[string]Rank `json:"ranks"`
}

// Baseline is the earlier ranking a list is compared with.
type Baseline struct {
	Snapshot Snapshot
	Age      time.Duration
}

// repoKey identifies a repository in a ranking.
func repoKey(r source.Repository) string { return strings.ToLower(r.Owner + "/" + r.Name) }

// NewSnapshot saves the top of a list, which must already be in order.
func NewSnapshot(at time.Time, ranked []source.Repository) Snapshot {
	snap := Snapshot{At: at, Ranks: map[string]Rank{}}
	for i, r := range ranked[:min(len(ranked), snapshotSize)] {
		snap.Ranks[repoKey(r)] = Rank{Position: i + 1, Stars: r.Stars}
	}
	return snap
}

// SnapshotDue reports whether enough time has passed since the newest saved
// ranking for another to be saved.
func SnapshotDue(history []Snapshot, now time.Time) bool {
	for _, s := range history {
		if now.Sub(s.At) < snapshotEvery {
			return false
		}
	}
	return true
}

// BaselineFor picks the earlier ranking to compare with: the newest one at
// least a day old for Day, at least about a week old for Week. With too little
// history for a week it takes the oldest there is. Rankings under 18 hours old
// are never compared with.
func BaselineFor(history []Snapshot, w Window, now time.Time) (Baseline, bool) {
	var old []Snapshot
	for _, s := range history {
		if now.Sub(s.At) >= baselineMinAge {
			old = append(old, s)
		}
	}
	if len(old) == 0 {
		return Baseline{}, false
	}
	slices.SortFunc(old, func(a, b Snapshot) int { return b.At.Compare(a.At) })
	minAge := baselineMinAge
	if w == Week {
		minAge = weekMinAge
	}
	pick := old[len(old)-1]
	for _, s := range old {
		if now.Sub(s.At) >= minAge {
			pick = s
			break
		}
	}
	return Baseline{Snapshot: pick, Age: now.Sub(pick.At)}, true
}

// Order puts a list in the order its tab shows. Trending ranks by stars gained
// a day, nudged toward projects more people already trust: the gain comes from
// the baseline when it knew the project, else from the stars over the project's
// age. The other tabs rank by stars. Your stars keep the order GitHub gave,
// newest star first. Ties go to the name, so the order is stable.
func Order(repos []source.Repository, tab Tab, base *Baseline, now time.Time) []source.Repository {
	ordered := slices.Clone(repos)
	if tab == Stars {
		return ordered
	}
	score := func(r source.Repository) float64 { return float64(r.Stars) }
	if tab == Trending {
		score = func(r source.Repository) float64 {
			return starsPerDay(r, base, now) * math.Log10(float64(r.Stars)+10)
		}
	}
	scores := make(map[string]float64, len(ordered))
	for _, r := range ordered {
		scores[repoKey(r)] = score(r)
	}
	slices.SortStableFunc(ordered, func(a, b source.Repository) int {
		switch {
		case scores[repoKey(a)] != scores[repoKey(b)]:
			if scores[repoKey(a)] > scores[repoKey(b)] {
				return -1
			}
			return 1
		case a.Stars != b.Stars:
			return b.Stars - a.Stars
		}
		return strings.Compare(repoKey(a), repoKey(b))
	})
	return ordered
}

func starsPerDay(r source.Repository, base *Baseline, now time.Time) float64 {
	if base != nil {
		if prev, ok := base.Snapshot.Ranks[repoKey(r)]; ok {
			return float64(max(0, r.Stars-prev.Stars)) / max(1.0, base.Age.Hours()/24)
		}
	}
	age := unknownAge
	if !r.Created.IsZero() {
		age = max(youngestAge, now.Sub(r.Created).Hours()/24)
	}
	return float64(r.Stars) / age
}

// MoveKind says how a project moved since the baseline.
type MoveKind int

// The kinds of movement. MoveNone means there is nothing to compare with.
const (
	MoveNone MoveKind = iota
	MoveNew
	MoveUp
	MoveDown
	MoveSame
)

// Movement is how a project's place and stars changed since the baseline.
type Movement struct {
	Kind MoveKind
	// Places is how many places it rose or fell.
	Places int
	// Gained is the stars gained, when it was in the baseline and gained any.
	Gained int
}

// MovementOf compares a project's place now with its place in the baseline.
func MovementOf(base *Baseline, r source.Repository, position int) Movement {
	if base == nil {
		return Movement{}
	}
	prev, ok := base.Snapshot.Ranks[repoKey(r)]
	if !ok {
		return Movement{Kind: MoveNew}
	}
	m := Movement{Gained: max(0, r.Stars-prev.Stars)}
	switch delta := prev.Position - position; {
	case delta > 0:
		m.Kind, m.Places = MoveUp, delta
	case delta < 0:
		m.Kind, m.Places = MoveDown, -delta
	default:
		m.Kind = MoveSame
	}
	return m
}

// SinceKind says how far back a baseline reaches.
type SinceKind int

// The spans a baseline can be described as.
const (
	SinceYesterday SinceKind = iota
	SinceLastWeek
	SinceDays
)

// Since describes the baseline's age for the user: yesterday, last week, or a
// number of days.
func (b Baseline) Since() (kind SinceKind, days int) {
	age := b.Age.Hours() / 24
	switch {
	case b.Age < 30*time.Hour:
		return SinceYesterday, 1
	case age >= 6:
		return SinceLastWeek, 7
	}
	return SinceDays, int(math.Round(age))
}
