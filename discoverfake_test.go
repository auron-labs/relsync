package main

import (
	"context"
	"slices"
	"sync"

	"github.com/aaronflorey/release-sync/source"
)

// fakeDiscovery is the site Discover reads from: searches are answered by a
// function the test sets, and everything else comes from the fake source.
type fakeDiscovery struct {
	*fakeSource

	mu sync.Mutex
	// find answers a search; it finds nothing until a test sets it.
	find func(q source.SearchQuery) source.SearchPage
	// stars are the signed-in user's starred repositories.
	stars []source.Repository
	// owned maps an owner to their repositories.
	owned map[string][]source.Repository
	// readmes maps owner/repo to its README.
	readmes  map[string]string
	hasToken bool
	searches []source.SearchQuery
}

func newFakeDiscovery(src *fakeSource) *fakeDiscovery {
	return &fakeDiscovery{fakeSource: src, owned: map[string][]source.Repository{}, readmes: map[string]string{}, hasToken: true}
}

func (f *fakeDiscovery) HasToken() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hasToken
}

func (f *fakeDiscovery) Search(_ context.Context, q source.SearchQuery) (source.SearchPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches = append(f.searches, q)
	if f.find == nil {
		return source.SearchPage{}, nil
	}
	return f.find(q), nil
}

func (f *fakeDiscovery) searched() []source.SearchQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.searches)
}

func (f *fakeDiscovery) Starred(_ context.Context, page int) (source.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const per = 100
	from := (page - 1) * per
	if from >= len(f.stars) {
		return source.Page{}, nil
	}
	to := min(len(f.stars), from+per)
	return source.Page{Repositories: f.stars[from:to], More: to < len(f.stars)}, nil
}

func (f *fakeDiscovery) OwnerRepositories(_ context.Context, owner string, page int) (source.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const per = 30
	all := f.owned[owner]
	from := (page - 1) * per
	if from >= len(all) {
		return source.Page{}, nil
	}
	to := min(len(all), from+per)
	return source.Page{Repositories: all[from:to], More: to < len(all)}, nil
}

func (f *fakeDiscovery) Readme(_ context.Context, ref source.RepoRef) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readmes[refKey(ref)], nil
}
