package source

import (
	"net/url"
	"strings"
)

// The kinds of site. An Item's source is the kind alone for GitHub and the
// kind and host, as "kind:host", for the others.
const (
	GitHubName  = "github"
	GitLabName  = "gitlab"
	ForgejoName = "forgejo"
	// unknownName is a host whose kind is not known yet; a Registry works it out.
	unknownName = "web"

	gitHubHost   = "github.com"
	gitLabHost   = "gitlab.com"
	codebergHost = "codeberg.org"
)

// SiteKey is the key of the site of the given kind at host. GitHub has only
// one host.
func SiteKey(kind, host string) string {
	if kind == GitHubName {
		return GitHubName
	}
	return kind + ":" + strings.ToLower(host)
}

// SplitSite is the kind and host of a site key. A key without a host is one of
// the public sites.
func SplitSite(key string) (kind, host string) {
	kind, host, found := strings.Cut(key, ":")
	switch {
	case found:
		return kind, host
	case kind == GitLabName:
		return kind, gitLabHost
	case kind == ForgejoName:
		return kind, codebergHost
	}
	return GitHubName, gitHubHost
}

// IsGitHub reports the key of GitHub, which has features the other sites lack.
func IsGitHub(key string) bool { return key == "" || key == GitHubName }

// SiteLabel is the name of the site to show: a well-known one by name, any
// other by its host.
func SiteLabel(key string) string {
	kind, host := SplitSite(key)
	if kind == GitHubName {
		return "GitHub"
	}
	return hostLabel(host)
}

func hostLabel(host string) string {
	switch host {
	case gitLabHost:
		return "GitLab"
	case codebergHost:
		return "Codeberg"
	}
	return host
}

// RepoURL is the address of the repository's page.
func RepoURL(ref RepoRef) string {
	return "https://" + ref.Host() + "/" + ref.Owner + "/" + ref.Repo
}

// Unresolved reports a repository on a host whose kind has not been worked out.
func (r RepoRef) Unresolved() bool {
	kind, _ := SplitSite(r.Source)
	return kind == unknownName
}

// Host is the host the repository is on.
func (r RepoRef) Host() string {
	_, host := SplitSite(r.SourceKey())
	return host
}

// hostPath is an address split into its host and the path segments.
type hostPath struct {
	host     string
	segments []string
}

// parseAddress splits a repository address into host and project path:
// https://host/group/sub/project/-/releases, host/owner/repo, with or without
// a scheme and ".git". It cuts the path at the first segment that is not part
// of a project's name. The scheme is required for a host that is not one of
// the public sites, so plain text is never taken for an address.
func parseAddress(text string) (hostPath, bool) {
	text = strings.Trim(strings.TrimSpace(text), `"'<>`)
	if text == "" || strings.ContainsAny(text, " \t\r\n\\") {
		return hostPath{}, false
	}
	hasScheme := strings.HasPrefix(strings.ToLower(text), "http://") || strings.HasPrefix(strings.ToLower(text), "https://")
	raw := text
	if !hasScheme {
		raw = "https://" + text
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return hostPath{}, false
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	if !hasScheme && host != gitHubHost && host != gitLabHost && host != codebergHost {
		return hostPath{}, false
	}
	if !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return hostPath{}, false
	}
	var segments []string
	for _, segment := range strings.Split(u.EscapedPath(), "/") {
		if segment == "" {
			continue
		}
		if stopSegments[strings.ToLower(segment)] {
			break
		}
		unescaped, err := url.PathUnescape(segment)
		if err != nil || isDots(unescaped) {
			return hostPath{}, false
		}
		segments = append(segments, unescaped)
	}
	if len(segments) < 2 {
		return hostPath{}, false
	}
	last := len(segments) - 1
	segments[last] = strings.TrimSuffix(segments[last], ".git")
	if segments[last] == "" {
		return hostPath{}, false
	}
	return hostPath{host: host, segments: segments}, true
}

// stopSegments are the path segments after which an address no longer names
// the project.
var stopSegments = map[string]bool{
	"-": true, "releases": true, "src": true, "tree": true, "blob": true, "issues": true, "pulls": true, "commit": true,
	"wiki": true, "actions": true, "tags": true, "branches": true, "raw": true, "archive": true, "packages": true,
	"projects": true, "compare": true,
}

// ParseAddress reads a repository from an address that names its site: a link
// to a project page on GitHub, GitLab, Codeberg or any other host, or an SSH
// remote of a public site. A host that is not one of the public sites has a
// kind that is not known yet, which Registry.Repository finds out. Unlike
// ParseLink it does not take a bare owner/repo.
func ParseAddress(text string) (RepoRef, bool) {
	if ref, ok := parseSSH(text); ok {
		return ref, true
	}
	addr, ok := parseAddress(text)
	switch {
	case ok && addr.host == gitHubHost:
		return ParseRepo(text)
	case ok:
		return refOnHost(addr), true
	case strings.Contains(text, "git@github.com:"):
		return ParseRepo(text)
	}
	return RepoRef{}, false
}

// ParseLink reads a repository from what people paste: an address
// (ParseAddress) or a bare owner/repo, which is on GitHub.
func ParseLink(text string) (RepoRef, bool) {
	if ref, ok := ParseAddress(text); ok {
		return ref, true
	}
	return ParseRepo(text)
}

// parseSSH reads git@host:path.git remotes of the public sites other than GitHub.
func parseSSH(text string) (RepoRef, bool) {
	text = strings.Trim(strings.TrimSpace(text), `"'<>`)
	rest, found := strings.CutPrefix(text, "git@")
	if !found {
		return RepoRef{}, false
	}
	host, path, found := strings.Cut(rest, ":")
	if !found || host == gitHubHost {
		return RepoRef{}, false
	}
	if host != gitLabHost && host != codebergHost {
		return RepoRef{}, false
	}
	addr, ok := parseAddress("https://" + host + "/" + path)
	if !ok {
		return RepoRef{}, false
	}
	return refOnHost(addr), true
}

// refOnHost is the repository an address names. On a host of a known kind
// the kind is set; any other host is left for the Registry to identify.
func refOnHost(a hostPath) RepoRef {
	last := len(a.segments) - 1
	owner, repo := strings.Join(a.segments[:last], "/"), a.segments[last]
	switch a.host {
	case gitHubHost:
		return NewRef(GitHubName, owner, repo)
	case gitLabHost:
		return NewRef(SiteKey(GitLabName, a.host), owner, repo)
	case codebergHost:
		return NewRef(SiteKey(ForgejoName, a.host), owner, repo)
	}
	return NewRef(SiteKey(unknownName, a.host), owner, repo)
}
