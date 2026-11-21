package main

import (
	"context"
	"log"

	"github.com/aaronflorey/release-sync/hints"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/oneo-me/onevra/source/components"
)

const (
	repoDialogWidth  = 640
	readmeViewHeight = 280
)

// repoDetails is the repository dialog: what is known about a repository, and
// its README once that arrives. It is only touched on the UI thread.
type repoDetails struct {
	repo source.Repository
	// loading says the README is still on its way.
	loading bool
	readme  string
	// failure is why the README could not be loaded, for the user.
	failure string
	// hints are the signals shown under "At a glance".
	hints []hints.Hint
}

// full reports metadata that came from a lookup or a search, as against the
// snapshot a saved repository keeps.
func (d *repoDetails) full() bool { return !d.repo.Created.IsZero() }

// showRepository opens a repository's details: its numbers, topics and README.
// A repository rebuilt from a saved snapshot is looked up again first, so the
// dialog is not left with figures from the day it was saved.
func (s *screen) showRepository(repo source.Repository) {
	st := &repoDetails{repo: repo, loading: true}
	dialog := s.showDialog(repo.Ref().String(), func(c *ui.Context) { s.repoDialog(c, st) })
	s.background(func(ctx context.Context) error {
		current := repo
		if repo.Created.IsZero() {
			if fresh, err := s.discover.Repository(ctx, repo.Ref()); err == nil {
				current = fresh
			} else if ctx.Err() == nil {
				log.Printf("discover: %v", err)
			}
		}
		glance := s.safetyHints(ctx, current, nil)
		readme, err := s.discover.Readme(ctx, current.Ref())
		if ctx.Err() != nil {
			return nil //nolint:nilerr // The dialog was closed or the application is quitting; there is nothing to report.
		}
		s.onUI(func() {
			if s.dialog != dialog {
				return
			}
			st.repo, st.readme, st.loading, st.hints = current, readme, false, glance
			if err != nil {
				log.Printf("discover: %v", err)
				st.failure = locales.RepoReadmeFailed(errorText(err))
			}
		})
		return unexpectedError(err)
	})
}

func (s *screen) repoDialog(c *ui.Context, st *repoDetails) {
	t := c.Theme()
	repo := st.repo
	ref := repo.Ref()
	held := s.held()
	key := repoKey(ref)
	ui.Column(c).Gap(t.Space(3)).MinWidth(repoDialogWidth - 120).MaxWidth(repoDialogWidth).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
			components.Avatar(c, repo.Name, components.AvatarOptions{Image: s.avatars.Get(repo.AvatarURL), Size: 56})
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				components.Text(c, repo.Name).Bold().FontSize(t.Rem(1.2)).Wrap()
				components.Text(c, ref.String()).TextColor(t.TextMuted).Wrap()
			})
		})
		if repo.Description != "" {
			components.Text(c, repo.Description).Wrap()
		}
		s.repoFacts(c, st)
		s.atAGlance(c, st.hints)
		if len(repo.Topics) > 0 {
			ui.Row(c).FillWidth().Gap(t.Space(1.5)).Wrap().Children(func() {
				for _, topic := range repo.Topics {
					components.Badge(c, topic, components.BadgeOptions{})
				}
			})
		}
		s.repoLinks(c, repo)
		s.repoReadme(c, st)
		ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
			s.repoDialogActions(c, repo, held.library[key], held.saved[key])
		})
	})
}

// repoFacts lists the numbers the repository's site gives, leaving out those a
// saved snapshot does not hold.
func (s *screen) repoFacts(c *ui.Context, st *repoDetails) {
	t := c.Theme()
	repo := st.repo
	fact := func(label, value string) {
		if value == "" {
			return
		}
		ui.Column(c).Gap(t.Space(0.5)).Children(func() {
			components.Text(c, label).FontSize(t.Rem(.8)).TextColor(t.TextMuted)
			components.Text(c, value).Bold()
		})
	}
	ui.Row(c).FillWidth().Gap(t.Space(5)).Wrap().Children(func() {
		fact(locales.RepoStars(), compactCount(repo.Stars))
		if st.full() {
			fact(locales.RepoForks(), compactCount(repo.Forks))
			fact(locales.RepoIssues(), compactCount(repo.OpenIssues))
			if !repo.Pushed.IsZero() {
				fact(locales.RepoUpdated(), locales.DateMedium(repo.Pushed))
			}
			fact(locales.RepoCreated(), locales.DateMedium(repo.Created))
		}
		fact(locales.RepoLanguage(), repo.Language)
		fact(locales.RepoLicense(), repo.License)
	})
}

// repoLinks are the links to the developer's other repositories and to the
// repository's pages.
func (s *screen) repoLinks(c *ui.Context, repo source.Repository) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(t.Space(3)).Wrap().Children(func() {
		if components.Button(c, locales.RepoMoreFrom(repo.Owner), components.ButtonLink).Clicked() {
			s.openOwner(repo.Owner)
		}
		if components.Button(c, locales.RepoOpen(), components.ButtonLink).Clicked() {
			s.openOnGitHub(repo.Ref())
		}
		if repo.Homepage != "" {
			if components.Button(c, locales.RepoWebsite(), components.ButtonLink).Clicked() {
				if err := s.openURL(repo.Homepage); err != nil {
					log.Printf("open %s: %v", repo.Homepage, err)
				}
			}
		}
	})
}

// repoReadme is the README as Markdown, or why there is none to show.
func (s *screen) repoReadme(c *ui.Context, st *repoDetails) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(t.Space(2)).Children(func() {
		components.Text(c, locales.RepoReadme()).Bold()
		switch {
		case st.loading:
			components.Text(c, locales.RepoReadmeLoading()).TextColor(t.TextMuted)
		case st.failure != "":
			components.Text(c, st.failure).TextColor(t.Danger).Wrap()
		case st.readme == "":
			components.Text(c, locales.RepoReadmeNone()).TextColor(t.TextMuted)
		default:
			ui.Scroll(c).FillWidth().Height(readmeViewHeight).Children(func() {
				markdown.View(c, st.readme, &s.markdown).Key(repoKey(st.repo.Ref()))
			})
		}
	})
}

// repoDialogActions are the dialog's buttons: save for later, close, add.
func (s *screen) repoDialogActions(c *ui.Context, repo source.Repository, inLibrary, saved bool) {
	if saved {
		if components.Button(c, locales.DiscoverCardRemoveTryLater(), components.ButtonOutline).Key("trylater").Clicked() {
			if entry, ok := s.catalog.FindTryLater(repo.Ref()); ok {
				s.removeFromTryLater(entry.ID, entry.FullName())
			}
		}
	} else if components.Button(c, locales.RepoTryLater(), components.ButtonOutline).Key("trylater").Clicked() {
		s.saveForLater(repo)
	}
	if components.Button(c, locales.Close(), components.ButtonSecondary).Clicked() {
		s.dialogOpen = false
	}
	if inLibrary {
		components.Button(c, locales.RepoInLibrary(), components.ButtonPrimary).Key("add").Disabled(true)
	} else if components.Button(c, locales.RepoAdd(), components.ButtonPrimary).Key("add").Clicked() {
		s.startAdd(repo.Ref())
	}
}
