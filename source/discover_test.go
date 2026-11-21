package source

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSearchSendsTheQueryAndSortsByStarsOnRequest(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":812,"items":[{"name":"tool","stargazers_count":9,"owner":{"login":"acme"}}]}`))
	})
	page, err := g.Search(t.Context(), SearchQuery{Text: "topic:emulator stars:>150", ByStars: true, PerPage: 24, Page: 3})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 812 || len(page.Repositories) != 1 || page.Repositories[0].Ref().String() != "acme/tool" {
		t.Fatalf("page = %+v", page)
	}
	query, err := url.ParseQuery((*seen)[0].query)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"q": "topic:emulator stars:>150", "per_page": "24", "page": "3", "sort": "stars", "order": "desc",
	} {
		if got := query.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	if _, err := g.Search(t.Context(), SearchQuery{Text: "editor", PerPage: 24, Page: 1}); err != nil {
		t.Fatal(err)
	}
	plain, _ := url.ParseQuery((*seen)[1].query)
	if plain.Has("sort") || plain.Has("page") {
		t.Errorf("a plain first-page search carries %v", plain)
	}
}

func TestSearchReportsALimitedSearchAsRateLimited(t *testing.T) {
	g, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := g.Search(t.Context(), SearchQuery{Text: "x", PerPage: 10, Page: 1})
	var se *Error
	if !errors.As(err, &se) || se.Kind != KindRateLimited {
		t.Fatalf("err = %v", err)
	}
}

func TestStarredReadsTheSignedInUsersStarsAndSaysWhenAnotherPageMayFollow(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		count := 100
		if r.URL.Query().Get("page") == "2" {
			count = 3
		}
		items := make([]string, count)
		for i := range items {
			items[i] = fmt.Sprintf(`{"name":"r%d","owner":{"login":"o"}}`, i)
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	})
	g.SetToken("secret")
	first, err := g.Starred(t.Context(), 1)
	if err != nil || len(first.Repositories) != 100 || !first.More {
		t.Fatalf("page 1 = %d repositories, more %v, %v", len(first.Repositories), first.More, err)
	}
	second, err := g.Starred(t.Context(), 2)
	if err != nil || len(second.Repositories) != 3 || second.More {
		t.Fatalf("page 2 = %d repositories, more %v, %v", len(second.Repositories), second.More, err)
	}
	if (*seen)[0].path != "/user/starred" || (*seen)[0].header.Get("Authorization") != "Bearer secret" {
		t.Errorf("request = %+v", (*seen)[0])
	}
}

func TestOwnerRepositoriesLeavesForksOutButKeepsPagingByWhatTheSiteSent(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		items := make([]string, 30)
		for i := range items {
			items[i] = fmt.Sprintf(`{"name":"r%d","fork":%v,"owner":{"login":"acme"}}`, i, i%2 == 0)
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	})
	page, err := g.OwnerRepositories(t.Context(), "acme", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Repositories) != 15 || !page.More {
		t.Fatalf("%d repositories, more %v; want 15 and more", len(page.Repositories), page.More)
	}
	for _, r := range page.Repositories {
		if r.Fork {
			t.Errorf("%s is a fork", r.Name)
		}
	}
	if (*seen)[0].path != "/users/acme/repos" || !strings.Contains((*seen)[0].query, "page=2") {
		t.Errorf("request = %+v", (*seen)[0])
	}
}

func TestReadmeAsksForRawMarkdownAndTreatsAMissingOneAsEmpty(t *testing.T) {
	g, seen := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/none/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("# Title\n" + strings.Repeat("é", maxReadmeBytes)))
	})
	text, err := g.Readme(t.Context(), RepoRef{Owner: "acme", Repo: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "# Title") || len(text) > maxReadmeBytes || !strings.HasSuffix(text, "é") {
		t.Errorf("README is %d bytes, ends %q", len(text), text[max(0, len(text)-4):])
	}
	if got := (*seen)[0].header.Get("Accept"); got != "application/vnd.github.raw+json" {
		t.Errorf("Accept = %q", got)
	}
	text, err = g.Readme(t.Context(), RepoRef{Owner: "acme", Repo: "none"})
	if err != nil || text != "" {
		t.Errorf("missing README = %q, %v", text, err)
	}
}

func TestRepositoryCarriesTheDetailsTheDialogShows(t *testing.T) {
	g, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"tool","forks_count":12,"open_issues_count":3,"homepage":"https://tool.example",
			"archived":true,"created_at":"2024-01-02T03:04:05Z","pushed_at":"2026-10-01T00:00:00Z",
			"license":{"spdx_id":"NOASSERTION","name":"Custom licence"},"owner":{"login":"acme"}}`))
	})
	repo, err := g.Repository(t.Context(), RepoRef{Owner: "acme", Repo: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	if repo.Forks != 12 || repo.OpenIssues != 3 || repo.Homepage != "https://tool.example" || !repo.Archived {
		t.Errorf("repo = %+v", repo)
	}
	if repo.License != "Custom licence" {
		t.Errorf("licence = %q, want the name when the identifier is unknown", repo.License)
	}
	if !repo.Created.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) || repo.Pushed.IsZero() {
		t.Errorf("created %v, pushed %v", repo.Created, repo.Pushed)
	}
}
