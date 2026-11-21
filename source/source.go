// Package source talks to the sites that host releases: GitHub, GitLab and
// Forgejo (Gitea, Codeberg). Callers depend on the Source interface.
package source

import (
	"context"
	"time"
)

// Source is a hosting service that publishes releases.
type Source interface {
	// Repository returns the repository's metadata.
	Repository(ctx context.Context, ref RepoRef) (Repository, error)
	// Releases returns the newest releases. A non-empty etag from an earlier
	// call makes the request conditional: when nothing changed the page has
	// NotModified set and no releases.
	Releases(ctx context.Context, ref RepoRef, etag string) (ReleasePage, error)
	// Allowance reports how many requests the current credentials may still make.
	Allowance() Allowance
}

// AllowanceOf is the allowance of the site the repository is on, for a Source
// that keeps one per site, and the Source's own otherwise.
func AllowanceOf(s Source, ref RepoRef) Allowance {
	if per, ok := s.(interface{ AllowanceFor(RepoRef) Allowance }); ok {
		return per.AllowanceFor(ref)
	}
	return s.Allowance()
}

// Searcher finds repositories by name.
type Searcher interface {
	// SearchRepositories finds repositories whose name contains name, forks
	// excluded, best match first.
	SearchRepositories(ctx context.Context, name string) ([]Repository, error)
}

// RepoRef names a repository.
type RepoRef struct {
	Owner string
	Repo  string
	// Source is the site's key (see SiteKey); empty is GitHub. An Owner on
	// GitLab can be a path of nested groups.
	Source string
}

// NewRef names a repository on the site with the given key.
func NewRef(site, owner, repo string) RepoRef {
	if site == GitHubName {
		site = ""
	}
	return RepoRef{Owner: owner, Repo: repo, Source: site}
}

// SourceKey is the key of the site the repository is on.
func (r RepoRef) SourceKey() string {
	if r.Source == "" {
		return GitHubName
	}
	return r.Source
}

// String returns owner/repo.
func (r RepoRef) String() string { return r.Owner + "/" + r.Repo }

// Repository is a repository's descriptive metadata.
type Repository struct {
	// Source is the site's key; empty is GitHub.
	Source      string   `json:"source,omitempty"`
	Owner       string   `json:"owner"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Language    string   `json:"language"`
	Topics      []string `json:"topics"`
	Stars       int      `json:"stars"`
	AvatarURL   string   `json:"avatarUrl"`
	URL         string   `json:"url"`

	// The fields below come with a lookup or a search, and are zero in a
	// repository rebuilt from a stored snapshot.
	Forks      int       `json:"forks"`
	OpenIssues int       `json:"openIssues"`
	License    string    `json:"license"`
	Homepage   string    `json:"homepage"`
	Created    time.Time `json:"created"`
	Pushed     time.Time `json:"pushed"`
	Archived   bool      `json:"archived"`
	Fork       bool      `json:"fork"`
}

// Ref returns the repository's name as a RepoRef.
func (r Repository) Ref() RepoRef { return NewRef(r.Source, r.Owner, r.Name) }

// Release is a published version of a repository.
type Release struct {
	Tag        string
	Name       string
	Notes      string
	Published  time.Time
	Prerelease bool
	URL        string
	Assets     []Asset
}

// Asset is a file attached to a release.
type Asset struct {
	// ID is the site's identifier of the file, which its authenticated
	// download endpoint needs. Releases cached before it was recorded have 0.
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	URL       string `json:"url"`
	Digest    string `json:"digest,omitempty"`
	Downloads int    `json:"downloads,omitempty"`
}

// ReleasePage is the newest releases of a repository, newest first. Drafts and
// assets still uploading are already dropped.
type ReleasePage struct {
	Releases    []Release
	ETag        string
	NotModified bool
}

// Allowance is the request allowance of the credentials in use, from the
// rate-limit headers of the latest response.
type Allowance struct {
	// Known is false until a response has reported the allowance.
	Known     bool
	Limit     int
	Remaining int
	Reset     time.Time
	// Authenticated reports whether the requests carried a token.
	Authenticated bool
}
