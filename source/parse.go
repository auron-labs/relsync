package source

import (
	"regexp"
	"strings"
)

// repoPattern accepts the forms people paste: a GitHub URL with any trailing
// path, a host-less github.com/owner/repo, an SSH remote, or bare owner/repo.
var repoPattern = regexp.MustCompile(
	`(?i)^(?:(?:https?://)?(?:www\.)?github\.com[/:]|git@github\.com:)?` +
		`([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?(?:[/?#].*)?$`)

// ParseRepo reads a repository reference from text such as
// https://github.com/owner/repo/releases, git@github.com:owner/repo.git or
// owner/repo. Surrounding quotes and angle brackets are ignored. Text with
// spaces, the orgs and topics pages, and a bare reference that looks like a
// path or a non-GitHub URL are not repositories.
func ParseRepo(text string) (RepoRef, bool) {
	text = strings.Trim(strings.TrimSpace(text), `"'<>`)
	if text == "" || strings.ContainsAny(text, " \t\r\n") {
		return RepoRef{}, false
	}
	m := repoPattern.FindStringSubmatch(text)
	if m == nil {
		return RepoRef{}, false
	}
	owner, repo := m[1], m[2]
	if strings.EqualFold(owner, "orgs") || strings.EqualFold(owner, "topics") || isDots(owner) || isDots(repo) {
		return RepoRef{}, false
	}
	if !strings.Contains(strings.ToLower(text), "github.com") &&
		(strings.Contains(text, "://") || strings.Contains(text, `\`) || strings.Count(text, "/") != 1) {
		return RepoRef{}, false
	}
	return RepoRef{Owner: owner, Repo: repo}, true
}

// isDots reports a name made only of dots, which is a path segment, not a name.
func isDots(name string) bool { return strings.Trim(name, ".") == "" }
