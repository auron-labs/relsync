package source

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// Forgejo is a Source backed by the REST API of a Forgejo or Gitea site,
// Codeberg or self-hosted.
type Forgejo struct{ *siteClient }

var _ Source = (*Forgejo)(nil)

// NewForgejo returns a client of the Forgejo or Gitea at options.Host.
func NewForgejo(options SiteOptions) *Forgejo {
	return &Forgejo{newSiteClient(options)}
}

func (f *Forgejo) key() string { return SiteKey(ForgejoName, f.host) }

type forgejoRepoDTO struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Language    string     `json:"language"`
	Topics      []string   `json:"topics"`
	Stars       int        `json:"stars_count"`
	Forks       int        `json:"forks_count"`
	OpenIssues  int        `json:"open_issues_count"`
	URL         string     `json:"html_url"`
	Homepage    string     `json:"website"`
	AvatarURL   string     `json:"avatar_url"`
	Archived    bool       `json:"archived"`
	Fork        bool       `json:"fork"`
	CreatedAt   *time.Time `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
	Owner       struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	} `json:"owner"`
}

// Repository returns the repository's metadata.
func (f *Forgejo) Repository(ctx context.Context, ref RepoRef) (Repository, error) {
	resp, err := f.get(ctx, forgejoRepoPath(ref), ref.String(), "")
	if err != nil {
		return Repository{}, err
	}
	var dto forgejoRepoDTO
	if err := json.Unmarshal(resp.body, &dto); err != nil {
		return Repository{}, fmt.Errorf("decode %s: %w", ref, err)
	}
	repo := Repository{
		Source: f.key(), Owner: cmp.Or(dto.Owner.Login, ref.Owner), Name: cmp.Or(dto.Name, ref.Repo),
		Description: dto.Description, Language: dto.Language, Topics: dto.Topics, Stars: dto.Stars, Forks: dto.Forks,
		OpenIssues: dto.OpenIssues, URL: dto.URL, Homepage: dto.Homepage, Archived: dto.Archived, Fork: dto.Fork,
		AvatarURL: cmp.Or(dto.Owner.AvatarURL, dto.AvatarURL),
	}
	if repo.URL == "" {
		repo.URL = RepoURL(repo.Ref())
	}
	if dto.CreatedAt != nil {
		repo.Created = *dto.CreatedAt
	}
	if dto.UpdatedAt != nil {
		repo.Pushed = *dto.UpdatedAt
	}
	return repo, nil
}

// Releases returns the newest releases, newest first. The request is
// conditional when the site sends ETags.
func (f *Forgejo) Releases(ctx context.Context, ref RepoRef, etag string) (ReleasePage, error) {
	resp, err := f.get(ctx, forgejoRepoPath(ref)+"/releases?limit="+strconv.Itoa(releasesPerPage), ref.String(), etag)
	if err != nil {
		return ReleasePage{}, err
	}
	if resp.status == http.StatusNotModified {
		return ReleasePage{NotModified: true, ETag: etag}, nil
	}
	var dtos []forgejoReleaseDTO
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
	slices.SortStableFunc(releases, func(a, b Release) int { return b.Published.Compare(a.Published) })
	return ReleasePage{Releases: releases, ETag: resp.header.Get("ETag")}, nil
}

type forgejoReleaseDTO struct {
	Tag         string     `json:"tag_name"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"`
	CreatedAt   *time.Time `json:"created_at"`
	URL         string     `json:"html_url"`
	Assets      []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		Downloads int    `json:"download_count"`
		URL       string `json:"browser_download_url"`
	} `json:"assets"`
}

func (d forgejoReleaseDTO) release() Release {
	r := Release{Tag: d.Tag, Name: d.Name, Notes: d.Body, Prerelease: d.Prerelease, URL: d.URL}
	switch {
	case d.PublishedAt != nil:
		r.Published = *d.PublishedAt
	case d.CreatedAt != nil:
		r.Published = *d.CreatedAt
	}
	for _, a := range d.Assets {
		r.Assets = append(r.Assets, Asset{ID: a.ID, Name: a.Name, Size: a.Size, URL: a.URL, Downloads: a.Downloads})
	}
	return r
}

func forgejoRepoPath(ref RepoRef) string {
	return "/api/v1/repos/" + url.PathEscape(ref.Owner) + "/" + url.PathEscape(ref.Repo)
}
