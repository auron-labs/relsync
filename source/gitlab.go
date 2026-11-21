package source

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GitLab is a Source backed by the REST API of a GitLab site, gitlab.com or
// self-hosted. A repository is a project, and its owner the path of the groups
// it is in.
type GitLab struct{ *siteClient }

var _ Source = (*GitLab)(nil)

// NewGitLab returns a client of the GitLab at options.Host.
func NewGitLab(options SiteOptions) *GitLab {
	return &GitLab{newSiteClient(options)}
}

func (g *GitLab) key() string { return SiteKey(GitLabName, g.host) }

type gitlabProjectDTO struct {
	Path        string     `json:"path"`
	FullPath    string     `json:"path_with_namespace"`
	Description string     `json:"description"`
	Topics      []string   `json:"topics"`
	Stars       int        `json:"star_count"`
	Forks       int        `json:"forks_count"`
	OpenIssues  int        `json:"open_issues_count"`
	URL         string     `json:"web_url"`
	AvatarURL   string     `json:"avatar_url"`
	Archived    bool       `json:"archived"`
	CreatedAt   *time.Time `json:"created_at"`
	ActivityAt  *time.Time `json:"last_activity_at"`
	ForkedFrom  *struct{}  `json:"forked_from_project"`
	License     *struct {
		Name     string `json:"name"`
		Nickname string `json:"nickname"`
	} `json:"license"`
	Namespace struct {
		AvatarURL string `json:"avatar_url"`
	} `json:"namespace"`
}

// Repository returns the project's metadata.
func (g *GitLab) Repository(ctx context.Context, ref RepoRef) (Repository, error) {
	resp, err := g.get(ctx, projectPath(ref)+"?license=true", ref.String(), "")
	if err != nil {
		return Repository{}, err
	}
	var dto gitlabProjectDTO
	if err := json.Unmarshal(resp.body, &dto); err != nil {
		return Repository{}, fmt.Errorf("decode %s: %w", ref, err)
	}
	return g.repository(dto, ref), nil
}

func (g *GitLab) repository(dto gitlabProjectDTO, ref RepoRef) Repository {
	owner, name := ref.Owner, dto.Path
	if i := strings.LastIndex(dto.FullPath, "/"); i > 0 {
		owner = dto.FullPath[:i]
	}
	if name == "" {
		name = ref.Repo
	}
	repo := Repository{
		Source: g.key(), Owner: owner, Name: name, Description: dto.Description, Topics: dto.Topics,
		Stars: dto.Stars, Forks: dto.Forks, OpenIssues: dto.OpenIssues, URL: dto.URL, Archived: dto.Archived,
		Fork: dto.ForkedFrom != nil, AvatarURL: g.absolute(cmp.Or(dto.AvatarURL, dto.Namespace.AvatarURL)),
	}
	if repo.URL == "" {
		repo.URL = RepoURL(repo.Ref())
	}
	if dto.License != nil {
		repo.License = cmp.Or(dto.License.Nickname, dto.License.Name)
	}
	if dto.CreatedAt != nil {
		repo.Created = *dto.CreatedAt
	}
	if dto.ActivityAt != nil {
		repo.Pushed = *dto.ActivityAt
	}
	return repo
}

// absolute makes an address the site gave relative to itself complete.
func (g *GitLab) absolute(link string) string {
	if strings.HasPrefix(link, "/") {
		return g.baseURL + link
	}
	return link
}

// Releases returns the newest releases, newest first. An upcoming release counts
// as a pre-release. The request is conditional when the site sends ETags.
func (g *GitLab) Releases(ctx context.Context, ref RepoRef, etag string) (ReleasePage, error) {
	resp, err := g.get(ctx, projectPath(ref)+"/releases?per_page="+strconv.Itoa(releasesPerPage), ref.String(), etag)
	if err != nil {
		return ReleasePage{}, err
	}
	if resp.status == http.StatusNotModified {
		return ReleasePage{NotModified: true, ETag: etag}, nil
	}
	var dtos []gitlabReleaseDTO
	if err := json.Unmarshal(resp.body, &dtos); err != nil {
		return ReleasePage{}, fmt.Errorf("decode releases of %s: %w", ref, err)
	}
	releases := make([]Release, 0, len(dtos))
	for _, dto := range dtos {
		releases = append(releases, dto.release())
	}
	slices.SortStableFunc(releases, func(a, b Release) int { return b.Published.Compare(a.Published) })
	return ReleasePage{Releases: releases, ETag: resp.header.Get("ETag")}, nil
}

type gitlabReleaseDTO struct {
	Tag         string     `json:"tag_name"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Upcoming    bool       `json:"upcoming_release"`
	CreatedAt   *time.Time `json:"created_at"`
	ReleasedAt  *time.Time `json:"released_at"`
	Links       struct {
		Self string `json:"self"`
	} `json:"_links"`
	Assets struct {
		Links []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			URL    string `json:"url"`
			Direct string `json:"direct_asset_url"`
			Type   string `json:"link_type"`
		} `json:"links"`
	} `json:"assets"`
}

func (d gitlabReleaseDTO) release() Release {
	r := Release{Tag: d.Tag, Name: d.Name, Notes: d.Description, Prerelease: d.Upcoming, URL: d.Links.Self}
	switch {
	case d.ReleasedAt != nil:
		r.Published = *d.ReleasedAt
	case d.CreatedAt != nil:
		r.Published = *d.CreatedAt
	}
	for _, l := range d.Assets.Links {
		// Runbooks and images are documentation and container images, not files to install.
		if l.Type == "runbook" || l.Type == "image" {
			continue
		}
		link := cmp.Or(l.Direct, l.URL)
		if link == "" {
			continue
		}
		r.Assets = append(r.Assets, Asset{ID: l.ID, Name: linkFileName(l.Name, link), URL: link})
	}
	return r
}

// linkFileName is the name of the file a release link points at. A link's
// name is a label of the publisher's choosing, such as "Windows installer",
// where the file's name is what says what the file is.
func linkFileName(label, link string) string {
	if looksLikeFile(label) {
		if base := label[strings.LastIndexAny(label, `/\`)+1:]; looksLikeFile(base) {
			return base
		}
	}
	if u, err := url.Parse(link); err == nil {
		if base := path.Base(u.Path); looksLikeFile(base) {
			return base
		}
	}
	return label
}

func looksLikeFile(name string) bool {
	return name != "" && !strings.ContainsAny(name, " \t") && path.Ext(name) != ""
}

// projectPath is the API path of a project, which a project is addressed in by
// its URL-encoded path.
func projectPath(ref RepoRef) string {
	return "/api/v4/projects/" + url.PathEscape(ref.Owner+"/"+ref.Repo)
}
