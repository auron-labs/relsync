package main

import (
	"context"
	"errors"
	"log"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/scan"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

// drawerGuessedLink asks whether a repository matched by name is the project the
// program comes from, with the ways to say yes or put it right.
func (s *screen) drawerGuessedLink(c *ui.Context, e catalog.Entry) {
	if !e.Item.LinkGuessed {
		return
	}
	t := c.Theme()
	ui.Column(c).FillWidth().Padding(t.Space(3)).Gap(t.Space(2)).Radius(t.Radius).Border(1, t.Warning).Children(func() {
		components.Text(c, locales.LinkGuessTitle()).Bold().Wrap()
		components.Text(c, locales.LinkGuessBody(appName)).TextColor(t.TextMuted).Wrap()
		ui.Row(c).FillWidth().Wrap().Gap(t.Space(2)).Children(func() {
			if components.Button(c, locales.LinkGuessYes(), components.ButtonSecondary).Key("link-yes").Clicked() {
				s.confirmLink(e)
			}
			if components.Button(c, locales.LinkGuessChange(), components.ButtonSecondary).Key("link-change").Clicked() {
				s.openRelinkDialog(e)
			}
			if components.Button(c, locales.LinkGuessFind(), components.ButtonSecondary).Key("link-find").Clicked() {
				s.openBetterMatch(e)
			}
		})
	})
}

// confirmLink records that the Item's repository is the right project.
func (s *screen) confirmLink(e catalog.Entry) {
	s.mutate(e, func(ctx context.Context) error { return s.catalog.SetLinkVerified(ctx, e.Item.ID) },
		locales.ActivityLinkConfirmed(e.Title(), e.FullName()))
}

// relinkState is the dialog for naming the right repository.
type relinkState struct {
	text    string
	problem string
}

// openRelinkDialog asks for the address of the repository the Item should follow.
func (s *screen) openRelinkDialog(e catalog.Entry) {
	st := &relinkState{}
	s.showDialog(locales.RelinkTitle(e.Title()), func(c *ui.Context) {
		t := c.Theme()
		ui.Column(c).Gap(t.Space(3)).MinWidth(400).MaxWidth(520).Children(func() {
			components.Text(c, locales.RelinkTitle(e.Title())).Bold().FontSize(t.Rem(1.1)).Wrap()
			components.Text(c, locales.RelinkBody(e.FullName(), appName)).TextColor(t.TextMuted).Wrap()
			box := components.TextBox(c, &st.text, components.TextBoxOptions{LeadingIcon: lucide.Link()}).
				Label(locales.ScanLinkLabel()).Placeholder(locales.ScanLinkPlaceholder()).FillWidth()
			submitted := box.Submitted()
			if st.problem != "" {
				components.Text(c, st.problem).TextColor(t.Danger).Wrap()
			}
			ui.Row(c).FillWidth().Gap(t.Space(2)).Justify(ui.End).Children(func() {
				if components.Button(c, locales.Cancel(), components.ButtonOutline).Clicked() {
					s.dialogOpen = false
				}
				if components.Button(c, locales.RelinkConfirm(), components.ButtonPrimary).Clicked() || submitted {
					ref, ok := s.sources.ParseLink(st.text)
					if !ok {
						st.problem = locales.ScanLinkInvalid()
						return
					}
					s.dialogOpen = false
					s.changeRepository(e, func(ctx context.Context) (catalog.Preview, error) { return s.catalog.Preview(ctx, ref) })
				}
			})
		})
	})
}

// changeRepository points the Item at the repository the preview reads.
func (s *screen) changeRepository(e catalog.Entry, preview func(context.Context) (catalog.Preview, error)) {
	s.background(func(ctx context.Context) error {
		p, err := preview(ctx)
		var changed catalog.Entry
		if err == nil {
			changed, err = s.catalog.ChangeRepository(ctx, e.Item.ID, p)
		}
		switch {
		case errors.Is(err, catalog.ErrExists):
			s.toast(components.ToastInfo, locales.ToastAlreadyTracked(changed.Title()))
		case ctx.Err() != nil:
		case err != nil:
			log.Printf("change the repository of %s: %v", e.FullName(), err)
			s.toast(components.ToastError, locales.ToastRepoChangeFailed(errorText(err)))
		default:
			s.record(ctx, activity.Success, e.Item.ID, locales.ActivityRepoChanged(e.Title(), changed.FullName()))
			s.toast(components.ToastSuccess, locales.ToastRepoChanged(e.Title(), changed.FullName()))
		}
		return nil //nolint:nilerr // A failure is shown to the person, and the job has nothing more to report.
	})
}

// betterMatchState is the dialog listing the repositories that fit an Item's name.
type betterMatchState struct {
	loading bool
	matches []scan.Match
	problem string
}

// openBetterMatch searches GitHub by the Item's name and offers the repositories
// that fit, but for the one it follows now.
func (s *screen) openBetterMatch(e catalog.Entry) {
	st := &betterMatchState{loading: true}
	d := s.showDialog(locales.BetterTitle(e.Title()), func(c *ui.Context) { s.betterMatchDialog(c, e, st) })
	s.background(func(ctx context.Context) error {
		matches, err := s.searchMatches(ctx, e)
		s.onUI(func() {
			if s.dialog != d {
				return
			}
			st.loading, st.matches = false, matches
			if err != nil && ctx.Err() == nil {
				st.problem = errorText(err)
			}
		})
		return nil
	})
}

// searchMatches searches by the Item's name, at the pace searches are allowed.
func (s *screen) searchMatches(ctx context.Context, e catalog.Entry) ([]scan.Match, error) {
	name := scan.CleanName(e.Title())
	if err := s.searchPacer().Wait(ctx); err != nil {
		return nil, err //nolint:wrapcheck // Only a canceled wait fails.
	}
	results, err := s.searcher.SearchRepositories(ctx, name)
	if err != nil {
		return nil, err //nolint:wrapcheck // The source says what it was searching.
	}
	var others []source.Repository
	for _, r := range results {
		if r.Ref() != e.Ref() {
			others = append(others, r)
		}
	}
	return scan.Matches(scan.Candidate{Name: e.Title()}, others), nil
}

func (s *screen) betterMatchDialog(c *ui.Context, e catalog.Entry, st *betterMatchState) {
	t := c.Theme()
	ui.Column(c).Gap(t.Space(3)).MinWidth(420).MaxWidth(560).Children(func() {
		components.Text(c, locales.BetterTitle(e.Title())).Bold().FontSize(t.Rem(1.1)).Wrap()
		switch {
		case st.loading:
			components.Text(c, locales.BetterSearching()).TextColor(t.TextMuted)
		case st.problem != "":
			components.Text(c, st.problem).TextColor(t.Danger).Wrap()
		case len(st.matches) == 0:
			components.Text(c, locales.BetterNone()).TextColor(t.TextMuted).Wrap()
		}
		for _, m := range st.matches {
			repo := m.Repo
			ui.Row(c).Key(repo.Ref().String()).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					components.Text(c, repo.Ref().String()).Bold().SingleLine().Ellipsis("…")
					components.Text(c, locales.AddStars(repo.Stars)).FontSize(t.Rem(.8)).TextColor(t.TextMuted)
					if repo.Description != "" {
						components.Text(c, repo.Description).FontSize(t.Rem(.85)).TextColor(t.TextMuted).MaxLines(2).Ellipsis("…")
					}
				})
				if components.Button(c, locales.BetterUse(), components.ButtonSecondary).Key(repo.Ref().String()).Label(locales.BetterRepository(repo.Ref().String(), e.Title())).Clicked() {
					s.dialogOpen = false
					s.changeRepository(e, func(ctx context.Context) (catalog.Preview, error) { return s.catalog.PreviewRepository(ctx, repo) })
				}
			})
		}
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			if components.Button(c, locales.Close(), components.ButtonOutline).Clicked() {
				s.dialogOpen = false
			}
		})
	})
}
