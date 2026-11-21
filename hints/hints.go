// Package hints turns what a site says about a repository and its owner into
// the few plain signals shown before the repository is added. A hint never
// blocks anything; it says what a careful person would look at.
package hints

import (
	"slices"
	"time"

	"github.com/aaronflorey/release-sync/source"
)

// Max is how many hints are shown.
const Max = 6

// Level is how a hint reads, in the order hints are listed.
type Level int

// The levels.
const (
	// Warning is something to look at before trusting the project.
	Warning Level = iota
	// Info is a fact that is neither good nor bad.
	Info
	// Good is a sign of a project that is looked after.
	Good
)

// Signal is what a hint is about.
type Signal int

// The signals.
const (
	// NewRepository is a repository under 30 days old.
	NewRepository Signal = iota
	// EstablishedRepository is a repository at least two years old.
	EstablishedRepository
	// Archived is a repository its owner no longer maintains.
	Archived
	// Stale is a repository not pushed to for over two years.
	Stale
	// FewStars is a repository fewer than five people starred.
	FewStars
	// ManyStars is a repository with at least 1,000 stars.
	ManyStars
	// NoLicense is a repository that states no licence.
	NoLicense
	// Fork is a fork of another project.
	Fork
	// Checksums is a latest release whose files carry checksums.
	Checksums
	// NewOwner is an owner account under 60 days old.
	NewOwner
	// Organization is an owner that is an organisation.
	Organization
)

// Hint is one signal and the number it was drawn from.
type Hint struct {
	Signal Signal
	Level  Level
	// Age is how old the repository or the owner's account is, for the signals about age.
	Age time.Duration
	// Pushed is when the repository was last pushed to, for Stale.
	Pushed time.Time
	// Stars is the repository's stars, for the signals about them.
	Stars int
	// Name is the owner's display name, for Organization.
	Name string
}

// Facts are what the hints are drawn from.
type Facts struct {
	Repository source.Repository
	// Latest is the repository's newest release; nil when it is not known.
	Latest *source.Release
	// Owner is the account that owns the repository; nil when it is not known.
	Owner *source.Owner
	Now   time.Time
}

const (
	day           = 24 * time.Hour
	newRepository = 30 * day
	oldRepository = 730 * day
	staleAfter    = 730 * day
	newOwner      = 60 * day
	fewStars      = 5
	manyStars     = 1000
)

// Build lists the signals the facts show, warnings first and at most Max.
// Nothing is said about a repository whose details are not known, as a saved
// snapshot's are not. Stars and licences are only judged on GitHub, the one
// site that reports both.
func Build(f Facts) []Hint {
	repo := f.Repository
	if repo.Created.IsZero() {
		return nil
	}
	var hints []Hint
	add := func(h Hint) { hints = append(hints, h) }
	if age := f.Now.Sub(repo.Created); age < newRepository {
		add(Hint{Signal: NewRepository, Level: Warning, Age: age})
	} else if age >= oldRepository {
		add(Hint{Signal: EstablishedRepository, Level: Good, Age: age})
	}
	switch {
	case repo.Archived:
		add(Hint{Signal: Archived, Level: Warning})
	case !repo.Pushed.IsZero() && f.Now.Sub(repo.Pushed) > staleAfter:
		add(Hint{Signal: Stale, Level: Warning, Pushed: repo.Pushed})
	}
	if source.IsGitHub(repo.Source) {
		switch {
		case repo.Stars < fewStars:
			add(Hint{Signal: FewStars, Level: Warning, Stars: repo.Stars})
		case repo.Stars >= manyStars:
			add(Hint{Signal: ManyStars, Level: Good, Stars: repo.Stars})
		}
		if repo.License == "" {
			add(Hint{Signal: NoLicense, Level: Warning})
		}
	}
	if repo.Fork {
		add(Hint{Signal: Fork, Level: Info})
	}
	if f.Latest != nil && slices.ContainsFunc(f.Latest.Assets, func(a source.Asset) bool { return a.Digest != "" }) {
		add(Hint{Signal: Checksums, Level: Good})
	}
	if o := f.Owner; o != nil {
		if age := f.Now.Sub(o.Created); !o.Created.IsZero() && age < newOwner {
			add(Hint{Signal: NewOwner, Level: Warning, Age: age})
		} else if o.Organization {
			add(Hint{Signal: Organization, Level: Good, Name: o.Name})
		}
	}
	slices.SortStableFunc(hints, func(a, b Hint) int { return int(a.Level) - int(b.Level) })
	return hints[:min(len(hints), Max)]
}
