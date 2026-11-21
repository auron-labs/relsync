package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// GitHubAPI is the address of GitHub's API.
const GitHubAPI = "https://api.github.com/"

const (
	// gistPageSize and gistPages bound the search for the sync gist among the
	// user's gists: five pages of a hundred.
	gistPageSize = 100
	gistPages    = 5
	// maxResponse bounds what is read of one response.
	maxResponse = 32 << 20
	gistTimeout = 30 * time.Second
)

// The ways talking to GitHub fails.
var (
	// ErrNoToken means there is no GitHub token to sync with.
	ErrNoToken = errors.New("syncing through a gist needs a GitHub token")
	// ErrTokenRejected means GitHub did not accept the token.
	ErrTokenRejected = errors.New("GitHub didn't accept the token")
	// ErrRateLimited means the request allowance is used up.
	ErrRateLimited = errors.New("GitHub's request limit is used up")
	// ErrGistInaccessible means GitHub would not open the gist: the token may
	// lack the permission to use gists, or the gist is gone.
	ErrGistInaccessible = errors.New("GitHub wouldn't open the sync gist")
	// ErrUnreachable means GitHub could not be reached.
	ErrUnreachable = errors.New("couldn't reach GitHub")
	// ErrTimedOut means GitHub took too long to answer.
	ErrTimedOut = errors.New("GitHub took too long to answer")
)

// StatusError is an answer from GitHub that is none of the errors above.
type StatusError struct {
	Status int
}

func (e *StatusError) Error() string { return "GitHub answered " + strconv.Itoa(e.Status) }

// GistOptions say which gist to keep the text in.
type GistOptions struct {
	// Client sends the requests; nil makes one with a timeout.
	Client *http.Client
	// BaseURL is GitHub's API, ending in a slash; empty is GitHubAPI. Tests
	// point it elsewhere.
	BaseURL   string
	UserAgent string
	// Token is a GitHub token that may read and write gists.
	Token string
	// ID is the gist, "" to look for one made earlier and make one on the
	// first write.
	ID string
	// App names the file in the gist, and Description the gist.
	App, Description string
}

// Gist keeps the text in one file of a private (secret) GitHub gist of the
// user's, found by the file's name among their gists or made on the first
// write.
type Gist struct {
	opts GistOptions
	base *url.URL

	mu sync.Mutex
	id string
}

var _ Transport = (*Gist)(nil)

// NewGist returns the Gist the options describe.
func NewGist(o GistOptions) (*Gist, error) {
	if o.Token == "" {
		return nil, ErrNoToken
	}
	if o.BaseURL == "" {
		o.BaseURL = GitHubAPI
	}
	base, err := url.Parse(o.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("read the GitHub address %q: %w", o.BaseURL, err)
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: gistTimeout}
	}
	return &Gist{opts: o, base: base, id: o.ID}, nil
}

// ID is the gist's id, "" until one was found or made.
func (g *Gist) ID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.id
}

func (g *Gist) fileName() string { return g.opts.App + "-library.json" }

// Read returns the file's text, and false when there is no gist, or no such
// file in it, yet.
func (g *Gist) Read(ctx context.Context) (string, bool, error) {
	id := g.ID()
	if id == "" {
		found, err := g.find(ctx)
		if err != nil || found == "" {
			return "", false, err
		}
		id = found
		g.mu.Lock()
		g.id = id
		g.mu.Unlock()
	}
	var gist struct {
		Files map[string]gistFile `json:"files"`
	}
	if err := g.call(ctx, http.MethodGet, "gists/"+url.PathEscape(id), nil, &gist); err != nil {
		return "", false, err
	}
	file, ok := gist.Files[g.fileName()]
	if !ok {
		return "", false, nil
	}
	if file.Truncated && file.RawURL != "" {
		text, err := g.raw(ctx, file.RawURL)
		return text, err == nil, err
	}
	return file.Content, true, nil
}

type gistFile struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	RawURL    string `json:"raw_url"`
}

// find looks through the user's gists for the one with the sync file.
func (g *Gist) find(ctx context.Context) (string, error) {
	for page := 1; page <= gistPages; page++ {
		var gists []struct {
			ID    string              `json:"id"`
			Files map[string]gistFile `json:"files"`
		}
		path := fmt.Sprintf("gists?per_page=%d&page=%d", gistPageSize, page)
		if err := g.call(ctx, http.MethodGet, path, nil, &gists); err != nil {
			return "", err
		}
		for _, gist := range gists {
			if _, ok := gist.Files[g.fileName()]; ok {
				return gist.ID, nil
			}
		}
		if len(gists) < gistPageSize {
			break
		}
	}
	return "", nil
}

// Write replaces the file's text, making the gist first when there is none.
func (g *Gist) Write(ctx context.Context, text string) error {
	files := map[string]any{g.fileName(): map[string]string{"content": text}}
	if id := g.ID(); id != "" {
		return g.call(ctx, http.MethodPatch, "gists/"+url.PathEscape(id), map[string]any{"files": files}, nil)
	}
	var made struct {
		ID string `json:"id"`
	}
	body := map[string]any{"description": g.opts.Description, "public": false, "files": files}
	if err := g.call(ctx, http.MethodPost, "gists", body, &made); err != nil {
		return err
	}
	g.mu.Lock()
	g.id = made.ID
	g.mu.Unlock()
	return nil
}

// call sends one request to the API and decodes the answer into out.
func (g *Gist) call(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the request to GitHub: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	address, err := g.base.Parse(path)
	if err != nil {
		return fmt.Errorf("address the request to GitHub: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, address.String(), payload)
	if err != nil {
		return fmt.Errorf("make the request to GitHub: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.opts.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", "2022-11-28")
	if g.opts.UserAgent != "" {
		req.Header.Set("User-Agent", g.opts.UserAgent)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	data, err := g.send(req)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("read GitHub's answer: %w", err)
	}
	return nil
}

// raw fetches the whole of a file the gist's answer cut short. The address is
// one GitHub gave and serves secret gists to anyone who has it, so the token
// is not sent.
func (g *Gist) raw(ctx context.Context, address string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("make the request to GitHub: %w", err)
	}
	if g.opts.UserAgent != "" {
		req.Header.Set("User-Agent", g.opts.UserAgent)
	}
	data, err := g.send(req)
	return string(data), err
}

func (g *Gist) send(req *http.Request) ([]byte, error) {
	res, err := g.opts.Client.Do(req)
	if err != nil {
		return nil, transportError(req.Context(), err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return nil, transportError(req.Context(), err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, statusFailure(res)
	}
	return data, nil
}

// transportError says why a request got no answer.
func transportError(ctx context.Context, err error) error {
	var timeout net.Error
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("talk to GitHub: %w", ctx.Err())
	case errors.As(err, &timeout) && timeout.Timeout():
		return fmt.Errorf("%w: %w", ErrTimedOut, err)
	}
	return fmt.Errorf("%w: %w", ErrUnreachable, err)
}

// statusFailure is the error for an answer that is not a success.
func statusFailure(res *http.Response) error {
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return ErrTokenRejected
	case res.StatusCode == http.StatusTooManyRequests,
		res.StatusCode == http.StatusForbidden && res.Header.Get("X-Ratelimit-Remaining") == "0":
		return ErrRateLimited
	case res.StatusCode == http.StatusForbidden, res.StatusCode == http.StatusNotFound:
		return ErrGistInaccessible
	}
	return &StatusError{Status: res.StatusCode}
}
