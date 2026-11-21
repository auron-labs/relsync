package exchange

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/source"
)

// Options are the choices made for an import.
type Options struct {
	// GroupID puts every repository in that group, whatever the file says; 0
	// leaves each in the group its file names, or its category's.
	GroupID int64
	// AlreadyInstalled tracks the repositories as software the person has
	// already, at its latest version, instead of as software to install.
	AlreadyInstalled bool
	// Prereleases is the default for entries that do not ask for them.
	Prereleases bool
	// Metadata fetches each repository's description and the like as it is
	// added, which costs a request each; otherwise the first check does.
	Metadata bool
}

// Progress says which repository an import is at.
type Progress struct {
	// Done is how many entries are finished, Total how many there are.
	Done, Total int
	Ref         source.RepoRef
}

// Failure is a repository that could not be added.
type Failure struct {
	Ref source.RepoRef
	Err error
}

// Result is what an import did.
type Result struct {
	// Added are the Items made, in the order of the file.
	Added          []catalog.Entry
	AlreadyTracked int
	Failed         []Failure
	// Stopped is why the import ended before its last entry: the request
	// allowance ran out, or the import was canceled. Nil when it finished.
	Stopped error
}

// Import adds each entry to the Library, one at a time, calling progress
// before each. Repositories already tracked are left alone. A request limit
// stops the import and keeps what was added; so does a canceled context. Any
// other failure leaves out that one repository.
func Import(ctx context.Context, lib *catalog.Catalog, entries []Entry, o Options, progress func(Progress)) Result {
	var result Result
	for i, e := range entries {
		if progress != nil {
			progress(Progress{Done: i, Total: len(entries), Ref: e.Ref})
		}
		if _, tracked := lib.Find(e.Ref); tracked {
			result.AlreadyTracked++
			continue
		}
		added, err := importOne(ctx, lib, e, o)
		switch {
		case errors.Is(err, catalog.ErrExists):
			result.AlreadyTracked++
		case ctx.Err() != nil:
			result.Stopped = ctx.Err()
			return result
		case isRateLimit(err):
			result.Stopped = err
			return result
		case err != nil:
			result.Failed = append(result.Failed, Failure{Ref: e.Ref, Err: err})
		default:
			result.Added = append(result.Added, added)
		}
	}
	return result
}

func importOne(ctx context.Context, lib *catalog.Catalog, e Entry, o Options) (catalog.Entry, error) {
	lookup := lib.PreviewReleases
	if o.Metadata {
		lookup = lib.Preview
	}
	preview, err := lookup(ctx, e.Ref)
	if err != nil {
		return catalog.Entry{}, err
	}
	groupID, err := resolveGroup(ctx, lib, o.GroupID, e.Group)
	if err != nil {
		return catalog.Entry{}, err
	}
	added, err := lib.Add(ctx, preview, catalog.AddOptions{
		IncludePrereleases: e.Prereleases || o.Prereleases, Kind: e.Kind, GroupID: groupID, AlreadyHave: o.AlreadyInstalled,
	})
	if err != nil {
		return added, fmt.Errorf("import %s: %w", e.Ref, err)
	}
	return added, nil
}

func isRateLimit(err error) bool {
	var e *source.Error
	return errors.As(err, &e) && e.Kind == source.KindRateLimited
}

// resolveGroup is the id of the group an entry goes in: the one chosen, else
// the file's named group, made when it is new, else 0 for the category's.
func resolveGroup(ctx context.Context, lib *catalog.Catalog, chosen int64, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if chosen != 0 || name == "" {
		return chosen, nil
	}
	groups := lib.Groups().Get()
	if i := slices.IndexFunc(groups, func(g catalog.Group) bool { return strings.EqualFold(g.Name, name) }); i >= 0 {
		return groups[i].ID, nil
	}
	created, err := lib.CreateGroup(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("create group %q: %w", name, err)
	}
	return created.ID, nil
}
