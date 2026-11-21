package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/auron-labs/mygo-framework/state"
)

// SiteOptions configure a GitLab or Forgejo client.
type SiteOptions struct {
	// Host is the site's host, as it appears in its addresses.
	Host string
	// BaseURL is the address the site is reached at, https://Host by default;
	// tests point it at a local server.
	BaseURL string
	// UserAgent identifies the product.
	UserAgent string
	// HTTPClient defaults to a client with a 30 second timeout.
	HTTPClient *http.Client
}

// siteClient is what the GitLab and Forgejo clients share: anonymous requests,
// the request allowance the site reports, and the mapping of failed requests to
// errors. It is safe for concurrent use.
type siteClient struct {
	label     string
	host      string
	baseURL   string
	userAgent string
	http      *http.Client
	now       func() time.Time

	allowance *state.Cell[Allowance]
}

func newSiteClient(o SiteOptions) *siteClient {
	c := &siteClient{
		label: hostLabel(strings.ToLower(o.Host)), host: strings.ToLower(o.Host), baseURL: strings.TrimSuffix(o.BaseURL, "/"),
		userAgent: o.UserAgent, http: o.HTTPClient, now: time.Now, allowance: state.NewCell(Allowance{}),
	}
	if c.baseURL == "" {
		c.baseURL = "https://" + o.Host
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: requestTimeout}
	}
	return c
}

// Allowance returns the allowance the latest response reported, if any did.
func (c *siteClient) Allowance() Allowance { return c.allowance.Get() }

// get sends a GET and returns a successful or not-modified response. Every
// other outcome is an *Error, or the context's error when the caller gave up.
func (c *siteClient) get(ctx context.Context, path, resource, etag string) (response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return response{}, fmt.Errorf("build request for %s: %w", resource, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	httpResp, err := c.http.Do(req)
	if err != nil {
		return response{}, transportError(ctx, c.label, resource, false, err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return response{}, transportError(ctx, c.label, resource, false, err)
	}
	resp := response{status: httpResp.StatusCode, header: httpResp.Header, body: body}
	c.recordAllowance(resp.header)
	if resp.status >= 200 && resp.status < 300 || resp.status == http.StatusNotModified {
		return resp, nil
	}
	return response{}, c.statusError(resp, resource)
}

// AssetRequest says how to fetch a release file: from its public link.
func (c *siteClient) AssetRequest(_ RepoRef, a Asset) AssetRequest {
	return AssetRequest{URL: a.URL, Header: http.Header{"User-Agent": {c.userAgent}}}
}

// recordAllowance reads the allowance a response reports, in either of the
// header families sites use.
func (c *siteClient) recordAllowance(h http.Header) {
	limit, limitErr := strconv.Atoi(firstHeader(h, "Ratelimit-Limit", "X-Ratelimit-Limit"))
	remaining, remainingErr := strconv.Atoi(firstHeader(h, "Ratelimit-Remaining", "X-Ratelimit-Remaining"))
	if limitErr != nil || remainingErr != nil {
		return
	}
	a := Allowance{Known: true, Limit: limit, Remaining: remaining}
	a.Reset = c.resetTime(h)
	c.allowance.Set(a)
}

func (c *siteClient) statusError(resp response, resource string) error {
	e := &Error{Status: resp.status, Resource: resource, Site: c.label}
	switch {
	case resp.status == http.StatusNotFound:
		e.Kind = KindNotFound
	case resp.status == http.StatusTooManyRequests || resp.status == http.StatusForbidden && c.limited(resp.header):
		e.Kind = KindRateLimited
		e.ResetAt = c.resetTime(resp.header)
	default:
		e.Kind = KindStatus
	}
	return e
}

// limited reports a refusal for lack of allowance, apart from the status that
// says so outright.
func (c *siteClient) limited(h http.Header) bool {
	return firstHeader(h, "Ratelimit-Remaining", "X-Ratelimit-Remaining") == "0" || h.Get("Retry-After") != ""
}

// resetTime is when the allowance is restored: Retry-After, else the reset
// header, which sites send as a Unix time or as seconds from now.
func (c *siteClient) resetTime(h http.Header) time.Time {
	if wait, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
		return c.now().Add(time.Duration(wait) * time.Second)
	}
	reset, err := strconv.ParseInt(firstHeader(h, "Ratelimit-Reset", "X-Ratelimit-Reset"), 10, 64)
	switch {
	case err != nil:
		return time.Time{}
	case reset > unixTimeFloor:
		return time.Unix(reset, 0)
	}
	return c.now().Add(time.Duration(reset) * time.Second)
}

// unixTimeFloor tells a Unix time from a number of seconds: no wait is that long.
const unixTimeFloor = 1_000_000_000

func firstHeader(h http.Header, names ...string) string {
	for _, name := range names {
		if v := h.Get(name); v != "" {
			return v
		}
	}
	return ""
}

// transportError is a request that never got a response: the caller giving
// up, or the network failing.
func transportError(ctx context.Context, site, resource string, authenticated bool, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("request %s: %w", resource, ctxErr)
	}
	kind := KindUnreachable
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() || errors.Is(err, os.ErrDeadlineExceeded) {
		kind = KindTimedOut
	}
	return &Error{Kind: kind, Site: site, Resource: resource, Authenticated: authenticated, Cause: err}
}
