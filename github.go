package main

import (
	"cmp"
	"context"
	"errors"
	"log"
	"strings"

	"github.com/auron-labs/mygo-framework/jobs"
	"github.com/auron-labs/mygo-framework/secrets"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

const (
	githubTokenKey  = "github-token"
	accountJob      = "github-account"
	tokenJob        = "github-token"
	lowAllowance    = 0.10 // The allowance card turns amber below this share.
	settingsGitHub  = "github"
	tokenFieldWidth = 360
)

// tokenOrigin says where the token in use came from.
type tokenOrigin int

const (
	originNone tokenOrigin = iota
	originSaved
	originCLI
)

// account is who the app is signed in to GitHub as.
type account struct {
	Login  string
	Origin tokenOrigin
}

// allowanceLevel is how much of the request allowance is left.
type allowanceLevel int

const (
	allowanceUnknown allowanceLevel = iota
	allowanceOK
	// allowanceLow is under a tenth of the limit.
	allowanceLow
	allowanceSpent
)

func allowanceLevelOf(a source.Allowance) allowanceLevel {
	switch {
	case !a.Known:
		return allowanceUnknown
	case a.Remaining <= 0:
		return allowanceSpent
	case a.Limit > 0 && float64(a.Remaining)/float64(a.Limit) < lowAllowance:
		return allowanceLow
	}
	return allowanceOK
}

// errorText explains a failed request to the user.
func errorText(err error) string {
	var se *source.Error
	if !errors.As(err, &se) {
		return locales.ErrOther(err.Error())
	}
	token := "no"
	if se.Authenticated {
		token = "yes"
	}
	site := cmp.Or(se.Site, "GitHub")
	switch se.Kind {
	case source.KindNotFound:
		return locales.ErrNotFound(se.Resource)
	case source.KindTokenRejected:
		return locales.ErrTokenRejected()
	case source.KindRateLimited:
		return rateLimitText(se, site, token)
	case source.KindUnreachable:
		return locales.ErrUnreachable(site)
	case source.KindTimedOut:
		return locales.ErrTimedOut(site)
	case source.KindStatus:
		return locales.ErrStatus(site, se.Status)
	case source.KindNoProject:
		return locales.ErrNoProject(appName)
	}
	return locales.ErrOther(se.Error())
}

// rateLimitText explains a spent allowance. Only GitHub's can be raised by a
// token from here, with the steps that go with it.
func rateLimitText(se *source.Error, site, token string) string {
	github := se.Site == ""
	switch {
	case github && se.ResetAt.IsZero():
		return locales.ErrRateLimitedSoon(token)
	case github:
		return locales.ErrRateLimited(se.ResetAt, token)
	case se.ResetAt.IsZero():
		return locales.ErrSiteRateLimitedSoon(site)
	}
	return locales.ErrSiteRateLimited(site, se.ResetAt)
}

// resolveToken picks the token to use: the one saved in the credential store,
// then the GitHub CLI's when the setting allows it.
func (s *screen) resolveToken(ctx context.Context) (string, tokenOrigin) {
	saved, err := s.tokens.Get(ctx, githubTokenKey)
	switch {
	case err == nil && len(saved) > 0:
		return string(saved), originSaved
	case err != nil && !errors.Is(err, secrets.ErrNotFound):
		// No credential store, or it is locked: carry on without a saved token.
		log.Printf("read the saved GitHub token: %v", err)
	}
	if s.prefs.Get().UseGitHubCLI {
		token, err := s.ghToken(ctx)
		if err != nil {
			log.Print(err)
		}
		if token != "" {
			return token, originCLI
		}
	}
	return "", originNone
}

// applyAccount adopts the token resolveToken finds and reads who it belongs to
// and the request allowance. It runs in a job.
func (s *screen) applyAccount(ctx context.Context) error {
	token, origin := s.resolveToken(ctx)
	s.github.SetToken(token)
	s.account.Set(account{Origin: origin})
	if err := s.github.RefreshAllowance(ctx); err != nil && ctx.Err() == nil {
		// Offline at launch is routine; the next response updates the card.
		log.Printf("read the GitHub request allowance: %v", err)
	}
	if token == "" {
		return nil
	}
	login, err := s.github.User(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("read the GitHub account: %v", err)
		}
		return nil
	}
	s.account.Set(account{Login: login, Origin: origin})
	return nil
}

// refreshAccount reads the account and allowance in the background.
func (s *screen) refreshAccount() {
	s.startJob(accountJob, s.applyAccount)
}

// startJob runs fn as the named job, ignoring a request while it is already
// running or after shutdown.
func (s *screen) startJob(name string, fn func(ctx context.Context) error) {
	err := s.jobs.Start(name, func(ctx context.Context, _ jobs.Report) error { return fn(ctx) })
	if err != nil && !errors.Is(err, jobs.ErrRunning) && !errors.Is(err, jobs.ErrStopped) {
		log.Print(err)
	}
}

// verifyAndSaveToken checks the token in the Settings field against GitHub,
// keeps it in the credential store and starts using it.
func (s *screen) verifyAndSaveToken() {
	token := strings.TrimSpace(s.tokenInput)
	if token == "" {
		return
	}
	s.startJob(tokenJob, func(ctx context.Context) error {
		login, err := s.github.ValidateToken(ctx, token)
		if err != nil {
			s.setTokenNote(tokenFailureText(err))
			return nil
		}
		if err := s.tokens.Set(ctx, githubTokenKey, []byte(token)); err != nil {
			log.Printf("save the GitHub token: %v", err)
			s.setTokenNote(locales.TokenSaveFailed(err.Error()))
			return nil
		}
		s.onUI(func() { s.tokenInput = "" })
		s.setTokenNote(locales.TokenSaved(login))
		s.record(ctx, activity.Success, 0, locales.ActivityTokenSaved(login))
		return s.applyAccount(ctx)
	})
}

// removeToken deletes the saved token and falls back to the GitHub CLI's, if any.
func (s *screen) removeToken() {
	s.startJob(tokenJob, func(ctx context.Context) error {
		if err := s.tokens.Delete(ctx, githubTokenKey); err != nil {
			log.Printf("remove the GitHub token: %v", err)
			s.setTokenNote(locales.TokenSaveFailed(err.Error()))
			return nil
		}
		s.setTokenNote(locales.TokenRemoved())
		s.record(ctx, activity.Info, 0, locales.ActivityTokenRemoved())
		return s.applyAccount(ctx)
	})
}

func tokenFailureText(err error) string {
	var se *source.Error
	if errors.As(err, &se) && se.Kind == source.KindTokenRejected {
		return locales.TokenInvalid()
	}
	return errorText(err)
}

func (s *screen) setTokenNote(note string) {
	s.onUI(func() { s.tokenNote = note })
}

// allowanceCard is the sidebar's account and request-allowance card. It opens
// Settings → GitHub.
func (s *screen) allowanceCard(c *ui.Context) {
	t := c.Theme()
	acct, allowance := s.account.Get(), s.github.Allowance()
	login := locales.GitHubSignedOut()
	if acct.Login != "" {
		login = locales.GitHubSignedIn(acct.Login)
	}
	allowanceText, allowanceColor := locales.GitHubAllowanceUnknown(), t.TextMuted
	if allowance.Known {
		allowanceText = locales.GitHubAllowance(allowance.Remaining, allowance.Limit)
	}
	switch allowanceLevelOf(allowance) {
	case allowanceUnknown:
	case allowanceOK:
		allowanceColor = t.Success
	case allowanceLow:
		allowanceColor = t.Warning
	case allowanceSpent:
		allowanceColor = t.Danger
	}
	dot := allowanceColor
	card := ui.ButtonBase(c).Label(locales.GitHubCardOpensSettings()).FillWidth().Shrink(0).
		Padding(t.Space(2.5), t.Space(3)).Radius(t.Radius).Border(1, t.Border).Justify(ui.Start).
		TextColor(t.Text).Background(ui.Color{})
	if card.Hovered() {
		card.Background(t.SurfaceHover)
	}
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(t.Space(1)).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
				ui.Box(c).Size(t.Space(2), t.Space(2)).Radius(t.Space(1)).Background(dot)
				components.Text(c, locales.GitHubCardTitle()).FontSize(t.Rem(.85)).Bold()
			})
			components.Text(c, login).FontSize(t.Rem(.8)).TextColor(t.TextMuted).SingleLine()
			components.Text(c, allowanceText).FontSize(t.Rem(.8)).TextColor(allowanceColor).Wrap()
		})
	})
	if card.Clicked() {
		s.openSettings(settingsGitHub)
	}
}

// openSettings shows the Settings page at a section.
func (s *screen) openSettings(section string) {
	s.settings.Category = section
	s.navigate("settings")
}

// githubSettingsGroups is the Settings → GitHub section: the access token and
// whether to borrow the GitHub CLI's.
func (s *screen) githubSettingsGroups() []components.SettingsGroup {
	current := s.prefs.Get().UseGitHubCLI
	useCLI := current
	return []components.SettingsGroup{{ID: "github", Items: []components.SettingsItem{
		{
			ID: "github-token", Title: locales.TokenTitle(), Description: locales.TokenHint(), Stacked: true,
			Keywords: []string{"token", "github", "sign in"},
			Build:    s.tokenControl,
		},
		{
			ID: "github-cli", Title: locales.CliTokenTitle(), Description: locales.CliTokenHint(),
			Keywords: []string{"gh", "cli"},
			Build: func(c *ui.Context) {
				// The switch reports a change by flipping the bound copy.
				components.Switch(c, &useCLI, locales.CliTokenLabel(), components.SwitchOptions{HideLabel: true})
				if useCLI != current {
					s.prefs.Update(func(p *preferences) { p.UseGitHubCLI = useCLI })
					s.refreshAccount()
				}
			},
		},
	}}, s.discoverSettingsGroup()}
}

// discoverSettingsGroup is the part of Settings → GitHub about how Discover
// spends the request allowance.
func (s *screen) discoverSettingsGroup() components.SettingsGroup {
	p := s.prefs.Get()
	light, downloads := p.DiscoverLight, p.DiscoverDownloadsOnly
	return components.SettingsGroup{ID: "discover", Title: locales.DiscoverSettingsTitle(), Items: []components.SettingsItem{
		{
			ID: "discover-light", Title: locales.DiscoverLightTitle(), Description: locales.DiscoverLightHint(lightPageSize),
			Keywords: []string{"discover", "requests", "allowance"},
			Build: func(c *ui.Context) {
				components.Switch(c, &light, locales.DiscoverLightLabel(), components.SwitchOptions{HideLabel: true})
				if light != p.DiscoverLight {
					s.prefs.Update(func(p *preferences) { p.DiscoverLight = light })
				}
			},
		},
		{
			ID: "discover-downloads", Title: locales.DiscoverDownloadsTitle(), Description: locales.DiscoverDownloadsHint(),
			Keywords: []string{"discover", "downloads", "filter"},
			Build: func(c *ui.Context) {
				components.Switch(c, &downloads, locales.DiscoverDownloadsLabel(), components.SwitchOptions{HideLabel: true})
				if downloads != p.DiscoverDownloadsOnly {
					s.prefs.Update(func(p *preferences) { p.DiscoverDownloadsOnly = downloads })
				}
			},
		},
		{
			ID: "discover-intro", Title: locales.DiscoverIntroAgainTitle(), Description: locales.DiscoverIntroAgainHint(),
			Keywords: []string{"discover", "explainer"},
			Build: func(c *ui.Context) {
				if components.Button(c, locales.DiscoverIntroAgain(), components.ButtonSecondary).Clicked() {
					s.prefs.Update(func(p *preferences) { p.DiscoverIntroSeen = false })
					s.navigate("discover")
				}
			},
		},
	}}
}

// tokenControl is the token field with its buttons and the outcome of the last attempt.
func (s *screen) tokenControl(c *ui.Context) {
	t := c.Theme()
	status, started := s.jobs.Status(tokenJob)
	verifying := started && status.State == jobs.Running
	saved := s.account.Get().Origin == originSaved
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.TextBox(c, &s.tokenInput, components.TextBoxOptions{LeadingIcon: lucide.KeyRound()}).
			Label(locales.TokenLabel()).Placeholder(locales.TokenPlaceholder()).Password().
			FillWidth().MaxWidth(tokenFieldWidth)
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			label := locales.TokenVerify()
			if verifying {
				label = locales.TokenVerifying()
			}
			verify := components.Button(c, label, components.ButtonPrimary).Disabled(verifying || strings.TrimSpace(s.tokenInput) == "")
			if verify.Clicked() {
				s.verifyAndSaveToken()
			}
			if saved {
				if components.Button(c, locales.TokenRemove(), components.ButtonOutline).Disabled(verifying).Clicked() {
					s.removeToken()
				}
			}
		})
		if saved && s.tokenNote == "" {
			components.Text(c, locales.TokenSavedBadge()).FontSize(t.Rem(.85)).TextColor(t.TextMuted)
		}
		if s.tokenNote != "" {
			components.Text(c, s.tokenNote).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
		}
	})
}
