package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/database"
	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/auron-labs/mygo-framework/secrets/memory"
	"github.com/auron-labs/mygo-framework/state"

	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/discover"
	"github.com/aaronflorey/release-sync/imagecache"
	"github.com/aaronflorey/release-sync/source"
)

// fakeSource answers the catalog from maps and records what it was asked.
type fakeSource struct {
	mu        sync.Mutex
	pages     map[string]source.ReleasePage
	repos     map[string]source.Repository
	failures  map[string]error
	allowance source.Allowance
	// requests counts release requests per repository.
	requests map[string]int
	// published counts publish calls, so each version of a repository has its own ETag.
	published int
	// block, when set, holds every release request until it is closed.
	block chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		pages: map[string]source.ReleasePage{}, repos: map[string]source.Repository{},
		failures: map[string]error{}, requests: map[string]int{},
	}
}

// testOwner owns every repository the tests track.
const testOwner = "acme"

func refKey(ref source.RepoRef) string { return strings.ToLower(ref.String()) }

// publish makes a repository known with the given releases, newest first.
func (f *fakeSource) publish(name, description string, releases ...source.Release) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.ToLower(testOwner + "/" + name)
	f.repos[key] = source.Repository{Owner: testOwner, Name: name, Description: description, Stars: 1234}
	f.published++
	f.pages[key] = source.ReleasePage{Releases: releases, ETag: fmt.Sprintf(`"%s-%d"`, key, f.published)}
}

// publishOwned is publish for a repository of another owner.
func (f *fakeSource) publishOwned(owner, name, description string, releases ...source.Release) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.ToLower(owner + "/" + name)
	f.repos[key] = source.Repository{Owner: owner, Name: name, Description: description, Stars: 1234}
	f.published++
	f.pages[key] = source.ReleasePage{Releases: releases, ETag: fmt.Sprintf(`"%s-%d"`, key, f.published)}
}

func (f *fakeSource) fail(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[strings.ToLower(testOwner+"/"+name)] = err
}

func (f *fakeSource) Repository(_ context.Context, ref source.RepoRef) (source.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures[refKey(ref)]; err != nil {
		return source.Repository{}, err
	}
	repo, ok := f.repos[refKey(ref)]
	if !ok {
		return source.Repository{}, &source.Error{Kind: source.KindNotFound, Status: 404, Resource: ref.String()}
	}
	return repo, nil
}

func (f *fakeSource) Releases(ctx context.Context, ref source.RepoRef, etag string) (source.ReleasePage, error) {
	f.mu.Lock()
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return source.ReleasePage{}, fmt.Errorf("fake source: %w", ctx.Err())
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests[refKey(ref)]++
	if err := f.failures[refKey(ref)]; err != nil {
		return source.ReleasePage{}, err
	}
	page, ok := f.pages[refKey(ref)]
	if !ok {
		return source.ReleasePage{}, &source.Error{Kind: source.KindNotFound, Status: 404, Resource: ref.String()}
	}
	if etag != "" && etag == page.ETag {
		return source.ReleasePage{NotModified: true, ETag: etag}, nil
	}
	return page, nil
}

func (f *fakeSource) Allowance() source.Allowance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allowance
}

func (f *fakeSource) releaseRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, n := range f.requests {
		total += n
	}
	return total
}

var releaseDay = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// release is a release with one Windows setup program, the minimum an Item needs
// to have something to install.
func release(tag string, daysAfter int, notes string) source.Release {
	return source.Release{
		Tag: tag, Name: tag, Notes: notes, Published: releaseDay.AddDate(0, 0, daysAfter),
		Assets: []source.Asset{{Name: "tool-x64-setup.exe", Size: 12 << 20}},
	}
}

// env is a screen with every collaborator replaced by something local.
type env struct {
	t      *testing.T
	screen *screen
	db     *database.Database
	source *fakeSource
	tokens *memory.Store
	// api is the GitHub API the account and token features talk to.
	api    *source.GitHub
	apiHit *fakeAPI
	// discovery is the site Discover reads from.
	discovery *fakeDiscovery

	// avatarRequests counts avatar downloads; avatarArrived receives once per loaded image.
	avatarRequests *atomic.Int32
	avatarArrived  chan struct{}
	// avatarGate holds avatar downloads until it is closed; tests that do not care close it at once.
	avatarGate chan struct{}

	mu       sync.Mutex
	inflight map[string]bool
}

// avatarTransport answers every request with a small PNG.
type avatarTransport struct {
	requests *atomic.Int32
	// gate, when not nil, holds every response until it is closed.
	gate <-chan struct{}
}

func (a avatarTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	a.requests.Add(1)
	if a.gate != nil {
		select {
		case <-a.gate:
		case <-r.Context().Done():
			return nil, fmt.Errorf("avatar download: %w", r.Context().Err())
		}
	}
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode avatar: %w", err)
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/png"}},
		Body: io.NopCloser(&buf), Request: r,
	}, nil
}

// fakeAPI stands in for GitHub's user endpoint and rate limit headers.
type fakeAPI struct {
	mu sync.Mutex
	// users maps a token to its login; other tokens are rejected.
	users map[string]string
	// seenTokens records the Authorization header of each request.
	seenTokens []string
}

func (a *fakeAPI) tokens() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seenTokens...)
}

func (a *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	a.seenTokens = append(a.seenTokens, token)
	w.Header().Set("X-Ratelimit-Resource", "core")
	w.Header().Set("X-Ratelimit-Limit", "60")
	w.Header().Set("X-Ratelimit-Remaining", "55")
	w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	if r.URL.Path == "/rate_limit" {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	login, ok := a.users[token]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = fmt.Fprintf(w, `{"login":%q}`, login)
}

func newEnv(t *testing.T, initial preferences) *env {
	t.Helper()
	e := &env{t: t, source: newFakeSource(), tokens: memory.New(), inflight: map[string]bool{}}
	e.apiHit = &fakeAPI{users: map[string]string{"good-token": "octocat"}}
	server := httptest.NewServer(e.apiHit)
	t.Cleanup(server.Close)
	e.api = source.NewGitHub(source.GitHubOptions{BaseURL: server.URL, UserAgent: "test"})

	dir := t.TempDir()
	db, err := database.Open(t.Context(), filepath.Join(dir, "app.db"), migrations.FS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e.db = db

	manager := jobs.New(t.Context())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	manager.Subscribe(func(st jobs.Status) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if st.State == jobs.Running {
			e.inflight[st.Name] = true
		} else {
			delete(e.inflight, st.Name)
		}
	})
	d := dirs{
		config: dir, cache: dir, downloads: filepath.Join(dir, "downloads"), apps: filepath.Join(dir, "apps"), tools: filepath.Join(dir, "tools"), staging: filepath.Join(dir, "staging"),
		appBackups: filepath.Join(dir, "app-backups"), libraryBackups: filepath.Join(dir, "library-backups"), restore: filepath.Join(dir, "restore-pending"),
	}
	svc, err := openServices(t.Context(), db.SQL, d, manager, e.api, e.source, e.tokens)
	if err != nil {
		t.Fatal(err)
	}
	// Discover reads from a stand-in too.
	e.discovery = newFakeDiscovery(e.source)
	svc.discover = discover.NewService(discover.Options{
		Client: e.discovery, DB: db.SQL, Jobs: manager, Policy: svc.catalog.Policy,
		// Waiting for searches to be free is not worth the time in a test: background work gives up at once.
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	// Avatars come from a local stand-in, never the network.
	svc.avatars.Close()
	e.avatarRequests = &atomic.Int32{}
	e.avatarArrived = make(chan struct{}, 64)
	e.avatarGate = make(chan struct{})
	svc.avatars = imagecache.New(filepath.Join(dir, "avatars"),
		&http.Client{Transport: avatarTransport{requests: e.avatarRequests, gate: e.avatarGate}}, func() { e.avatarArrived <- struct{}{} })
	t.Cleanup(svc.avatars.Close)

	cell := state.NewCell(initial)
	saver := state.Autosave(cell, time.Hour, func(context.Context, preferences) error { return nil })
	t.Cleanup(func() { _ = saver.Close(context.Background()) })
	t.Cleanup(func() {
		select {
		case <-e.avatarGate:
		default:
			close(e.avatarGate)
		}
	})
	e.screen = newScreen(cell, saver, manager, d, svc)
	e.screen.openURL = func(string) error { return nil }
	e.screen.ghToken = func(context.Context) (string, error) { return "", nil }
	return e
}

// settle waits until no job is running.
func (e *env) settle() {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		running := len(e.inflight)
		e.mu.Unlock()
		if running == 0 {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("jobs still running: %v", e.inflight)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
