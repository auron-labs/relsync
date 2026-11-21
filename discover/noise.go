package discover

import (
	"strings"
	"sync"

	"github.com/aaronflorey/release-sync/source"
)

// noisePatterns say which repositories are not software: link collections.
type noisePatterns struct {
	NameStartsWith        []string `json:"nameStartsWith"`
	NameEndsWith          []string `json:"nameEndsWith"`
	NameContains          []string `json:"nameContains"`
	DescriptionContains   []string `json:"descriptionContains"`
	DescriptionStartsWith []string `json:"descriptionStartsWith"`
}

var noise = sync.OnceValue(func() noisePatterns {
	var p noisePatterns
	load("data/noise.json", &p)
	return p
})

// IsNoise reports a repository that no list shows: archived, with no stars, or
// a collection of links rather than software.
func IsNoise(r source.Repository) bool {
	if r.Archived || r.Stars < 1 {
		return true
	}
	p := noise()
	name, description := strings.ToLower(r.Name), strings.ToLower(r.Description)
	return anyOf(p.NameStartsWith, func(s string) bool { return strings.HasPrefix(name, s) }) ||
		anyOf(p.NameEndsWith, func(s string) bool { return strings.HasSuffix(name, s) }) ||
		anyOf(p.NameContains, func(s string) bool { return strings.Contains(name, s) }) ||
		anyOf(p.DescriptionContains, func(s string) bool { return strings.Contains(description, s) }) ||
		anyOf(p.DescriptionStartsWith, func(s string) bool { return strings.HasPrefix(description, s) })
}

func anyOf(patterns []string, matches func(string) bool) bool {
	for _, p := range patterns {
		if matches(p) {
			return true
		}
	}
	return false
}

// MatchesText reports whether text appears in the repository's name,
// description, language or topics, ignoring case. Empty text matches all.
func MatchesText(r source.Repository, text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return true
	}
	fields := append([]string{r.Owner + "/" + r.Name, r.Description, r.Language}, r.Topics...)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), text) {
			return true
		}
	}
	return false
}
