package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/auron-labs/mygo-framework/state"
)

const (
	defaultBaseURL = "https://api.github.com"
	apiVersion     = "2022-11-28"
	// releasesPerPage is how many releases one check fetches and the catalog caches.
	releasesPerPage = 30
	requestTimeout  = 30 * time.Second
	// maxResponseBytes bounds what one response may make the client read.
	maxResponseBytes = 16 << 20
	// searchResults is how many repositories one search asks for.
	searchResults = 30
	// avatarSize is the avatar edge length in pixels; cards show them small.
	avatarSize = "96"
)

// GitHubOptions configure a GitHub client.
type GitHubOptions struct {
	// BaseURL defaults to the public API; tests point it at a local server.
	BaseURL string
	// UserAgent identifies the product; GitHub rejects requests without one.
	UserAgent string
	// HTTPClient defaults to a client with a 30 second timeout.
	HTTPClient *http.Client
}

// GitHub is a Source backed by the GitHub REST API. It is safe for concurrent use.
type GitHub struct {
	baseURL   string
	userAgent string
	http      *http.Client
	now       func() time.Time

	mu    sync.RWMutex
	token string

	allowance *state.Cell[Allowance]
}

var (
	_ Source   = (*GitHub)(nil)
	_ Searcher = (*GitHub)(nil)
)

// NewGitHub returns a client that sends no token until SetToken is called.
func NewGitHub(options GitHubOptions) *GitHub {
	g := &GitHub{
		baseURL:   strings.TrimSuffix(options.BaseURL, "/"),
		userAgent: options.UserAgent,
		http:      options.HTTPClient,
		now:       time.Now,
		allowance: state.NewCell(Allowance{}),
	}
	if g.baseURL == "" {
		g.baseURL = defaultBaseURL
	}
	if g.http == nil {
		g.http = &http.Client{Timeout: requestTimeout}
	}
	return g
}

// SetToken sets the token sent with requests, or none when empty. The request
// allowance belongs to the credentials, so it is unknown again until the next
// response reports it.
func (g *GitHub) SetToken(token string) {
	g.mu.Lock()
	g.token = token
	g.mu.Unlock()
	g.allowance.Set(Allowance{})
}

// HasToken reports whether requests carry a token.
func (g *GitHub) HasToken() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.token != ""
}

// Allowance returns the allowance reported by the latest response.
func (g *GitHub) Allowance() Allowance { return g.allowance.Get() }

// AllowanceState lets a view follow the allowance as responses arrive.
func (g *GitHub) AllowanceState() *state.Cell[Allowance] { return g.allowance }

// repositoryDTO is a repository as the API describes it, in a lookup or a search.
type repositoryDTO struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Language    string     `json:"language"`
	Topics      []string   `json:"topics"`
	Stars       int        `json:"stargazers_count"`
	URL         string     `json:"html_url"`
	Forks       int        `json:"forks_count"`
	OpenIssues  int        `json:"open_issues_count"`
	Homepage    string     `json:"homepage"`
	Archived    bool       `json:"archived"`
	Fork        bool       `json:"fork"`
	CreatedAt   *time.Time `json:"created_at"`
	PushedAt    *time.Time `json:"pushed_at"`
	License     *struct {
		SPDX string `json:"spdx_id"`
		Name string `json:"name"`
	} `json:"license"`
	Owner struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	} `json:"owner"`
}

func (d repositoryDTO) repository() Repository {
	repo := Repository{
		Owner: d.Owner.Login, Name: d.Name, Description: d.Description,
		Language: d.Language, Topics: d.Topics, Stars: d.Stars,
		AvatarURL: smallAvatar(d.Owner.AvatarURL), URL: d.URL,
		Forks: d.Forks, OpenIssues: d.OpenIssues, Homepage: d.Homepage, Archived: d.Archived, Fork: d.Fork,
	}
	if d.License != nil {
		// "NOASSERTION" means GitHub could not tell which licence; its name is the better label.
		repo.License = d.License.SPDX
		if repo.License == "" || repo.License == "NOASSERTION" {
			repo.License = d.License.Name
		}
	}
	if d.CreatedAt != nil {
		repo.Created = *d.CreatedAt
	}
	if d.PushedAt != nil {
		repo.Pushed = *d.PushedAt
	}
	return repo
}

// Repository returns the repository's metadata.
func (g *GitHub) Repository(ctx context.Context, ref RepoRef) (Repository, error) {
	var dto repositoryDTO
	if err := g.getJSON(ctx, repoPath(ref), ref.String(), &dto); err != nil {
		return Repository{}, err
	}
	repo := dto.repository()
	// A rename or a case difference in the link: trust the site's spelling,
	// but never return an empty identity.
	if repo.Owner == "" {
		repo.Owner = ref.Owner
	}
	if repo.Name == "" {
		repo.Name = ref.Repo
	}
	return repo, nil
}

// SearchRepositories finds repositories whose name contains name, forks
// excluded, best match first. Search has an allowance of its own, apart from
// the one shown to the user.
func (g *GitHub) SearchRepositories(ctx context.Context, name string) ([]Repository, error) {
	query := url.Values{"q": {name + " in:name fork:false"}, "per_page": {strconv.Itoa(searchResults)}}
	var page struct {
		Items []repositoryDTO `json:"items"`
	}
	if err := g.getJSON(ctx, "/search/repositories?"+query.Encode(), "search", &page); err != nil {
		return nil, err
	}
	found := make([]Repository, 0, len(page.Items))
	for _, dto := range page.Items {
		found = append(found, dto.repository())
	}
	return found, nil
}

// Releases returns the newest 30 releases, newest published first.
func (g *GitHub) Releases(ctx context.Context, ref RepoRef, etag string) (ReleasePage, error) {
	resp, err := g.do(ctx, request{
		path: repoPath(ref) + "/releases?per_page=" + strconv.Itoa(releasesPerPage), resource: ref.String(), etag: etag,
	})
	if err != nil {
		return ReleasePage{}, err
	}
	if resp.status == http.StatusNotModified {
		return ReleasePage{NotModified: true, ETag: etag}, nil
	}
	var dtos []releaseDTO
	if err := json.Unmarshal(resp.body, &dtos); err != nil {
		return ReleasePage{}, fmt.Errorf("decode releases of %s: %w", ref, err)
	}
	releases := make([]Release, 0, len(dtos))
	for _, dto := range dtos {
		if dto.Draft {
			continue
		}
		releases = append(releases, dto.release())
	}
	// Stable, so equal timestamps keep the API's order.
	slices.SortStableFunc(releases, func(a, b Release) int { return b.Published.Compare(a.Published) })
	return ReleasePage{Releases: releases, ETag: resp.header.Get("ETag")}, nil
}

// User returns the login the current token belongs to.
func (g *GitHub) User(ctx context.Context) (string, error) {
	return g.login(ctx, request{path: "/user", resource: "user"})
}

// ValidateToken returns the login a token belongs to without adopting it.
func (g *GitHub) ValidateToken(ctx context.Context, token string) (string, error) {
	return g.login(ctx, request{path: "/user", resource: "user", token: &token})
}

// RefreshAllowance asks GitHub for the current request allowance. The endpoint
// itself does not count against it.
func (g *GitHub) RefreshAllowance(ctx context.Context) error {
	_, err := g.do(ctx, request{path: "/rate_limit", resource: "rate_limit"})
	return err
}

func (g *GitHub) login(ctx context.Context, req request) (string, error) {
	var dto struct {
		Login string `json:"login"`
	}
	resp, err := g.do(ctx, req)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(resp.body, &dto); err != nil {
		return "", fmt.Errorf("decode user: %w", err)
	}
	return dto.Login, nil
}

func (g *GitHub) getJSON(ctx context.Context, path, resource string, out any) error {
	resp, err := g.do(ctx, request{path: path, resource: resource})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return fmt.Errorf("decode %s: %w", resource, err)
	}
	return nil
}

type request struct {
	path     string
	resource string
	etag     string
	// accept replaces the default media type.
	accept string
	// token overrides the client's token, for checking one before adopting it.
	// Such a request says nothing about the client's own allowance.
	token *string
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// do sends a GET and returns a successful or not-modified response. Every other
// outcome is an *Error, or the context's error when the caller gave up.
func (g *GitHub) do(ctx context.Context, req request) (response, error) {
	token := g.currentToken()
	own := req.token == nil
	if !own {
		token = *req.token
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+req.path, nil)
	if err != nil {
		return response{}, fmt.Errorf("build request for %s: %w", req.resource, err)
	}
	httpReq.Header.Set("User-Agent", g.userAgent)
	accept := req.accept
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	httpReq.Header.Set("Accept", accept)
	httpReq.Header.Set("X-Github-Api-Version", apiVersion)
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	if req.etag != "" {
		httpReq.Header.Set("If-None-Match", req.etag)
	}

	httpResp, err := g.http.Do(httpReq)
	if err != nil {
		return response{}, transportError(ctx, "", req.resource, token != "", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return response{}, transportError(ctx, "", req.resource, token != "", err)
	}
	resp := response{status: httpResp.StatusCode, header: httpResp.Header, body: body}
	if own {
		g.recordAllowance(resp.header, token != "")
	}
	if resp.status >= 200 && resp.status < 300 || resp.status == http.StatusNotModified {
		return resp, nil
	}
	return response{}, g.statusError(resp, req.resource, token != "")
}

func (g *GitHub) currentToken() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.token
}

// recordAllowance reads the core allowance from a response. Search has its own,
// smaller allowance that must not replace the one shown to the user.
func (g *GitHub) recordAllowance(h http.Header, authenticated bool) {
	if resource := h.Get("X-Ratelimit-Resource"); resource != "" && resource != "core" {
		return
	}
	limit, limitErr := strconv.Atoi(h.Get("X-Ratelimit-Limit"))
	remaining, remainingErr := strconv.Atoi(h.Get("X-Ratelimit-Remaining"))
	if limitErr != nil || remainingErr != nil {
		return
	}
	a := Allowance{Known: true, Limit: limit, Remaining: remaining, Authenticated: authenticated}
	if reset, err := strconv.ParseInt(h.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
		a.Reset = time.Unix(reset, 0)
	}
	g.allowance.Set(a)
}

func (g *GitHub) statusError(resp response, resource string, authenticated bool) error {
	e := &Error{Status: resp.status, Resource: resource, Authenticated: authenticated}
	switch {
	case resp.status == http.StatusNotFound:
		e.Kind = KindNotFound
	case resp.status == http.StatusUnauthorized:
		e.Kind = KindTokenRejected
	case (resp.status == http.StatusForbidden || resp.status == http.StatusTooManyRequests) && g.limited(resp.header):
		e.Kind = KindRateLimited
		e.ResetAt = g.resetTime(resp.header)
	default:
		e.Kind = KindStatus
	}
	return e
}

// limited reports a response that refused the request for lack of allowance: the
// primary limit is spent, or a secondary limit asks the client to wait.
func (g *GitHub) limited(h http.Header) bool {
	return h.Get("X-Ratelimit-Remaining") == "0" || h.Get("Retry-After") != ""
}

func (g *GitHub) resetTime(h http.Header) time.Time {
	if reset, err := strconv.ParseInt(h.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
		return time.Unix(reset, 0)
	}
	if wait, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		return g.now().Add(time.Duration(wait) * time.Second)
	}
	return time.Time{}
}

type releaseDTO struct {
	Tag         string     `json:"tag_name"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	Prerelease  bool       `json:"prerelease"`
	Draft       bool       `json:"draft"`
	PublishedAt *time.Time `json:"published_at"`
	URL         string     `json:"html_url"`
	Assets      []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		URL       string `json:"browser_download_url"`
		Downloads int    `json:"download_count"`
		State     string `json:"state"`
		Digest    string `json:"digest"`
	} `json:"assets"`
}

func (d releaseDTO) release() Release {
	r := Release{Tag: d.Tag, Name: d.Name, Notes: d.Body, Prerelease: d.Prerelease, URL: d.URL}
	if d.PublishedAt != nil {
		r.Published = *d.PublishedAt
	}
	for _, a := range d.Assets {
		// An asset still being uploaded cannot be downloaded yet.
		if a.State != "" && a.State != "uploaded" {
			continue
		}
		r.Assets = append(r.Assets, Asset{ID: a.ID, Name: a.Name, Size: a.Size, URL: a.URL, Digest: a.Digest, Downloads: a.Downloads})
	}
	return r
}

func repoPath(ref RepoRef) string {
	return "/repos/" + url.PathEscape(ref.Owner) + "/" + url.PathEscape(ref.Repo)
}

// smallAvatar asks for a thumbnail: the default is hundreds of pixels square
// and a Library can show hundreds of avatars.
func smallAvatar(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return raw
	}
	q := u.Query()
	q.Set("s", avatarSize)
	u.RawQuery = q.Encode()
	return u.String()
}
