package scan

import (
	"regexp"
	"strings"

	"github.com/aaronflorey/release-sync/source"
)

// repoReference finds a GitHub repository in free text: an address, an SSH
// remote or a mention.
var repoReference = regexp.MustCompile(`(?i)\bgithub\.com[/:]([A-Za-z0-9_.-]+)/([A-Za-z0-9_][A-Za-z0-9_.-]*)`)

// otherSiteAddress finds an address on one of the public sites other than GitHub.
var otherSiteAddress = regexp.MustCompile(`(?i)https?://(?:www\.)?(?:gitlab\.com|codeberg\.org)/[^\s"'<>?#]+`)

// notOwners are first path words of GitHub's own pages, which are not accounts.
var notOwners = map[string]bool{
	"sponsors": true, "orgs": true, "settings": true, "topics": true, "marketplace": true, "features": true,
	"about": true, "apps": true, "users": true, "login": true, "collections": true, "enterprise": true,
}

// RepoIn finds the first repository a text names, on GitHub, else on GitLab or
// Codeberg. Pages of GitHub that are not repositories, such as its sponsors
// and settings, are passed over.
func RepoIn(text string) (source.RepoRef, bool) {
	for _, m := range repoReference.FindAllStringSubmatch(text, -1) {
		owner, repo := m[1], strings.TrimRight(m[2], ".")
		if len(repo) > 4 && strings.EqualFold(repo[len(repo)-4:], ".git") {
			repo = repo[:len(repo)-4]
		}
		if notOwners[strings.ToLower(owner)] || repo == "" {
			continue
		}
		return source.RepoRef{Owner: owner, Repo: repo}, true
	}
	for _, address := range otherSiteAddress.FindAllString(text, -1) {
		if ref, ok := source.ParseAddress(address); ok {
			return ref, true
		}
	}
	return source.RepoRef{}, false
}
