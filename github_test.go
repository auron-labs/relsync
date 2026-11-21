package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/secrets"

	"github.com/aaronflorey/release-sync/source"
)

func TestErrorTextMapsEveryKindToSomethingActionable(t *testing.T) {
	reset := time.Date(2026, 11, 21, 15, 30, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{
			"not found", &source.Error{Kind: source.KindNotFound, Resource: "acme/tool"},
			"acme/tool wasn't found. If it's private, add a token with access in Settings.",
		},
		{
			"token rejected", &source.Error{Kind: source.KindTokenRejected},
			"GitHub rejected your access token. Check it in Settings → GitHub.",
		},
		{
			"rate limited without a token", &source.Error{Kind: source.KindRateLimited, ResetAt: reset},
			"GitHub rate limit reached. It resets at 3:30 PM. Add a token in Settings to raise the limit to 5,000 per hour.",
		},
		{
			"rate limited with a token", &source.Error{Kind: source.KindRateLimited, ResetAt: reset, Authenticated: true},
			"GitHub rate limit reached. It resets at 3:30 PM.",
		},
		{
			"rate limited, reset unknown", &source.Error{Kind: source.KindRateLimited},
			"GitHub rate limit reached. It resets soon. Add a token in Settings to raise the limit to 5,000 per hour.",
		},
		{"unreachable", &source.Error{Kind: source.KindUnreachable}, "Can't reach GitHub. Check your internet connection."},
		{"timed out", &source.Error{Kind: source.KindTimedOut}, "GitHub took too long to respond."},
		{"other status", &source.Error{Kind: source.KindStatus, Status: 502}, "GitHub returned an error (502)."},
		{"not a request error", errors.New("bad json"), "Something went wrong: bad json"},
		{
			"another site's limit", &source.Error{Kind: source.KindRateLimited, Site: "GitLab", ResetAt: reset},
			"GitLab rate limit reached. It resets at 3:30 PM.",
		},
		{
			"another site's limit, reset unknown", &source.Error{Kind: source.KindRateLimited, Site: "Codeberg"},
			"Codeberg rate limit reached. It resets soon.",
		},
		{"another site unreachable", &source.Error{Kind: source.KindUnreachable, Site: "git.example.com"}, "Can't reach git.example.com. Check your internet connection."},
		{"another site's status", &source.Error{Kind: source.KindStatus, Site: "Codeberg", Status: 503}, "Codeberg returned an error (503)."},
		{"no project at an address", &source.Error{Kind: source.KindNoProject, Site: "git.example.com"}, "Release Sync couldn't find a project at that address."},
	} {
		got := errorText(c.err)
		// The reset time is shown in the viewer's zone.
		if strings.Contains(c.want, "3:30 PM") {
			want := strings.Replace(c.want, "3:30 PM", reset.Local().Format("3:04 PM"), 1)
			if got != want {
				t.Errorf("%s: %q, want %q", c.name, got, want)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAllowanceLevels(t *testing.T) {
	for _, c := range []struct {
		allowance source.Allowance
		want      allowanceLevel
	}{
		{source.Allowance{}, allowanceUnknown},
		{source.Allowance{Known: true, Limit: 60, Remaining: 60}, allowanceOK},
		{source.Allowance{Known: true, Limit: 60, Remaining: 6}, allowanceOK},
		{source.Allowance{Known: true, Limit: 60, Remaining: 5}, allowanceLow},
		{source.Allowance{Known: true, Limit: 5000, Remaining: 499}, allowanceLow},
		{source.Allowance{Known: true, Limit: 5000, Remaining: 500}, allowanceOK},
		{source.Allowance{Known: true, Limit: 60, Remaining: 0}, allowanceSpent},
	} {
		if got := allowanceLevelOf(c.allowance); got != c.want {
			t.Errorf("%+v = %v, want %v", c.allowance, got, c.want)
		}
	}
}

func TestTheAccountCardShowsWhoIsSignedInAndTheAllowance(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	if err := e.tokens.Set(t.Context(), githubTokenKey, []byte("good-token")); err != nil {
		t.Fatal(err)
	}
	tt := e.open()
	requireText(t, tt, "Not signed in", "Request allowance not checked yet")

	e.screen.refreshAccount()
	e.settle()
	requireText(t, tt, "Signed in as octocat", "55/60 requests left")
	requireNoText(t, tt, "Not signed in")
	if tokens := e.apiHit.tokens(); len(tokens) == 0 || tokens[len(tokens)-1] != "good-token" {
		t.Errorf("requests carried %q", tokens)
	}

	// The card opens Settings at the GitHub section.
	click(t, tt, "Open GitHub settings")
	requireText(t, tt, "Access token", "Verify and save")
}

func TestWithoutATokenTheCardShowsTheAnonymousAllowance(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	e.screen.refreshAccount()
	e.settle()
	requireText(t, tt, "Not signed in", "55/60 requests left")
}

func TestTheGitHubCLITokenIsUsedOnlyWhenAllowedAndNothingIsSaved(t *testing.T) {
	prefs := defaultPreferences()
	e := newEnv(t, prefs)
	e.apiHit.users["cli-token"] = "clibot"
	e.screen.ghToken = func(context.Context) (string, error) { return "cli-token", nil }

	e.screen.refreshAccount()
	e.settle()
	if got := e.screen.account.Get(); got.Login != "" || e.api.HasToken() {
		t.Fatalf("the CLI token was used while the setting is off: %+v", got)
	}

	e.screen.prefs.Update(func(p *preferences) { p.UseGitHubCLI = true })
	e.screen.refreshAccount()
	e.settle()
	if got := e.screen.account.Get(); got.Login != "clibot" || got.Origin != originCLI {
		t.Fatalf("account = %+v", got)
	}

	// A saved token wins over the CLI's.
	if err := e.tokens.Set(t.Context(), githubTokenKey, []byte("good-token")); err != nil {
		t.Fatal(err)
	}
	e.screen.refreshAccount()
	e.settle()
	if got := e.screen.account.Get(); got.Login != "octocat" || got.Origin != originSaved {
		t.Fatalf("account = %+v", got)
	}
}

func TestVerifyAndSaveStoresTheTokenOnlyInTheCredentialStore(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	e.screen.openSettings(settingsGitHub)
	requireText(t, tt, "Access token", "Use the GitHub CLI's token")
	requireNoText(t, tt, "Remove token")

	typeInto(t, tt, "GitHub access token", "good-token")
	click(t, tt, "Verify and save")
	e.settle()
	requireText(t, tt, "Saved. Signed in as octocat.", "Remove token", "Signed in as octocat")
	if got, err := e.tokens.Get(t.Context(), githubTokenKey); err != nil || string(got) != "good-token" {
		t.Fatalf("stored token = %q, %v", got, err)
	}
	if e.screen.tokenInput != "" {
		t.Error("the token stayed in the field")
	}
	if !e.api.HasToken() {
		t.Error("the client did not adopt the token")
	}
	if !hasMessage(e, "Signed in to GitHub as octocat") {
		t.Errorf("activity = %q", messages(e))
	}
	assertTokenNotOnDisk(t, e, "good-token")

	click(t, tt, "Remove token")
	e.settle()
	requireText(t, tt, "The saved token was removed.", "Not signed in")
	if _, err := e.tokens.Get(t.Context(), githubTokenKey); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("after removal Get = %v", err)
	}
	if e.api.HasToken() {
		t.Error("the client kept the removed token")
	}
}

func TestAnInvalidTokenIsRejectedAndNotSaved(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	e.screen.openSettings(settingsGitHub)
	tt.Frame()
	typeInto(t, tt, "GitHub access token", "bad-token")
	click(t, tt, "Verify and save")
	e.settle()
	requireText(t, tt, "That token isn't valid.")
	if _, err := e.tokens.Get(t.Context(), githubTokenKey); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("a rejected token was stored: %v", err)
	}
	if e.api.HasToken() {
		t.Error("a rejected token is in use")
	}
}

func TestSavingFailsVisiblyWhenThereIsNoCredentialStore(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.screen.tokens = brokenStore{}
	tt := e.open()
	e.screen.openSettings(settingsGitHub)
	tt.Frame()
	typeInto(t, tt, "GitHub access token", "good-token")
	click(t, tt, "Verify and save")
	e.settle()
	requireText(t, tt, "Could not save the token: ")
	if e.api.HasToken() {
		t.Error("an unsaved token is in use")
	}
}

type brokenStore struct{}

var errNoStore = errors.New("no credential store")

func (brokenStore) Get(context.Context, string) ([]byte, error) { return nil, errNoStore }
func (brokenStore) Set(context.Context, string, []byte) error   { return errNoStore }
func (brokenStore) Delete(context.Context, string) error        { return errNoStore }

// assertTokenNotOnDisk fails when the token is in any file of the data directory.
func assertTokenNotOnDisk(t *testing.T, e *env, token string) {
	t.Helper()
	dir := e.screen.dirs.config
	// Flush the write-ahead log so every page reaches the files being searched.
	if _, err := e.db.SQL.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "app.db*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("database files: %v, %v", files, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file) // #nosec G304 -- the files come from a glob of the test's own directory.
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(token)) {
			t.Errorf("the token is in %s", filepath.Base(file))
		}
	}
}

func TestSettingsForChecks(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	// The Updates section is taller than the window, and a control below its edge cannot be clicked.
	tt.SetSize(windowWidth, 2000)
	e.screen.openSettings("updates")
	requireText(t, tt, "Check for updates at startup", "Checks at once", "Include pre-releases for new items")
	click(t, tt, "Toggle checking for updates at startup")
	click(t, tt, "Toggle including pre-releases for new items")
	got := e.screen.prefs.Get()
	if got.CheckOnStartup || !got.DefaultIncludePrereleases {
		t.Errorf("preferences = %+v", got)
	}
}

func TestCLITokenSetting(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	e.screen.openSettings(settingsGitHub)
	tt.Frame()
	click(t, tt, "Toggle using the GitHub CLI's token")
	e.settle()
	if !e.screen.prefs.Get().UseGitHubCLI {
		t.Error("the setting was not saved")
	}
}

func TestParallelChecksIsBoundedToOneThroughEight(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	tt.SetSize(windowWidth, 2000)
	e.screen.openSettings("updates")
	tt.Frame()
	click(t, tt, "Checks at once Increase")
	if got := e.screen.prefs.Get().ParallelChecks; got != 5 {
		t.Errorf("after increasing = %d", got)
	}
	for range 6 {
		click(t, tt, "Checks at once Increase")
	}
	if got := e.screen.prefs.Get().ParallelChecks; got != maxParallelChecks {
		t.Errorf("after many increases = %d, want the maximum %d", got, maxParallelChecks)
	}
	for range 10 {
		click(t, tt, "Checks at once Decrease")
	}
	if got := e.screen.prefs.Get().ParallelChecks; got != minParallelChecks {
		t.Errorf("after many decreases = %d, want the minimum %d", got, minParallelChecks)
	}
}
