package scan

import (
	"testing"

	"github.com/aaronflorey/release-sync/source"
)

func TestRepoInFindsTheRepositoryInEachFormATextMayName(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       source.RepoRef
		ok         bool
	}{
		{"an address", "https://github.com/ip7z/7zip", source.RepoRef{Owner: "ip7z", Repo: "7zip"}, true},
		{"an address with a page of the repository", "https://github.com/acme/tool/releases/latest", source.RepoRef{Owner: "acme", Repo: "tool"}, true},
		{"www and a query", "http://www.github.com/acme/tool?tab=readme", source.RepoRef{Owner: "acme", Repo: "tool"}, true},
		{"an SSH remote", "git@github.com:acme/tool.git", source.RepoRef{Owner: "acme", Repo: "tool"}, true},
		{"a clone address", "https://github.com/acme/tool.git", source.RepoRef{Owner: "acme", Repo: "tool"}, true},
		{"a mention in prose", "Source code is at github.com/acme/tool. Enjoy!", source.RepoRef{Owner: "acme", Repo: "tool"}, true},
		{"a copyright line", "Copyright (c) Acme - https://GitHub.com/Acme/Tool", source.RepoRef{Owner: "Acme", Repo: "Tool"}, true},
		{"a repository name with dots", "https://github.com/acme/tool.js", source.RepoRef{Owner: "acme", Repo: "tool.js"}, true},
		{"a page that is not an owner is passed over for the next", "https://github.com/sponsors/someone and https://github.com/acme/tool", source.RepoRef{Owner: "acme", Repo: "tool"}, true},
		{"an owner with no repository", "https://github.com/acme", source.RepoRef{}, false},
		{"a GitLab project", "https://gitlab.com/acme/group/tool/-/releases", source.RepoRef{Owner: "acme/group", Repo: "tool", Source: "gitlab:gitlab.com"}, true},
		{"a Codeberg repository", "Home page: https://codeberg.org/acme/tool.git", source.RepoRef{Owner: "acme", Repo: "tool", Source: "forgejo:codeberg.org"}, true},
		{"GitHub wins over another site", "https://gitlab.com/acme/mirror https://github.com/acme/tool", source.RepoRef{Owner: "acme", Repo: "tool"}, true},
		{"a site that is not public", "https://git.example.com/acme/tool", source.RepoRef{}, false},
		{"a site that only ends in the name", "https://notgithub.com/acme/tool", source.RepoRef{}, false},
		{"a project page host", "https://acme.github.io/tool", source.RepoRef{}, false},
		{"nothing", "", source.RepoRef{}, false},
	} {
		got, ok := RepoIn(tc.text)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: RepoIn(%q) = %+v, %v; want %+v, %v", tc.name, tc.text, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRepoInRejectsEveryPageOfGitHubThatIsNotAnAccount(t *testing.T) {
	for _, word := range []string{"sponsors", "orgs", "settings", "topics", "marketplace", "features", "about", "apps", "users", "login", "collections", "enterprise", "Sponsors"} {
		if got, ok := RepoIn("https://github.com/" + word + "/something"); ok {
			t.Errorf("%s/something was taken for the repository %+v", word, got)
		}
	}
}
