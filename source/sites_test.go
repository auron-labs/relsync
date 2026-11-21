package source

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLinkReadsEverySiteAndCutsThePathAtTheProject(t *testing.T) {
	gitlab, codeberg := "gitlab:gitlab.com", "forgejo:codeberg.org"
	for _, c := range []struct {
		text string
		want RepoRef
		ok   bool
	}{
		{"https://gitlab.com/acme/tool", RepoRef{Owner: "acme", Repo: "tool", Source: gitlab}, true},
		{"https://gitlab.com/acme/team/sub/tool/-/releases", RepoRef{Owner: "acme/team/sub", Repo: "tool", Source: gitlab}, true},
		{"gitlab.com/acme/tool.git", RepoRef{Owner: "acme", Repo: "tool", Source: gitlab}, true},
		{"https://www.gitlab.com/acme/tool/-/tags/v1", RepoRef{Owner: "acme", Repo: "tool", Source: gitlab}, true},
		{"git@gitlab.com:acme/team/tool.git", RepoRef{Owner: "acme/team", Repo: "tool", Source: gitlab}, true},
		{"https://codeberg.org/acme/tool/releases/tag/v1.0", RepoRef{Owner: "acme", Repo: "tool", Source: codeberg}, true},
		{"codeberg.org/acme/tool", RepoRef{Owner: "acme", Repo: "tool", Source: codeberg}, true},
		{"https://git.example.com/acme/tool/src/branch/main", RepoRef{Owner: "acme", Repo: "tool", Source: "web:git.example.com"}, true},
		{"https://git.example.com/a/b/c/wiki/Home", RepoRef{Owner: "a/b", Repo: "c", Source: "web:git.example.com"}, true},
		{"http://127.0.0.1:3000/acme/tool", RepoRef{Owner: "acme", Repo: "tool", Source: "web:127.0.0.1:3000"}, true},
		{"https://github.com/cli/cli/pulls", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"cli/cli", RepoRef{Owner: "cli", Repo: "cli"}, true},

		{"https://git.example.com/acme", RepoRef{}, false},
		{"https://git.example.com/issues/12", RepoRef{}, false},
		{"https://gitlab.com/acme/-/releases", RepoRef{}, false},
		{"git.example.com/acme/tool", RepoRef{}, false},
		{"https://git.example.com/../tool", RepoRef{}, false},
		{"two words https://gitlab.com/acme/tool", RepoRef{}, false},
	} {
		got, ok := ParseLink(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseLink(%q) = %+v, %v; want %+v, %v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestParseAddressNeedsASiteSoProseIsNeverARepository(t *testing.T) {
	for _, text := range []string{"read/write", "owner/repo", "and/or"} {
		if ref, ok := ParseAddress(text); ok {
			t.Errorf("ParseAddress(%q) = %+v, want no repository", text, ref)
		}
	}
	if _, ok := ParseAddress("https://github.com/owner/repo"); !ok {
		t.Error("ParseAddress rejected a GitHub address")
	}
}

func TestARegistryReadsAHostItHasSeenAsItsKind(t *testing.T) {
	r := NewRegistry(nil, RegistryOptions{})
	text := "https://git.example.com/acme/tool/issues/4"
	if ref, _ := r.ParseLink(text); !ref.Unresolved() {
		t.Fatalf("an unseen host is already %+v", ref)
	}
	r.Learn("forgejo:git.example.com")
	if got, _ := r.ParseLink(text); got != (RepoRef{Owner: "acme", Repo: "tool", Source: "forgejo:git.example.com"}) {
		t.Errorf("after Learn, ParseLink = %+v", got)
	}
	r.Learn("gitlab:code.example.org")
	if got, _ := r.ParseLink("https://code.example.org/a/b/c"); got != (RepoRef{Owner: "a/b", Repo: "c", Source: "gitlab:code.example.org"}) {
		t.Errorf("a GitLab host keeps its nested groups: %+v", got)
	}
}

// siteServer is a local site; seen holds the requests it answered.
type siteServer struct {
	*httptest.Server
	host string
	mu   sync.Mutex
	seen []*http.Request
}

func newSiteServer(t *testing.T, handler http.HandlerFunc) *siteServer {
	t.Helper()
	s := &siteServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r)
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.host = u.Host
	return s
}

func (s *siteServer) options() SiteOptions {
	return SiteOptions{Host: s.host, BaseURL: s.URL, UserAgent: "test"}
}

func (s *siteServer) last() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[len(s.seen)-1]
}

const gitlabProject = `{"path":"tool","path_with_namespace":"acme/team/tool","description":"A tool","topics":["cli"],
 "star_count":7,"forks_count":2,"open_issues_count":3,"web_url":"https://gitlab.example/acme/team/tool",
 "avatar_url":null,"archived":false,"created_at":"2024-01-02T03:04:05Z","last_activity_at":"2026-10-01T00:00:00Z",
 "license":{"nickname":"MIT","name":"MIT License"},"namespace":{"avatar_url":"/uploads/group.png"}}`

const gitlabReleases = `[
 {"tag_name":"v1.0.0","name":"One","description":"first","released_at":"2026-10-01T10:00:00Z","upcoming_release":false,
  "_links":{"self":"https://gitlab.example/acme/team/tool/-/releases/v1.0.0"},
  "assets":{"links":[
    {"id":1,"name":"tool-1.0.0-windows-amd64.zip","url":"https://cdn.example/t.zip","direct_asset_url":"https://gitlab.example/acme/team/tool/-/releases/v1.0.0/downloads/tool.zip","link_type":"package"},
    {"id":2,"name":"Windows installer","url":"https://gitlab.example/uploads/abc/tool-setup.exe","link_type":"other"},
    {"id":3,"name":"Operations guide","url":"https://gitlab.example/wiki/guide","link_type":"runbook"},
    {"id":4,"name":"registry image","url":"https://registry.example/tool:1","link_type":"image"}]}},
 {"tag_name":"v1.1.0-rc1","name":"RC","description":"","created_at":"2026-10-10T10:00:00Z","upcoming_release":true,"assets":{"links":[]}}
]`

func TestGitLabReadsNestedProjectsReleasesAndLinks(t *testing.T) {
	site := newSiteServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.EscapedPath(), "/releases"):
			w.Header().Set("ETag", `"abc"`)
			_, _ = w.Write([]byte(gitlabReleases))
		default:
			_, _ = w.Write([]byte(gitlabProject))
		}
	})
	g := NewGitLab(site.options())
	ref := RepoRef{Owner: "acme/team", Repo: "tool", Source: SiteKey(GitLabName, site.host)}

	repo, err := g.Repository(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := site.last().URL.EscapedPath(); got != "/api/v4/projects/acme%2Fteam%2Ftool" {
		t.Errorf("the project was asked for at %q: nested groups are part of the encoded path", got)
	}
	if repo.Owner != "acme/team" || repo.Name != "tool" || repo.Stars != 7 || repo.License != "MIT" || repo.Source != ref.Source {
		t.Errorf("repository = %+v", repo)
	}
	if want := site.URL + "/uploads/group.png"; repo.AvatarURL != want {
		t.Errorf("avatar = %q, want %q: a site's relative address is made complete", repo.AvatarURL, want)
	}

	page, err := g.Releases(t.Context(), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.ETag != `"abc"` || len(page.Releases) != 2 {
		t.Fatalf("page = %+v", page)
	}
	rc := page.Releases[0]
	if rc.Tag != "v1.1.0-rc1" || !rc.Prerelease {
		t.Errorf("an upcoming release should be a pre-release, got %+v", rc)
	}
	assets := page.Releases[1].Assets
	names, links := make([]string, len(assets)), make([]string, len(assets))
	for i, a := range assets {
		names[i], links[i] = a.Name, a.URL
	}
	if strings.Join(names, ",") != "tool-1.0.0-windows-amd64.zip,tool-setup.exe" {
		t.Errorf("asset names = %v: a label is replaced by the file's name, runbooks and images are dropped", names)
	}
	if !strings.HasSuffix(links[0], "/downloads/tool.zip") {
		t.Errorf("the direct asset link should win, got %q", links[0])
	}
}

func TestGitLabAsksConditionallyAndKeepsCachedReleasesWhenNothingChanged(t *testing.T) {
	site := newSiteServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	page, err := NewGitLab(site.options()).Releases(t.Context(), RepoRef{Owner: "a", Repo: "b"}, `"abc"`)
	if err != nil || !page.NotModified || page.ETag != `"abc"` {
		t.Errorf("Releases = %+v, %v; want not modified", page, err)
	}
}

func TestGitLabReadsTheAllowanceAndStopsAtALimit(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	var limited bool
	site := newSiteServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Ratelimit-Limit", "500")
		if limited {
			w.Header().Set("Ratelimit-Remaining", "0")
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Ratelimit-Remaining", "498")
		w.Header().Set("Ratelimit-Reset", strconv.FormatInt(reset, 10))
		_, _ = w.Write([]byte(gitlabProject))
	})
	g := NewGitLab(site.options())
	ref := RepoRef{Owner: "a", Repo: "b"}
	if _, err := g.Repository(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if a := g.Allowance(); !a.Known || a.Limit != 500 || a.Remaining != 498 || a.Reset.Unix() != reset || a.Authenticated {
		t.Errorf("allowance = %+v", a)
	}
	if got := site.last().Header.Get("Authorization") + site.last().Header.Get("Private-Token"); got != "" {
		t.Errorf("a request carried credentials %q", got)
	}

	limited = true
	_, err := g.Repository(t.Context(), ref)
	var se *Error
	if !errors.As(err, &se) || se.Kind != KindRateLimited || se.Site != hostLabel(site.host) {
		t.Fatalf("err = %v, want a rate limit of this site", err)
	}
	if wait := time.Until(se.ResetAt); wait < 20*time.Second || wait > 40*time.Second {
		t.Errorf("reset in %v, want about the 30 seconds Retry-After gave", wait)
	}
}

const forgejoRepo = `{"name":"tool","description":"A tool","language":"Go","topics":["cli"],"stars_count":9,"forks_count":1,
 "open_issues_count":2,"html_url":"https://codeberg.example/acme/tool","website":"https://tool.example","archived":false,
 "fork":false,"created_at":"2025-01-02T03:04:05Z","updated_at":"2026-10-01T00:00:00Z",
 "owner":{"login":"acme","avatar_url":"https://codeberg.example/avatars/acme"}}`

const forgejoReleases = `[
 {"tag_name":"v2.0.0","name":"Two","body":"notes","draft":false,"prerelease":false,"published_at":"2026-10-02T10:00:00Z",
  "html_url":"https://codeberg.example/acme/tool/releases/tag/v2.0.0",
  "assets":[{"id":5,"name":"tool-windows-x64.zip","size":1234,"download_count":4,"browser_download_url":"https://codeberg.example/attachments/x"}]},
 {"tag_name":"v2.1.0","draft":true,"published_at":"2026-10-03T10:00:00Z"},
 {"tag_name":"v1.0.0","prerelease":true,"created_at":"2026-01-01T00:00:00Z","assets":[]}
]`

func TestForgejoReadsRepositoriesAndReleases(t *testing.T) {
	site := newSiteServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases") {
			_, _ = w.Write([]byte(forgejoReleases))
			return
		}
		_, _ = w.Write([]byte(forgejoRepo))
	})
	f := NewForgejo(site.options())
	ref := RepoRef{Owner: "acme", Repo: "tool", Source: SiteKey(ForgejoName, site.host)}
	repo, err := f.Repository(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Owner != "acme" || repo.Stars != 9 || repo.Language != "Go" || repo.Homepage != "https://tool.example" || repo.Source != ref.Source {
		t.Errorf("repository = %+v", repo)
	}
	page, err := f.Releases(t.Context(), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Releases) != 2 || page.Releases[0].Tag != "v2.0.0" || !page.Releases[1].Prerelease {
		t.Fatalf("releases = %+v: the draft is dropped and the newest comes first", page.Releases)
	}
	if a := page.Releases[0].Assets; len(a) != 1 || a[0].Size != 1234 || a[0].Downloads != 4 || a[0].ID != 5 {
		t.Errorf("assets = %+v", a)
	}
}

func TestForgejoReportsAMissingRepositoryAndALimit(t *testing.T) {
	status := http.StatusNotFound
	site := newSiteServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(status)
	})
	f := NewForgejo(site.options())
	ref := RepoRef{Owner: "a", Repo: "b"}
	var se *Error
	if _, err := f.Repository(t.Context(), ref); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("a missing repository: %v", err)
	}
	status = http.StatusTooManyRequests
	if _, err := f.Releases(t.Context(), ref, ""); !errors.As(err, &se) || se.Kind != KindRateLimited || se.ResetAt.IsZero() {
		t.Errorf("a limit: %v", err)
	}
	status = http.StatusInternalServerError
	if _, err := f.Releases(t.Context(), ref, ""); !errors.As(err, &se) || se.Kind != KindStatus || se.Status != 500 {
		t.Errorf("a server error: %v", err)
	}
}

// testRegistry builds a registry whose sites are the given local servers.
func testRegistry() *Registry {
	return NewRegistry(NewGitHub(GitHubOptions{BaseURL: "http://127.0.0.1:1", UserAgent: "test"}), RegistryOptions{
		UserAgent: "test", BaseURL: func(host string) string { return "http://" + host },
	})
}

func TestARegistryFindsOutWhatAnUnfamiliarHostRuns(t *testing.T) {
	forgejo := newSiteServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/acme/tool" {
			_, _ = w.Write([]byte(forgejoRepo))
			return
		}
		http.NotFound(w, r)
	})
	gitlab := newSiteServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/api/v4/projects/acme%2Fteam%2Ftool" {
			_, _ = w.Write([]byte(gitlabProject))
			return
		}
		// A site that answers every address with a page, as a single-page app does.
		_, _ = w.Write([]byte(`<html></html>`))
	})
	neither := newSiteServer(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	r := testRegistry()

	repo, err := r.Repository(t.Context(), RepoRef{Owner: "acme", Repo: "tool", Source: "web:" + forgejo.host})
	if err != nil || repo.Source != "forgejo:"+forgejo.host {
		t.Fatalf("Forgejo host: %+v, %v", repo, err)
	}
	repo, err = r.Repository(t.Context(), RepoRef{Owner: "acme/team", Repo: "tool", Source: "web:" + gitlab.host})
	if err != nil || repo.Source != "gitlab:"+gitlab.host || repo.Owner != "acme/team" {
		t.Fatalf("GitLab host: %+v, %v", repo, err)
	}
	_, err = r.Repository(t.Context(), RepoRef{Owner: "acme", Repo: "tool", Source: "web:" + neither.host})
	var se *Error
	if !errors.As(err, &se) || se.Kind != KindNoProject {
		t.Fatalf("a host that runs neither: %v, want KindNoProject", err)
	}

	// Once found, the host's addresses are read as its kind and its releases come from its client.
	ref, _ := r.ParseLink("http://" + forgejo.host + "/acme/tool")
	if ref.Source != "forgejo:"+forgejo.host {
		t.Errorf("ParseLink after detection = %+v", ref)
	}
}

func TestARegistryDoesNotTakeAnUnreachableHostForAMissingProject(t *testing.T) {
	dead := newSiteServer(t, func(http.ResponseWriter, *http.Request) {})
	host := dead.host
	dead.Close()
	_, err := testRegistry().Repository(t.Context(), RepoRef{Owner: "a", Repo: "b", Source: "web:" + host})
	var se *Error
	if !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("err = %v, want KindUnreachable", err)
	}
}

func TestARegistrySendsRequestsToTheSiteOfTheRepository(t *testing.T) {
	site := newSiteServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases") {
			_, _ = w.Write([]byte(forgejoReleases))
			return
		}
		_, _ = w.Write([]byte(forgejoRepo))
	})
	r := testRegistry()
	ref := RepoRef{Owner: "acme", Repo: "tool", Source: SiteKey(ForgejoName, site.host)}
	page, err := r.Releases(t.Context(), ref, "")
	if err != nil || len(page.Releases) != 2 {
		t.Fatalf("Releases = %+v, %v", page, err)
	}
	if got := site.last().URL.Path; got != "/api/v1/repos/acme/tool/releases" {
		t.Errorf("asked %q", got)
	}
	if a := r.AllowanceFor(ref); a.Authenticated {
		t.Errorf("allowance = %+v", a)
	}
	if got := r.AssetRequest(ref, Asset{URL: site.URL + "/f"}); got.URL != site.URL+"/f" || got.Header.Get("User-Agent") != "test" {
		t.Errorf("asset request = %+v", got)
	}
}

func TestLinkFileNameDropsDirectoriesFromALabel(t *testing.T) {
	for label, want := range map[string]string{
		`../../x.exe`:         "x.exe",
		`..\..\Startup\x.exe`: "x.exe",
		"tool-x64.zip":        "tool-x64.zip",
	} {
		if got := linkFileName(label, "https://example.test/dl"); got != want {
			t.Errorf("linkFileName(%q) = %q, want %q", label, got, want)
		}
	}
}
