package main

import (
	"strconv"
	"strings"

	"github.com/aaronflorey/release-sync/discover"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

const (
	discoverCardMinWidth = 320
	discoverCardHeight   = 176
	// The share of the allowance left below which its line turns amber and red.
	allowanceMid = 0.25
	allowanceEnd = 0.05
	// githubTokenLimit and githubAnonymousLimit are the hourly allowances the
	// explainer quotes.
	githubTokenLimit     = "5,000"
	githubAnonymousLimit = "60"
)

// categoryTitle is the name of a Discover category, from its key.
func categoryTitle(key string) string {
	switch key {
	case "games":
		return locales.DiscoverCategoryGames()
	case "emulators":
		return locales.DiscoverCategoryEmulators()
	case "utilities":
		return locales.DiscoverCategoryUtilities()
	case "media":
		return locales.DiscoverCategoryMedia()
	case "dev":
		return locales.DiscoverCategoryDev()
	case "privacy":
		return locales.DiscoverCategoryPrivacy()
	case "productivity":
		return locales.DiscoverCategoryProductivity()
	case "customize":
		return locales.DiscoverCategoryCustomize()
	case "graphics":
		return locales.DiscoverCategoryGraphics()
	case "communication":
		return locales.DiscoverCategoryCommunication()
	case "ai":
		return locales.DiscoverCategoryAi()
	case "internet":
		return locales.DiscoverCategoryInternet()
	case "cli":
		return locales.DiscoverCategoryCli()
	}
	return locales.DiscoverCategoryAll()
}

// tabTitle is the name of a Discover tab.
func tabTitle(tab discover.Tab) string {
	switch tab {
	case discover.Popular:
		return locales.DiscoverTabPopular()
	case discover.New:
		return locales.DiscoverTabNew()
	case discover.Updated:
		return locales.DiscoverTabUpdated()
	case discover.Featured:
		return locales.DiscoverTabFeatured()
	case discover.Stars:
		return locales.DiscoverTabStars()
	case discover.Trending:
	}
	return locales.DiscoverTabTrending()
}

func tabBlurb(tab discover.Tab) string {
	switch tab {
	case discover.Popular:
		return locales.DiscoverBlurbPopular()
	case discover.New:
		return locales.DiscoverBlurbNew()
	case discover.Updated:
		return locales.DiscoverBlurbUpdated()
	case discover.Featured:
		return locales.DiscoverBlurbFeatured()
	case discover.Stars:
		return locales.DiscoverBlurbStars()
	case discover.Trending:
	}
	return locales.DiscoverBlurbTrending()
}

// discoverView is the Discover page: the browsing lists, the results of a
// search, or one developer's repositories.
func (s *screen) discoverView(c *ui.Context) {
	d := &s.discoverUI
	t := c.Theme()
	rows := s.rowsOf()
	f := d.active()
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		if d.mode == modeBrowse {
			s.discoverToolbar(c)
		} else {
			s.discoverSideHeader(c)
		}
		s.discoverIntro(c)
		s.allowanceLine(c)
		if status := s.discoverStatus(f, len(rows)); status != "" {
			ui.Row(c).FillWidth().Shrink(0).Padding(0, pagePadding, t.Space(2), pagePadding).Children(func() {
				components.Text(c, status).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
			})
		}
		s.discoverBody(c, f, rows)
	})
}

// discoverToolbar holds the tabs, the category, the comparison, the download
// filter and the refresh button.
func (s *screen) discoverToolbar(c *ui.Context) {
	t := c.Theme()
	opts := s.discoverOptions()
	tabs := discover.Tabs()
	labels := make([]string, len(tabs))
	selected := 0
	for i, tab := range tabs {
		labels[i] = tabTitle(tab)
		if tab == opts.tab {
			selected = i
		}
	}
	ui.Column(c).FillWidth().Shrink(0).Padding(0, pagePadding, t.Space(2), pagePadding).Gap(t.Space(2)).Children(func() {
		chosen := selected
		components.TabsWith(c, &chosen, components.TabsOptions{}, labels...).List.Label(locales.DiscoverTabsLabel())
		if chosen != selected {
			s.setDiscoverChoice(func(p *preferences) { p.DiscoverTab = string(tabs[chosen]) })
		}
		components.Text(c, tabBlurb(opts.tab)).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Wrap()
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(t.Space(3)).Wrap().Children(func() {
			s.discoverChoices(c, opts)
		})
	})
}

// discoverChoices are the controls under the tabs that apply to the tab shown.
func (s *screen) discoverChoices(c *ui.Context, opts discoverOptions) {
	d := &s.discoverUI
	if opts.tab == discover.Stars {
		components.TextBox(c, &d.starsText, components.TextBoxOptions{LeadingIcon: lucide.Search()}).
			Label(locales.DiscoverFilterStars()).Placeholder(locales.DiscoverFilterStars()).
			Grow(1).Shrink(1).MinWidth(160).MaxWidth(320)
	} else {
		options := make([]option, 0, len(discover.Categories()))
		for _, cat := range discover.Categories() {
			options = append(options, option{cat.Key, categoryTitle(cat.Key)})
		}
		if key, changed := dropdown(c, locales.DiscoverCategoryLabel(), opts.category, options); changed {
			s.setDiscoverChoice(func(p *preferences) { p.DiscoverCategory = key })
		}
	}
	if opts.tab != discover.Stars {
		compare := []option{{string(discover.Day), locales.DiscoverCompareDay()}, {string(discover.Week), locales.DiscoverCompareWeek()}}
		if window, changed := dropdown(c, locales.DiscoverCompareLabel(), string(opts.window), compare); changed {
			s.setDiscoverChoice(func(p *preferences) { p.DiscoverCompare = window })
		}
	}
	if opts.tab != discover.Stars && opts.tab != discover.Featured {
		on := opts.downloads
		components.Switch(c, &on, locales.DiscoverDownloadsOnly())
		if on != opts.downloads {
			s.setDiscoverChoice(func(p *preferences) { p.DiscoverDownloadsOnly = on })
		}
	}
	if components.Button(c, locales.DiscoverRefresh(), components.ButtonSecondary).Disabled(d.browse.busy).Clicked() {
		s.loadBrowse(true)
	}
}

// discoverSideHeader is the title of a search or a developer's repositories and
// the way back.
func (s *screen) discoverSideHeader(c *ui.Context) {
	d := &s.discoverUI
	t := c.Theme()
	title := locales.DiscoverSearchTitle(d.query)
	if d.mode == modeOwner {
		title = locales.DiscoverOwnerTitle(d.owner)
	}
	ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(t.Space(3)).Padding(0, pagePadding, t.Space(3), pagePadding).Children(func() {
		back := components.IconButton(c, lucide.ArrowLeft(), locales.DiscoverBack(), components.IconButtonPlain)
		components.Tooltip(c, back, locales.DiscoverBack())
		if back.Clicked() {
			s.leaveSide()
		}
		components.Text(c, title).Bold().FontSize(t.Rem(1.1)).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
	})
}

// discoverIntro is the one-time explainer of the request allowance.
func (s *screen) discoverIntro(c *ui.Context) {
	if s.prefs.Get().DiscoverIntroSeen {
		return
	}
	t := c.Theme()
	limit := locales.DiscoverIntroWithoutToken(githubAnonymousLimit, githubTokenLimit)
	if s.discover.HasToken() {
		limit = locales.DiscoverIntroWithToken(githubTokenLimit)
	}
	ui.Row(c).FillWidth().Shrink(0).Padding(0, pagePadding, t.Space(3), pagePadding).Children(func() {
		ui.Column(c).FillWidth().Padding(t.Space(3)).Gap(t.Space(2)).Radius(t.Radius).Border(1, t.Border).Background(t.Surface).Children(func() {
			components.Text(c, locales.DiscoverIntroTitle()).Bold()
			components.Text(c, limit).Wrap()
			components.Text(c, locales.DiscoverIntroOptions(fullPageSize, lightPageSize)).TextColor(t.TextMuted).Wrap()
			ui.Row(c).Gap(t.Space(2)).Wrap().Children(func() {
				if components.Button(c, locales.DiscoverIntroGotIt(), components.ButtonPrimary).Clicked() {
					s.prefs.Update(func(p *preferences) { p.DiscoverIntroSeen = true })
				}
				if components.Button(c, locales.DiscoverIntroLight(), components.ButtonSecondary).Clicked() {
					s.setDiscoverChoice(func(p *preferences) { p.DiscoverLight, p.DiscoverIntroSeen = true, true })
				}
				if !s.discover.HasToken() {
					if components.Button(c, locales.GitHubCardOpensSettings(), components.ButtonGhost).Clicked() {
						s.openSettings(settingsGitHub)
					}
				}
			})
		})
	})
}

// allowanceLine says how many requests are left this hour. It turns amber and
// then red as they run down.
func (s *screen) allowanceLine(c *ui.Context) {
	a := s.github.Allowance()
	if !a.Known {
		return
	}
	t := c.Theme()
	text := locales.DiscoverAllowanceNoReset(a.Remaining, a.Limit)
	if !a.Reset.IsZero() {
		text = locales.DiscoverAllowance(a.Remaining, a.Limit, a.Reset)
	}
	color := t.TextMuted
	switch share := float64(a.Remaining) / float64(max(a.Limit, 1)); {
	case a.Remaining <= 0 || share < allowanceEnd:
		color = t.Danger
	case share < allowanceMid:
		color = t.Warning
	}
	ui.Row(c).FillWidth().Shrink(0).Padding(0, pagePadding, t.Space(1), pagePadding).Children(func() {
		components.Text(c, text).FontSize(t.Rem(.85)).TextColor(color).Wrap()
	})
}

// discoverStatus is the line under the toolbar: how many projects are shown,
// how old the list is, and what movement is measured against.
func (s *screen) discoverStatus(f *discoverFeed, shown int) string {
	d := &s.discoverUI
	if f.busy && shown == 0 || f.failure != "" || f.needsToken {
		return ""
	}
	if d.mode == modeSearch {
		return s.searchStatus(f, shown)
	}
	if d.mode == modeOwner || !f.loaded {
		return ""
	}
	parts := []string{locales.DiscoverStatusShown(shown)}
	if d.opts.tab == discover.Stars {
		if d.starsText != "" {
			parts[0] = locales.DiscoverStatusStars(len(d.matching), len(d.ordered))
		} else {
			parts[0] = locales.DiscoverStatusStars(len(d.ordered), len(d.ordered))
		}
		return strings.Join(parts, "  ·  ")
	}
	n := f.notes
	if !n.fetched.IsZero() {
		parts = append(parts, locales.DiscoverStatusUpdated(n.fetched))
	}
	switch {
	case n.base != nil:
		kind, days := n.base.Since()
		switch kind {
		case discover.SinceYesterday:
			parts = append(parts, locales.DiscoverStatusMovementYesterday())
		case discover.SinceLastWeek:
			parts = append(parts, locales.DiscoverStatusMovementWeek())
		case discover.SinceDays:
			parts = append(parts, locales.DiscoverStatusMovementDays(days))
		}
	case d.opts.tab != discover.Featured:
		parts = append(parts, locales.DiscoverStatusFirstVisit())
	}
	if n.stale {
		parts = append(parts, locales.DiscoverStatusStale())
	}
	if n.partial {
		parts = append(parts, locales.DiscoverStatusPartial())
	}
	if n.paused {
		parts = append(parts, locales.DiscoverStatusPaused())
	}
	return strings.Join(parts, "  ·  ")
}

func (s *screen) searchStatus(f *discoverFeed, shown int) string {
	if shown == 0 {
		return ""
	}
	return locales.DiscoverSearchCount(shown, min(f.total, discover.MaxSearchResults))
}

// discoverBody is the list of projects, or why there is none.
func (s *screen) discoverBody(c *ui.Context, f *discoverFeed, rows []discoverRow) {
	t := c.Theme()
	switch {
	case f.failure != "":
		ui.Column(c).Fill().Padding(24).AlignItems(ui.Center).Justify(ui.Center).Gap(t.Space(3)).Children(func() {
			components.Text(c, f.failure).TextColor(t.Danger).Wrap()
			if components.Button(c, locales.DiscoverTryAgain(), components.ButtonSecondary).Clicked() {
				s.retryDiscover()
			}
		})
	case f.needsToken:
		ui.Column(c).Fill().Padding(24).AlignItems(ui.Center).Justify(ui.Center).Gap(t.Space(3)).Children(func() {
			components.Text(c, locales.DiscoverStarsNeedToken()).TextColor(t.TextMuted).Wrap()
			if components.Button(c, locales.GitHubCardOpensSettings(), components.ButtonSecondary).Clicked() {
				s.openSettings(settingsGitHub)
			}
		})
	case len(rows) == 0 && (f.busy || !f.loaded):
		ui.Column(c).Fill().AlignItems(ui.Center).Justify(ui.Center).Gap(t.Space(2)).Children(func() {
			components.Spinner(c, components.SpinnerRing).Size(t.Rem(1.5), t.Rem(1.5))
			components.Text(c, locales.DiscoverLoading()).TextColor(t.TextMuted)
		})
	case len(rows) == 0:
		s.emptyState(c, "", s.discoverEmptyText())
	default:
		s.discoverGrid(c, f, rows)
	}
}

// discoverEmptyText says why a list that loaded has nothing in it.
func (s *screen) discoverEmptyText() string {
	d := &s.discoverUI
	switch {
	case d.mode == modeSearch:
		return locales.DiscoverSearchNone(d.query)
	case d.mode == modeOwner:
		return locales.DiscoverOwnerNone(d.owner)
	case d.opts.tab == discover.Stars && d.starsText != "":
		return locales.DiscoverStarsNoMatch()
	case d.opts.tab == discover.Stars:
		return locales.DiscoverStarsEmpty()
	case d.opts.downloads && d.opts.tab != discover.Featured:
		return locales.DiscoverEmptyDownloads()
	}
	return locales.DiscoverEmpty()
}

// retryDiscover loads again what the page was showing.
func (s *screen) retryDiscover() {
	d := &s.discoverUI
	switch d.mode {
	case modeSearch:
		s.startSearch(d.query)
	case modeOwner:
		s.openOwner(d.owner)
	case modeBrowse:
		s.loadBrowse(true)
	}
}

// discoverGrid lays the projects out in a virtualised grid with a button under
// it that loads more.
func (s *screen) discoverGrid(c *ui.Context, f *discoverFeed, rows []discoverRow) {
	d := &s.discoverUI
	t := c.Theme()
	held := s.held()
	d.grid.Key = func(i int) any {
		if i < 0 || i >= len(rows) {
			return i
		}
		return repoKey(rows[i].repo.Ref())
	}
	ui.Box(c).FillWidth().Grow(1).MinHeight(0).Padding(0, pagePadding-2, 0, pagePadding-2).Children(func() {
		components.GridView(c, &d.grid, len(rows), discoverCardMinWidth, discoverCardHeight, func(i int) {
			s.discoverCard(c, rows[i], held)
		}).Fill()
	})
	if f.more || s.moreStars() {
		ui.Row(c).FillWidth().Shrink(0).Justify(ui.Center).Padding(t.Space(2), 0).Children(func() {
			if components.Button(c, locales.DiscoverLoadMore(), components.ButtonSecondary).Disabled(f.busy).Clicked() {
				s.loadMore()
			}
		})
	}
}

// discoverCard is one project: its place, name, description, numbers and the
// buttons to add it, save it or look closer. Clicking the top opens its details.
func (s *screen) discoverCard(c *ui.Context, row discoverRow, held heldSets) {
	t := c.Theme()
	repo := row.repo
	ref := repo.Ref()
	name := ref.String()
	key := repoKey(ref)
	inLibrary, saved := held.library[key], held.saved[key]
	ui.Column(c).Key(key).Fill().Padding(t.Space(3)).Gap(t.Space(2)).Radius(t.Radius).Border(1, t.Border).Background(t.Surface).Children(func() {
		open := ui.ButtonBase(c).Key("open").Label(locales.OpenDetails(name)).FillWidth().Grow(1).MinHeight(0).
			Justify(ui.Start).AlignItems(ui.Start).TextColor(t.Text).Background(ui.Color{})
		open.Children(func() {
			ui.Column(c).Fill().MinWidth(0).Gap(t.Space(2)).Children(func() {
				ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(t.Space(2.5)).Children(func() {
					if row.rank > 0 {
						components.Text(c, "#"+strconv.Itoa(row.rank)).FontSize(t.Rem(.85)).TextColor(t.TextMuted).Shrink(0)
					}
					components.Avatar(c, repo.Name, components.AvatarOptions{Image: s.avatars.Get(repo.AvatarURL), Size: avatarSize})
					ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
						components.Text(c, repo.Name).Bold().SingleLine().Ellipsis("…")
						components.Text(c, name).FontSize(t.Rem(.8)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
					})
					if saved {
						components.Icon(c, lucide.BookmarkCheck()).Size(t.Rem(1.1), t.Rem(1.1)).TextColor(t.Accent).Shrink(0)
					}
				})
				components.Text(c, repo.Description).FontSize(t.Rem(.85)).TextColor(t.TextMuted).MaxLines(2).Ellipsis("…")
				s.discoverFacts(c, row)
			})
		})
		if open.Clicked() {
			s.showRepository(repo)
		}
		s.discoverCardActions(c, repo, inLibrary, saved)
	})
}

// discoverFacts is the card's line of stars, language, platform badge and movement.
func (s *screen) discoverFacts(c *ui.Context, row discoverRow) {
	t := c.Theme()
	ui.Row(c).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(t.Space(2)).Wrap().Children(func() {
		components.Text(c, locales.DiscoverStars(compactCount(row.repo.Stars))).FontSize(t.Rem(.85))
		if row.repo.Language != "" {
			components.Text(c, row.repo.Language).FontSize(t.Rem(.85)).TextColor(t.TextMuted).SingleLine()
		}
		if row.answer == discover.Has {
			components.Badge(c, locales.DiscoverBadgeDownload(), components.BadgeOptions{Tone: components.BadgeSuccess})
		}
		s.movementText(c, row.move)
	})
}

// movementText shows how a project moved since the ranking it is compared with.
func (s *screen) movementText(c *ui.Context, m discover.Movement) {
	t := c.Theme()
	var text string
	color := t.TextMuted
	switch m.Kind {
	case discover.MoveNone:
		return
	case discover.MoveUp:
		text, color = locales.DiscoverMoveUp(m.Places), t.Success
	case discover.MoveDown:
		text, color = locales.DiscoverMoveDown(m.Places), t.Danger
	case discover.MoveSame:
		text = locales.DiscoverMoveSame()
	case discover.MoveNew:
		text, color = locales.DiscoverMoveNew(), t.Accent
	}
	components.Text(c, text).FontSize(t.Rem(.85)).TextColor(color).Bold().SingleLine().
		Description(movementLabel(m))
	if m.Gained > 0 {
		components.Text(c, locales.DiscoverGained(compactCount(m.Gained))).FontSize(t.Rem(.85)).TextColor(t.TextMuted).SingleLine()
	}
}

// movementLabel says a movement in words, for the accessibility description.
func movementLabel(m discover.Movement) string {
	switch m.Kind {
	case discover.MoveUp:
		return locales.DiscoverMoveUpLabel(m.Places)
	case discover.MoveDown:
		return locales.DiscoverMoveDownLabel(m.Places)
	case discover.MoveSame:
		return locales.DiscoverMoveSameLabel()
	case discover.MoveNew:
		return locales.DiscoverMoveNewLabel()
	case discover.MoveNone:
	}
	return ""
}

// discoverCardActions is the card's button to add the project and its menu.
func (s *screen) discoverCardActions(c *ui.Context, repo source.Repository, inLibrary, saved bool) {
	t := c.Theme()
	ref := repo.Ref()
	ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Gap(t.Space(2)).Children(func() {
		if inLibrary {
			components.Button(c, locales.DiscoverCardInLibrary(), components.ButtonSecondary).Key("add").Disabled(true)
		} else if components.Button(c, locales.DiscoverCardAdd(), components.ButtonPrimary).Key("add").Clicked() {
			s.startAdd(ref)
		}
		ui.Spacer(c).Grow(1)
		more := components.IconButton(c, lucide.Ellipsis(), locales.DiscoverCardMenu(ref.String()), components.IconButtonPlain).Key("menu")
		components.MenuButton(c, more, func(m *components.Menu) {
			if m.Item(locales.DiscoverCardDetails()).Chosen() {
				s.showRepository(repo)
			}
			s.tryLaterMenuItem(m, repo, saved)
			if m.Item(locales.DiscoverCardMore()).Chosen() {
				s.openOwner(repo.Owner)
			}
			if m.Item(locales.DiscoverCardOpen()).Chosen() {
				s.openOnGitHub(ref)
			}
		})
	})
}

// tryLaterMenuItem is the menu item that saves a repository to try later, or
// takes it off the list when it is saved.
func (s *screen) tryLaterMenuItem(m *components.Menu, repo source.Repository, saved bool) {
	if !saved {
		if m.Item(locales.DiscoverCardTryLater()).Chosen() {
			s.saveForLater(repo)
		}
		return
	}
	if m.Item(locales.DiscoverCardRemoveTryLater()).Chosen() {
		if entry, ok := s.catalog.FindTryLater(repo.Ref()); ok {
			s.removeFromTryLater(entry.ID, entry.FullName())
		}
	}
}
