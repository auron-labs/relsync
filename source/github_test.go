package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorded is a request as the test server saw it.
type recorded struct {
	path   string
	query  string
	header http.Header
}

func newServer(t *testing.T, handler http.HandlerFunc) (*GitHub, *[]recorded) {
	t.Helper()
	var mu sync.Mutex
	var seen []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, recorded{path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone()})
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return NewGitHub(GitHubOptions{BaseURL: server.URL, UserAgent: "Release Sync/test"}), &seen
}

func setLimit(w http.ResponseWriter, limit, remaining int, reset time.Time) {
	w.Header().Set("X-Ratelimit-Resource", "core")
	w.Header().Set("X-Ratelimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-Ratelimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(reset.Unix(), 10))
}

const releasesBody = `[
  {"tag_name":"v1.0.0","name":"One","body":"first","published_at":"2026-11-01T10:00:00Z","html_url":"https://example.test/1","assets":[
     {"id":7,"name":"app.zip","size":10,"browser_download_url":"https://example.test/app.zip","download_count":3,"state":"uploaded","digest":"sha256:abc"},
     {"name":"half.zip","size":1,"browser_download_url":"https://example.test/half.zip","state":"open"}]},
  {"tag_name":"v1.1.0","name":null,"body":null,"published_at":"2026-11-05T10:00:00Z","prerelease":true},
  {"tag_name":"v1.2.0-draft","draft":true,"published_at":null}
]`

func TestRequestsCarryTheDocumentedHeaders(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"login":"octo"}`)) })
	g.SetToken("secret")
	if _, err := g.User(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := (*seen)[0].header
	for name, want := range map[string]string{
		"User-Agent":           "Release Sync/test",
		"Accept":               "application/vnd.github+json",
		"X-Github-Api-Version": "2022-11-28",
		"Authorization":        "Bearer secret",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	g.SetToken("")
	if _, err := g.User(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[1].header.Get("Authorization"); got != "" {
		t.Errorf("anonymous request sent Authorization %q", got)
	}
}

func TestReleasesDropsDraftsAndUploadingAssetsAndSortsNewestFirst(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `W/"abc"`)
		_, _ = w.Write([]byte(releasesBody))
	})
	page, err := g.Releases(t.Context(), RepoRef{Owner: "acme", Repo: "tool"}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := (*seen)[0]
	if got.path != "/repos/acme/tool/releases" || got.query != "per_page=30" {
		t.Errorf("request = %s?%s", got.path, got.query)
	}
	if got.header.Get("If-None-Match") != "" {
		t.Error("an unconditional request sent If-None-Match")
	}
	if page.ETag != `W/"abc"` || page.NotModified {
		t.Errorf("page = %+v", page)
	}
	if len(page.Releases) != 2 || page.Releases[0].Tag != "v1.1.0" || page.Releases[1].Tag != "v1.0.0" {
		t.Fatalf("releases = %+v", page.Releases)
	}
	if !page.Releases[0].Prerelease || page.Releases[0].Notes != "" {
		t.Errorf("prerelease = %+v", page.Releases[0])
	}
	first := page.Releases[1]
	if len(first.Assets) != 1 || first.Assets[0] != (Asset{ID: 7, Name: "app.zip", Size: 10, URL: "https://example.test/app.zip", Digest: "sha256:abc", Downloads: 3}) {
		t.Errorf("assets = %+v", first.Assets)
	}
	if first.Published != time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC) || first.URL != "https://example.test/1" || first.Name != "One" {
		t.Errorf("release = %+v", first)
	}
}

func TestReleasesSendsTheETagAndReportsNotModified(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	page, err := g.Releases(t.Context(), RepoRef{Owner: "acme", Repo: "tool"}, `"v1"`)
	if err != nil {
		t.Fatal(err)
	}
	if !page.NotModified || page.ETag != `"v1"` || len(page.Releases) != 0 {
		t.Errorf("page = %+v", page)
	}
	if got := (*seen)[0].header.Get("If-None-Match"); got != `"v1"` {
		t.Errorf("If-None-Match = %q", got)
	}
}

func TestRepositoryMapsMetadataAndAsksForASmallAvatar(t *testing.T) {
	g, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Tool","description":null,"language":"Go","topics":["cli","go"],
			"stargazers_count":1234,"html_url":"https://github.com/Acme/Tool",
			"owner":{"login":"Acme","avatar_url":"https://avatars.example/u/1?v=4"}}`))
	})
	repo, err := g.Repository(t.Context(), RepoRef{Owner: "acme", Repo: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	want := Repository{
		Owner: "Acme", Name: "Tool", Language: "Go", Topics: []string{"cli", "go"}, Stars: 1234,
		AvatarURL: "https://avatars.example/u/1?s=96&v=4", URL: "https://github.com/Acme/Tool",
	}
	if repo.Owner != want.Owner || repo.Name != want.Name || repo.Description != "" || repo.Language != want.Language ||
		repo.Stars != want.Stars || repo.AvatarURL != want.AvatarURL || repo.URL != want.URL || len(repo.Topics) != 2 {
		t.Errorf("repository = %+v, want %+v", repo, want)
	}
}

func TestAllowanceComesFromEveryCoreResponseIncludingFailures(t *testing.T) {
	reset := time.Unix(1_800_000_000, 0)
	status := http.StatusOK
	g, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		setLimit(w, 60, 41, reset)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	})
	if g.Allowance().Known {
		t.Fatal("allowance known before any request")
	}
	if _, err := g.Repository(t.Context(), RepoRef{Owner: "a", Repo: "b"}); err != nil {
		t.Fatal(err)
	}
	want := Allowance{Known: true, Limit: 60, Remaining: 41, Reset: reset}
	if got := g.Allowance(); got != want {
		t.Errorf("allowance = %+v, want %+v", got, want)
	}

	status = http.StatusNotFound
	g.SetToken("t")
	if g.Allowance().Known {
		t.Error("allowance survived a token change")
	}
	_, _ = g.Repository(t.Context(), RepoRef{Owner: "a", Repo: "b"})
	want.Authenticated = true
	if got := g.Allowance(); got != want {
		t.Errorf("allowance after a failure = %+v, want %+v", got, want)
	}
}

func TestOnlyTheCoreResourceAndOwnTokenUpdateTheAllowance(t *testing.T) {
	g, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			w.Header().Set("X-Ratelimit-Resource", "search")
			w.Header().Set("X-Ratelimit-Limit", "10")
			w.Header().Set("X-Ratelimit-Remaining", "9")
		} else {
			setLimit(w, 5000, 4999, time.Now())
		}
		_, _ = w.Write([]byte(`{"login":"octo"}`))
	})
	g.SetToken("mine")
	if _, err := g.User(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := g.do(t.Context(), request{path: "/search"}); err != nil {
		t.Fatal(err)
	}
	if got := g.Allowance(); got.Limit != 5000 {
		t.Errorf("a search response replaced the core allowance: %+v", got)
	}
	// Validating someone else's token must not report its allowance as ours.
	g.SetToken("")
	if login, err := g.ValidateToken(t.Context(), "other"); err != nil || login != "octo" {
		t.Fatalf("ValidateToken = %q, %v", login, err)
	}
	if g.Allowance().Known {
		t.Errorf("validating a token changed the allowance: %+v", g.Allowance())
	}
}

func TestErrorMapping(t *testing.T) {
	reset := time.Unix(1_800_000_000, 0)
	for _, c := range []struct {
		name    string
		status  int
		headers map[string]string
		token   string
		want    Error
	}{
		{"not found", 404, nil, "", Error{Kind: KindNotFound, Status: 404, Resource: "a/b"}},
		{"token rejected", 401, nil, "bad", Error{Kind: KindTokenRejected, Status: 401, Resource: "a/b", Authenticated: true}},
		{
			"primary limit", 403,
			map[string]string{"X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": strconv.FormatInt(reset.Unix(), 10)},
			"",
			Error{Kind: KindRateLimited, Status: 403, Resource: "a/b", ResetAt: reset},
		},
		{
			"secondary limit", 429,
			map[string]string{"Retry-After": "60"},
			"t",
			Error{Kind: KindRateLimited, Status: 429, Resource: "a/b", Authenticated: true},
		},
		{"plain forbidden", 403, map[string]string{"X-Ratelimit-Remaining": "12"}, "", Error{Kind: KindStatus, Status: 403, Resource: "a/b"}},
		{"server error", 500, nil, "", Error{Kind: KindStatus, Status: 500, Resource: "a/b"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
			})
			g.SetToken(c.token)
			_, err := g.Repository(t.Context(), RepoRef{Owner: "a", Repo: "b"})
			var got *Error
			if !errors.As(err, &got) {
				t.Fatalf("error = %v, want *Error", err)
			}
			if c.want.Kind == KindRateLimited && c.name == "secondary limit" {
				// The wait comes from Retry-After, relative to now.
				if until := time.Until(got.ResetAt); until < 55*time.Second || until > 61*time.Second {
					t.Errorf("ResetAt in %v, want about 60s", until)
				}
				got.ResetAt = time.Time{}
			}
			if got.Kind != c.want.Kind || got.Status != c.want.Status || got.Resource != c.want.Resource ||
				!got.ResetAt.Equal(c.want.ResetAt) || got.Authenticated != c.want.Authenticated {
				t.Errorf("error = %+v, want %+v", *got, c.want)
			}
		})
	}
}

func TestUnreachableAndTimedOut(t *testing.T) {
	// A closed port refuses connections.
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	g := NewGitHub(GitHubOptions{BaseURL: url, UserAgent: "test"})
	_, err := g.Repository(t.Context(), RepoRef{Owner: "a", Repo: "b"})
	var got *Error
	if !errors.As(err, &got) || got.Kind != KindUnreachable {
		t.Errorf("refused connection = %v", err)
	}

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	g = NewGitHub(GitHubOptions{BaseURL: slow.URL, UserAgent: "test", HTTPClient: &http.Client{Timeout: 50 * time.Millisecond}})
	_, err = g.Repository(t.Context(), RepoRef{Owner: "a", Repo: "b"})
	if !errors.As(err, &got) || got.Kind != KindTimedOut {
		t.Errorf("slow server = %v", err)
	}
}

func TestACanceledContextIsNotMappedToANetworkError(t *testing.T) {
	g, _ := newServer(t, func(http.ResponseWriter, *http.Request) {})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := g.Repository(ctx, RepoRef{Owner: "a", Repo: "b"})
	var mapped *Error
	if !errors.Is(err, context.Canceled) || errors.As(err, &mapped) {
		t.Errorf("error = %v, want the context's cancellation", err)
	}
}

func TestValidateToken(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"octo"}`))
	})
	g.SetToken("saved")
	if login, err := g.ValidateToken(t.Context(), "good"); err != nil || login != "octo" {
		t.Fatalf("good token = %q, %v", login, err)
	}
	if got := (*seen)[0].header.Get("Authorization"); got != "Bearer good" {
		t.Errorf("validated with %q, not the candidate token", got)
	}
	var got *Error
	if _, err := g.ValidateToken(t.Context(), "bad"); !errors.As(err, &got) || got.Kind != KindTokenRejected {
		t.Errorf("bad token = %v", err)
	}
	if !g.HasToken() {
		t.Error("validation replaced the saved token")
	}
}

func TestRefreshAllowanceReadsTheRateLimitEndpoint(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		setLimit(w, 60, 60, time.Now().Add(time.Hour))
		_, _ = w.Write([]byte(`{}`))
	})
	if err := g.RefreshAllowance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].path != "/rate_limit" || g.Allowance().Remaining != 60 {
		t.Errorf("path %q, allowance %+v", (*seen)[0].path, g.Allowance())
	}
}

func TestAssetRequestUsesTheAuthenticatedEndpointOnlyWithATokenAndAnID(t *testing.T) {
	g := NewGitHub(GitHubOptions{BaseURL: "https://api.example.test", UserAgent: "Release Sync/test"})
	ref := RepoRef{Owner: "acme", Repo: "tool"}
	asset := Asset{ID: 7, Name: "app.zip", URL: "https://example.test/app.zip"}

	anonymous := g.AssetRequest(ref, asset)
	if anonymous.URL != asset.URL || anonymous.Header.Get("Authorization") != "" {
		t.Errorf("without a token = %+v", anonymous)
	}

	g.SetToken("secret")
	authed := g.AssetRequest(ref, asset)
	if authed.URL != "https://api.example.test/repos/acme/tool/releases/assets/7" ||
		authed.Header.Get("Authorization") != "Bearer secret" || authed.Header.Get("Accept") != "application/octet-stream" {
		t.Errorf("with a token = %+v", authed)
	}

	// A file cached before ids were recorded still downloads, from its public link.
	legacy := g.AssetRequest(ref, Asset{Name: "app.zip", URL: asset.URL})
	if legacy.URL != asset.URL || legacy.Header.Get("Authorization") != "" {
		t.Errorf("without an id = %+v", legacy)
	}
}

func TestSearchRepositoriesAsksForNamesWithoutForksAndReadsTheMatches(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Resource", "search")
		w.Header().Set("X-Ratelimit-Limit", "10")
		w.Header().Set("X-Ratelimit-Remaining", "9")
		_, _ = w.Write([]byte(`{"total_count":1,"items":[{"name":"7zip","description":"Archiver","language":"C++","stargazers_count":900,
			"html_url":"https://github.com/ip7z/7zip","owner":{"login":"ip7z","avatar_url":"https://example.test/a.png"}}]}`))
	})
	found, err := g.SearchRepositories(t.Context(), "7-Zip")
	if err != nil {
		t.Fatal(err)
	}
	req := (*seen)[0]
	if req.path != "/search/repositories" || !strings.Contains(req.query, "q=7-Zip+in%3Aname+fork%3Afalse") {
		t.Errorf("request = %+v", req)
	}
	if len(found) != 1 || found[0].Owner != "ip7z" || found[0].Name != "7zip" || found[0].Stars != 900 || found[0].Description != "Archiver" {
		t.Errorf("found = %+v", found)
	}
	// Search has an allowance of its own; the one shown to the user is not replaced by it.
	if g.Allowance().Known {
		t.Errorf("allowance = %+v", g.Allowance())
	}
}

func TestSearchRepositoriesReportsALimitedSearch(t *testing.T) {
	g, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := g.SearchRepositories(t.Context(), "tool")
	var se *Error
	if !errors.As(err, &se) || se.Kind != KindRateLimited {
		t.Fatalf("error = %v", err)
	}
}
