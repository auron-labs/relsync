package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub answers the gist API from memory, newest gist first as GitHub
// lists them.
type fakeGitHub struct {
	mu     sync.Mutex
	gists  []*fakeGist
	tokens map[string]bool
	// requests records "METHOD path" of every call, and authorization the
	// header of each.
	requests      []string
	authorization []string
	// fail, when set, answers every request with that status.
	fail    int
	headers map[string]string
	// truncate serves file content cut short, with a raw address for the rest.
	truncate bool
	server   *httptest.Server
}

type fakeGist struct {
	ID          string
	Description string
	Public      bool
	Files       map[string]string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{tokens: map[string]bool{"good-token": true}}
	f.server = httptest.NewServer(f)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.authorization = append(f.authorization, r.Header.Get("Authorization"))
	for k, v := range f.headers {
		w.Header().Set(k, v)
	}
	if r.URL.Path == "/raw/"+"big" {
		_, _ = io.WriteString(w, f.fullText())
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case f.fail != 0:
		w.WriteHeader(f.fail)
		return
	case !f.tokens[token]:
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && path == "gists":
		f.list(w, r)
	case r.Method == http.MethodPost && path == "gists":
		var body struct {
			Description string                       `json:"description"`
			Public      bool                         `json:"public"`
			Files       map[string]map[string]string `json:"files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		g := &fakeGist{ID: "gist" + strconv.Itoa(len(f.gists)+1), Description: body.Description, Public: body.Public, Files: map[string]string{}}
		for name, file := range body.Files {
			g.Files[name] = file["content"]
		}
		f.gists = append([]*fakeGist{g}, f.gists...)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": g.ID})
	case strings.HasPrefix(path, "gists/"):
		f.one(w, r, strings.TrimPrefix(path, "gists/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeGitHub) fullText() string {
	for _, g := range f.gists {
		for _, text := range g.Files {
			return text
		}
	}
	return ""
}

func (f *fakeGitHub) list(w http.ResponseWriter, r *http.Request) {
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	var out []map[string]any
	for i, g := range f.gists {
		if i/perPage != page-1 {
			continue
		}
		files := map[string]any{}
		for name := range g.Files {
			files[name] = map[string]any{"filename": name}
		}
		out = append(out, map[string]any{"id": g.ID, "files": files})
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeGitHub) one(w http.ResponseWriter, r *http.Request, id string) {
	var found *fakeGist
	for _, g := range f.gists {
		if g.ID == id {
			found = g
		}
	}
	if found == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		files := map[string]any{}
		for name, text := range found.Files {
			file := map[string]any{"content": text}
			if f.truncate {
				file = map[string]any{"content": text[:10], "truncated": true, "raw_url": f.server.URL + "/raw/big"}
			}
			files[name] = file
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "files": files})
	case http.MethodPatch:
		var body struct {
			Files map[string]map[string]string `json:"files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for name, file := range body.Files {
			found.Files[name] = file["content"]
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	}
}

func (f *fakeGitHub) gist(t *testing.T, token string) *Gist {
	t.Helper()
	g, err := NewGist(GistOptions{BaseURL: f.server.URL + "/", Token: token, App: "release-sync", Description: "Release Sync library", UserAgent: "test/1"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestTheGistIsMadeOnTheFirstWriteAndFoundByNameAfterwards(t *testing.T) {
	github := newFakeGitHub(t)
	ctx := context.Background()
	first := github.gist(t, "good-token")

	if _, found, err := first.Read(ctx); err != nil || found {
		t.Fatalf("Read before any gist = %v, %v, want nothing", found, err)
	}
	if err := first.Write(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	if first.ID() == "" {
		t.Fatal("the gist's id was not kept")
	}
	if len(github.gists) != 1 || github.gists[0].Public || github.gists[0].Description != "Release Sync library" {
		t.Errorf("gist made = %+v, want one secret gist with the description", github.gists)
	}
	if _, ok := github.gists[0].Files["release-sync-library.json"]; !ok {
		t.Errorf("files = %v, want release-sync-library.json", github.gists[0].Files)
	}

	// Another device has no id; it finds the gist by the file name.
	second := github.gist(t, "good-token")
	text, found, err := second.Read(ctx)
	if err != nil || !found || text != "hello" {
		t.Fatalf("second device Read = %q, %v, %v", text, found, err)
	}
	if second.ID() != first.ID() {
		t.Errorf("second device uses gist %q, want %q", second.ID(), first.ID())
	}
	if err := second.Write(ctx, "changed"); err != nil {
		t.Fatal(err)
	}
	if len(github.gists) != 1 || github.gists[0].Files["release-sync-library.json"] != "changed" {
		t.Errorf("writing again made a second gist or did not update the first: %+v", github.gists)
	}
}

func TestTheGistIsFoundOnALaterPageOfTheUsersGists(t *testing.T) {
	github := newFakeGitHub(t)
	github.gists = append(github.gists, &fakeGist{ID: "mine", Files: map[string]string{"release-sync-library.json": "found me"}})
	for i := range 150 {
		github.gists = append([]*fakeGist{{ID: fmt.Sprint("other", i), Files: map[string]string{"notes.txt": "x"}}}, github.gists...)
	}
	text, found, err := github.gist(t, "good-token").Read(context.Background())
	if err != nil || !found || text != "found me" {
		t.Errorf("Read = %q, %v, %v", text, found, err)
	}
}

func TestAGistWithoutOurFileCountsAsNothingStored(t *testing.T) {
	github := newFakeGitHub(t)
	github.gists = []*fakeGist{{ID: "g1", Files: map[string]string{"release-sync-library.json": "x"}}}
	g, err := NewGist(GistOptions{BaseURL: github.server.URL + "/", Token: "good-token", ID: "g1", App: "other-app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := g.Read(context.Background()); err != nil || found {
		t.Errorf("Read = %v, %v, want nothing stored", found, err)
	}
}

func TestAFileTooLargeForTheListingIsReadInFull(t *testing.T) {
	github := newFakeGitHub(t)
	github.gists = []*fakeGist{{ID: "g1", Files: map[string]string{"release-sync-library.json": strings.Repeat("0123456789", 5)}}}
	github.truncate = true
	g, err := NewGist(GistOptions{BaseURL: github.server.URL + "/", Token: "good-token", ID: "g1", App: "release-sync"})
	if err != nil {
		t.Fatal(err)
	}
	text, found, err := g.Read(context.Background())
	if err != nil || !found || len(text) != 50 {
		t.Fatalf("Read = %d bytes, %v, %v, want the whole file", len(text), found, err)
	}
	if last := github.authorization[len(github.authorization)-1]; last != "" {
		t.Errorf("the token was sent to the raw address: %q", last)
	}
}

func TestRequestsCarryTheTokenAndNameTheApplication(t *testing.T) {
	github := newFakeGitHub(t)
	if err := github.gist(t, "good-token").Write(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got := github.authorization[0]; got != "Bearer good-token" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestEachFailureOfGitHubIsTold(t *testing.T) {
	var target *StatusError
	for name, c := range map[string]struct {
		status  int
		headers map[string]string
		want    error
	}{
		"a token GitHub does not accept": {status: 401, want: ErrTokenRejected},
		"the hourly limit":               {status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0"}, want: ErrRateLimited},
		"too many requests":              {status: 429, want: ErrRateLimited},
		"a token without the permission": {status: 403, want: ErrGistInaccessible},
		"a gist that is gone":            {status: 404, want: ErrGistInaccessible},
	} {
		t.Run(name, func(t *testing.T) {
			github := newFakeGitHub(t)
			github.fail, github.headers = c.status, c.headers
			err := github.gist(t, "good-token").Write(context.Background(), "x")
			if !errors.Is(err, c.want) {
				t.Errorf("error = %v, want %v", err, c.want)
			}
		})
	}
	t.Run("another status", func(t *testing.T) {
		github := newFakeGitHub(t)
		github.fail = 500
		err := github.gist(t, "good-token").Write(context.Background(), "x")
		if !errors.As(err, &target) || target.Status != 500 {
			t.Errorf("error = %v, want a StatusError for 500", err)
		}
	})
	t.Run("a token that is rejected on read", func(t *testing.T) {
		github := newFakeGitHub(t)
		_, _, err := github.gist(t, "revoked").Read(context.Background())
		if !errors.Is(err, ErrTokenRejected) {
			t.Errorf("error = %v, want ErrTokenRejected", err)
		}
	})
}

func TestNoTokenMeansNoGist(t *testing.T) {
	if _, err := NewGist(GistOptions{App: "x"}); !errors.Is(err, ErrNoToken) {
		t.Errorf("error = %v, want ErrNoToken", err)
	}
}

func TestAnUnreachableOrSlowGitHubIsTold(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		github := newFakeGitHub(t)
		g := github.gist(t, "good-token")
		github.server.Close()
		if _, _, err := g.Read(context.Background()); !errors.Is(err, ErrUnreachable) {
			t.Errorf("error = %v, want ErrUnreachable", err)
		}
	})
	t.Run("slow", func(t *testing.T) {
		slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(300 * time.Millisecond) }))
		t.Cleanup(slow.Close)
		g, err := NewGist(GistOptions{BaseURL: slow.URL + "/", Token: "t", App: "x", Client: &http.Client{Timeout: 30 * time.Millisecond}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := g.Read(context.Background()); !errors.Is(err, ErrTimedOut) {
			t.Errorf("error = %v, want ErrTimedOut", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		github := newFakeGitHub(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := github.gist(t, "good-token").Read(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want the cancellation", err)
		}
	})
}

func TestTwoDevicesSyncThroughAGist(t *testing.T) {
	github := newFakeGitHub(t)
	pc := newDevice(t, "pc", "Windows", github.gist(t, "good-token"))
	mac := newDevice(t, "mac", "macOS", github.gist(t, "good-token"))
	pc.add("neovim", "neovim", at(0))
	pc.mustSync(at(1))
	mac.mustSync(at(2))
	equalStrings(t, "mac items", mac.itemKeys(), []string{"neovim/neovim"})

	mac.remove("neovim", "neovim", at(3))
	mac.mustSync(at(4))
	pc.mustSync(at(5))
	equalStrings(t, "pc items", pc.itemKeys(), []string{})
	if len(github.gists) != 1 {
		t.Errorf("%d gists were made, want one shared", len(github.gists))
	}
}
