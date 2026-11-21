package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/aaronflorey/release-sync/hints"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"github.com/oneo-me/onevra/source/icons/lucide"
)

// safetyHints works out the hints for a repository: from what the site says
// about it and, on GitHub, about its owner. It asks for the owner, which costs
// a request, only when the person wants hints. latest is the repository's
// newest release, nil when it is not known. It is nil when hints are off or
// the repository's details are not known, and it runs off the UI thread.
func (s *screen) safetyHints(ctx context.Context, repo source.Repository, latest *source.Release) []hints.Hint {
	if !s.prefs.Get().SafetyHints || repo.Created.IsZero() {
		return nil
	}
	facts := hints.Facts{Repository: repo, Latest: latest, Now: time.Now()}
	if source.IsGitHub(repo.Source) {
		owner, err := s.github.Owner(ctx, repo.Owner)
		switch {
		case err == nil:
			facts.Owner = &owner
		case ctx.Err() == nil:
			// Hints are a bonus: without the owner there are fewer of them.
			log.Printf("look up the owner %s: %v", repo.Owner, err)
		}
	}
	return hints.Build(facts)
}

// atAGlance lists the hints, warnings first.
func (s *screen) atAGlance(c *ui.Context, list []hints.Hint) {
	if len(list) == 0 {
		return
	}
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(t.Space(1.5)).Children(func() {
		components.Text(c, locales.HintsTitle()).Bold()
		for i, h := range list {
			icon, color := lucide.Info(), t.TextMuted
			switch h.Level {
			case hints.Warning:
				icon, color = lucide.TriangleAlert(), t.Warning
			case hints.Good:
				icon, color = lucide.CircleCheck(), t.Success
			case hints.Info:
			}
			ui.Row(c).Key(strconv.Itoa(i)).FillWidth().Gap(t.Space(2)).Children(func() {
				components.Icon(c, icon).Size(t.Rem(1), t.Rem(1)).TextColor(color).Shrink(0)
				components.Text(c, hintText(h)).TextColor(t.Text).Wrap().Shrink(1).MinWidth(0)
			})
		}
	})
}

// hintText is the plain-language sentence for a hint.
func hintText(h hints.Hint) string {
	switch h.Signal {
	case hints.NewRepository:
		return locales.HintNewRepository(ageText(h.Age))
	case hints.EstablishedRepository:
		return locales.HintEstablishedRepository(ageText(h.Age))
	case hints.Archived:
		return locales.HintArchived()
	case hints.Stale:
		return locales.HintStale(h.Pushed)
	case hints.FewStars:
		return locales.HintFewStars(h.Stars)
	case hints.ManyStars:
		return locales.HintManyStars(compactCount(h.Stars))
	case hints.NoLicense:
		return locales.HintNoLicense()
	case hints.Fork:
		return locales.HintFork()
	case hints.Checksums:
		return locales.HintChecksums(appName)
	case hints.NewOwner:
		return locales.HintNewOwner(ageText(h.Age))
	case hints.Organization:
		if h.Name != "" {
			return locales.HintOrganizationNamed(h.Name)
		}
		return locales.HintOrganization()
	}
	return ""
}

// ageText writes an age the way a person would: days while it is young, then
// months, then years to a tenth.
func ageText(d time.Duration) string {
	days := d.Hours() / 24
	switch {
	case days < 60:
		return locales.AgeDays(int(days))
	case days < 700:
		return locales.AgeMonths(int(days / 30))
	}
	return locales.AgeYears(fmt.Sprintf("%.1f", days/365))
}
