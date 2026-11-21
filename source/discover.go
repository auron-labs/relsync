package source

import (
	"context"
	"errors"
	"net/url"
	"strconv"
)

const (
	// ownerPageSize is how many of an owner's repositories one request lists.
	ownerPageSize = 30
	// starredPageSize is the most the starred endpoint returns at once.
	starredPageSize = 100
	// maxReadmeBytes bounds the README kept for display.
	maxReadmeBytes = 40000
)

// Discovery is what Discover needs beyond tracking repositories: searching by
// query, the signed-in user's stars, an owner's repositories and READMEs. Only
// GitHub offers it.
type Discovery interface {
	// Search runs a repository search.
	Search(ctx context.Context, q SearchQuery) (SearchPage, error)
	// Starred lists the repositories the signed-in user has starred, newest
	// star first, 100 to a page starting at 1.
	Starred(ctx context.Context, page int) (Page, error)
	// OwnerRepositories lists an owner's public repositories, most recently
	// pushed first, 30 to a page starting at 1. Forks are left out.
	OwnerRepositories(ctx context.Context, owner string, page int) (Page, error)
	// Readme returns the repository's README as Markdown, empty when it has none.
	Readme(ctx context.Context, ref RepoRef) (string, error)
}

// SearchQuery is a repository search.
type SearchQuery struct {
	// Text is the query, qualifiers included.
	Text string
	// ByStars sorts the matches by stars, most first; otherwise by best match.
	ByStars bool
	// PerPage is how many matches to ask for, at most 100.
	PerPage int
	// Page starts at 1.
	Page int
}

// SearchPage is one page of matches.
type SearchPage struct {
	Repositories []Repository
	// Total is how many repositories match, of which this is one page.
	Total int
}

// Page is a page of repositories, with whether another may follow it.
type Page struct {
	Repositories []Repository
	More         bool
}

var _ Discovery = (*GitHub)(nil)

// Search runs a repository search. Search has an allowance of its own.
func (g *GitHub) Search(ctx context.Context, q SearchQuery) (SearchPage, error) {
	values := url.Values{"q": {q.Text}, "per_page": {strconv.Itoa(q.PerPage)}}
	if q.Page > 1 {
		values.Set("page", strconv.Itoa(q.Page))
	}
	if q.ByStars {
		values.Set("sort", "stars")
		values.Set("order", "desc")
	}
	var body struct {
		Total int             `json:"total_count"`
		Items []repositoryDTO `json:"items"`
	}
	if err := g.getJSON(ctx, "/search/repositories?"+values.Encode(), "search", &body); err != nil {
		return SearchPage{}, err
	}
	return SearchPage{Repositories: repositories(body.Items), Total: body.Total}, nil
}

// Starred lists the signed-in user's starred repositories. A page of 100 may
// be followed by another.
func (g *GitHub) Starred(ctx context.Context, page int) (Page, error) {
	path := "/user/starred?per_page=" + strconv.Itoa(starredPageSize) + "&page=" + strconv.Itoa(page)
	var dtos []repositoryDTO
	if err := g.getJSON(ctx, path, "stars", &dtos); err != nil {
		return Page{}, err
	}
	return Page{Repositories: repositories(dtos), More: len(dtos) == starredPageSize}, nil
}

// OwnerRepositories lists an owner's repositories, forks left out. More is
// decided on the page as the site returned it, so a page of forks does not end
// the list.
func (g *GitHub) OwnerRepositories(ctx context.Context, owner string, page int) (Page, error) {
	path := "/users/" + url.PathEscape(owner) + "/repos?sort=pushed&per_page=" + strconv.Itoa(ownerPageSize) + "&page=" + strconv.Itoa(page)
	var dtos []repositoryDTO
	if err := g.getJSON(ctx, path, owner, &dtos); err != nil {
		return Page{}, err
	}
	found := repositories(dtos)
	own := found[:0]
	for _, r := range found {
		if !r.Fork {
			own = append(own, r)
		}
	}
	return Page{Repositories: own, More: len(dtos) == ownerPageSize}, nil
}

// Readme returns the repository's README as Markdown, cut to a length a page
// can show. A repository without one has an empty README.
func (g *GitHub) Readme(ctx context.Context, ref RepoRef) (string, error) {
	resp, err := g.do(ctx, request{path: repoPath(ref) + "/readme", resource: ref.String(), accept: "application/vnd.github.raw+json"})
	if err != nil {
		var se *Error
		if errors.As(err, &se) && se.Kind == KindNotFound {
			return "", nil
		}
		return "", err
	}
	text := string(resp.body)
	if len(text) > maxReadmeBytes {
		text = truncateUTF8(text, maxReadmeBytes)
	}
	return text, nil
}

func repositories(dtos []repositoryDTO) []Repository {
	repos := make([]Repository, 0, len(dtos))
	for _, dto := range dtos {
		repos = append(repos, dto.repository())
	}
	return repos
}

// truncateUTF8 cuts s to at most limit bytes without splitting a character.
func truncateUTF8(s string, limit int) string {
	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
