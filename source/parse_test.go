package source

import "testing"

func TestParseRepo(t *testing.T) {
	for _, c := range []struct {
		text string
		want RepoRef
		ok   bool
	}{
		{"https://github.com/cli/cli", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"https://github.com/cli/cli/releases/tag/v2.0.0", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"http://www.github.com/cli/cli/?tab=readme#top", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"github.com/cli/cli", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"git@github.com:cli/cli", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"git@github.com:cli/cli.git", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"https://github.com/cli/cli.git", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"cli/cli", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"BurntSushi/ripgrep.rs", RepoRef{Owner: "BurntSushi", Repo: "ripgrep.rs"}, true},
		{`  "cli/cli"  `, RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"<https://github.com/cli/cli>", RepoRef{Owner: "cli", Repo: "cli"}, true},
		{"'owner/repo.git'", RepoRef{Owner: "owner", Repo: "repo"}, true},

		{"", RepoRef{}, false},
		{"cli", RepoRef{}, false},
		{"find me a text editor", RepoRef{}, false},
		{"https://github.com/cli/cli and more", RepoRef{}, false},
		{"https://github.com/orgs/acme/people", RepoRef{}, false},
		{"https://github.com/topics/cli", RepoRef{}, false},
		{"orgs/acme", RepoRef{}, false},
		{"https://github.com/cli", RepoRef{}, false},
		{"https://example.com/cli/cli", RepoRef{}, false},
		{"https://gitlab.com/cli/cli", RepoRef{}, false},
		{"a/b/c", RepoRef{}, false},
		{"cli/cli/", RepoRef{}, false},
		{`C:\tools\cli`, RepoRef{}, false},
		{"./tools", RepoRef{}, false},
		{"../cli", RepoRef{}, false},
	} {
		got, ok := ParseRepo(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseRepo(%q) = %v, %v; want %v, %v", c.text, got, ok, c.want, c.ok)
		}
	}
}
