package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// RegistryOptions configure a Registry.
type RegistryOptions struct {
	// UserAgent identifies the product to the sites.
	UserAgent string
	// HTTPClient is used for every site but GitHub; the default has a timeout.
	HTTPClient *http.Client
	// BaseURL is where a site's API is reached; the default is https://host.
	// Tests point it at local servers.
	BaseURL func(host string) string
}

// client is a Source that also says how to fetch the files of its site.
type client interface {
	Source
	AssetRequest(ref RepoRef, a Asset) AssetRequest
}

// Registry is the Source of the whole Library: it sends each request to the
// client of the site the repository is on, making clients as sites turn up. It
// also finds out what an unfamiliar host runs. It is safe for concurrent use.
type Registry struct {
	github *GitHub
	opts   RegistryOptions

	mu      sync.Mutex
	clients map[string]client
	// kinds is the kind of each host seen so far, which an address on it needs.
	kinds map[string]string
}

var _ Source = (*Registry)(nil)

// NewRegistry returns a Registry that sends GitHub's requests to github.
func NewRegistry(github *GitHub, options RegistryOptions) *Registry {
	return &Registry{github: github, opts: options, clients: map[string]client{}, kinds: map[string]string{}}
}

// Learn records that the site with the given key is in use, so addresses on
// its host are read as its kind.
func (r *Registry) Learn(key string) {
	kind, host := SplitSite(key)
	if kind == GitHubName || kind == unknownName {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds[host] = kind
}

// ParseLink is ParseLink knowing the hosts that are in use.
func (r *Registry) ParseLink(text string) (RepoRef, bool) {
	ref, ok := ParseLink(text)
	return r.refine(ref), ok
}

// ParseAddress is ParseAddress knowing the hosts that are in use.
func (r *Registry) ParseAddress(text string) (RepoRef, bool) {
	ref, ok := ParseAddress(text)
	return r.refine(ref), ok
}

// refine gives a repository on a host seen before the kind of that host.
func (r *Registry) refine(ref RepoRef) RepoRef {
	if !ref.Unresolved() {
		return ref
	}
	host := ref.Host()
	r.mu.Lock()
	kind, known := r.kinds[host]
	r.mu.Unlock()
	switch {
	case !known:
		return ref
	case kind == ForgejoName:
		// A Forgejo project is always owner/repo; the rest of the path is not it.
		segments := strings.Split(ref.Owner+"/"+ref.Repo, "/")
		return NewRef(SiteKey(kind, host), segments[0], segments[1])
	}
	return NewRef(SiteKey(kind, host), ref.Owner, ref.Repo)
}

// Repository returns the repository's metadata. A repository on a host of
// unknown kind is looked for as a Forgejo project, then a GitLab one.
func (r *Registry) Repository(ctx context.Context, ref RepoRef) (Repository, error) {
	if ref.Unresolved() {
		return r.detect(ctx, ref)
	}
	c, err := r.client(ref.SourceKey())
	if err != nil {
		return Repository{}, err
	}
	return c.Repository(ctx, ref) //nolint:wrapcheck // The client's *Error names the site and the repository.
}

// Releases returns the newest releases of the repository.
func (r *Registry) Releases(ctx context.Context, ref RepoRef, etag string) (ReleasePage, error) {
	if ref.Unresolved() {
		repo, err := r.detect(ctx, ref)
		if err != nil {
			return ReleasePage{}, err
		}
		ref = repo.Ref()
	}
	c, err := r.client(ref.SourceKey())
	if err != nil {
		return ReleasePage{}, err
	}
	return c.Releases(ctx, ref, etag) //nolint:wrapcheck // The client's *Error names the site and the repository.
}

// Allowance is GitHub's, which is the one the application shows.
func (r *Registry) Allowance() Allowance { return r.github.Allowance() }

// AllowanceFor is the allowance of the site the repository is on, as far as
// its responses have told.
func (r *Registry) AllowanceFor(ref RepoRef) Allowance {
	if IsGitHub(ref.Source) {
		return r.github.Allowance()
	}
	r.mu.Lock()
	c, ok := r.clients[ref.Source]
	r.mu.Unlock()
	if !ok {
		return Allowance{}
	}
	return c.Allowance()
}

// AssetRequest says how to fetch a release file of the repository.
func (r *Registry) AssetRequest(ref RepoRef, a Asset) AssetRequest {
	if IsGitHub(ref.Source) {
		return r.github.AssetRequest(ref, a)
	}
	r.mu.Lock()
	c, ok := r.clients[ref.Source]
	r.mu.Unlock()
	if !ok {
		return AssetRequest{URL: a.URL, Header: http.Header{"User-Agent": {r.opts.UserAgent}}}
	}
	return c.AssetRequest(ref, a)
}

func normalKey(key string) string {
	if IsGitHub(key) {
		return GitHubName
	}
	return key
}

// client returns the client of the site, making it on first use.
func (r *Registry) client(key string) (client, error) {
	key = normalKey(key)
	if key == GitHubName {
		return r.github, nil
	}
	r.mu.Lock()
	c, ok := r.clients[key]
	r.mu.Unlock()
	if ok {
		return c, nil
	}
	kind, host := SplitSite(key)
	made, err := r.newClient(kind, host)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[key]; ok {
		return c, nil
	}
	r.clients[key] = made
	r.kinds[host] = kind
	return made, nil
}

func (r *Registry) newClient(kind, host string) (client, error) {
	options := SiteOptions{Host: host, UserAgent: r.opts.UserAgent, HTTPClient: r.opts.HTTPClient}
	if r.opts.BaseURL != nil {
		options.BaseURL = r.opts.BaseURL(host)
	}
	switch kind {
	case GitLabName:
		return NewGitLab(options), nil
	case ForgejoName:
		return NewForgejo(options), nil
	}
	return nil, fmt.Errorf("no client for %s sites (%s)", kind, host)
}

// detect asks the host for the project as a Forgejo site would have it, then a
// GitLab one, and returns the metadata from the one that has it.
func (r *Registry) detect(ctx context.Context, ref RepoRef) (Repository, error) {
	host := ref.Host()
	segments := strings.Split(ref.Owner+"/"+ref.Repo, "/")
	var kinds []string
	// A Forgejo project is always owner/repo; only GitLab nests groups.
	if len(segments) == 2 {
		kinds = append(kinds, ForgejoName)
	}
	kinds = append(kinds, GitLabName)
	for _, kind := range kinds {
		probe, err := r.newClient(kind, host)
		if err != nil {
			return Repository{}, err
		}
		repo, err := probe.Repository(ctx, NewRef(SiteKey(kind, host), ref.Owner, ref.Repo))
		switch {
		case err == nil:
			r.Learn(repo.Source)
			return repo, nil
		case ctx.Err() != nil || stopsDetection(err):
			return Repository{}, err //nolint:wrapcheck // The client's *Error names the site and the repository.
		}
	}
	return Repository{}, &Error{Kind: KindNoProject, Site: hostLabel(host), Resource: ref.Owner + "/" + ref.Repo}
}

// stopsDetection reports a failure that says nothing about what the host
// runs: the host cannot be reached, or refuses for lack of allowance. Any
// other failure means the host does not have the project as that kind of site.
func stopsDetection(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Kind == KindUnreachable || e.Kind == KindTimedOut || e.Kind == KindRateLimited
}
