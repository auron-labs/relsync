// Package version compares release tags. Tags are labels people chose, not
// strict semantic versions, so comparison falls back from numbers to dates.
package version

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	digits    = regexp.MustCompile(`\d+`)
	numberRun = regexp.MustCompile(`\d+(?:\.\d+)*`)
	// dotted matches a real version number such as v1.2.3 or launcher@0.2.45,
	// or a bare number such as v2, as opposed to a build label like build-83.
	dotted = regexp.MustCompile(`^(?:\D*\d+(?:\.\d+)+|[vV]?\d+\b)`)
	// preRelease matches a marker as a whole word, so rc1 and rc.5 count but
	// predator and develop do not.
	preRelease = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:alpha|beta|rc|preview|pre|dev|nightly|canary|snapshot)(?:[^a-z]|$)`)
	oneOff     = regexp.MustCompile(`(?i)^(?:branch|pr|feature|test|scratch)[-_/]`)
)

// Clean trims whitespace and a leading v that introduces a number.
func Clean(tag string) string {
	tag = strings.TrimSpace(tag)
	if len(tag) > 1 && (tag[0] == 'v' || tag[0] == 'V') && tag[1] >= '0' && tag[1] <= '9' {
		return tag[1:]
	}
	return tag
}

// IsOneOff reports a throwaway build: a developer's branch, pull request or
// experiment rather than something most people mean by a version.
func IsOneOff(tag string) bool { return oneOff.MatchString(tag) }

// IsPreRelease reports a tag carrying a pre-release marker.
func IsPreRelease(tag string) bool { return preRelease.MatchString(tag) }

// Lane returns the tag's prefix family: the text before its first digit,
// lowercased, so cli-v1.2 and cli-v1.3 share one lane. A bare v prefix is the
// same family as no prefix.
func Lane(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexFunc(tag, func(r rune) bool { return r >= '0' && r <= '9' }); i >= 0 {
		tag = tag[:i]
	}
	if tag == "v" {
		return ""
	}
	return tag
}

// IsNewer reports whether latest is a newer version than installed. A zero
// date means the publish date is unknown. The rules, in order: equal tags are
// not newer; two dotted versions compare numerically; tags of the same shape
// compare all their numbers; otherwise the later publish date wins; with no
// dates a dotted version beats a label and unlike labels count as different. A
// numeric tie is broken by a final release beating a pre-release.
func IsNewer(latest, installed string, latestDate, installedDate time.Time) bool {
	if Clean(latest) == Clean(installed) {
		return false
	}
	var order int
	switch {
	case dotted.MatchString(latest) && dotted.MatchString(installed):
		order = compareNumbers(numbers(firstRun(latest)), numbers(firstRun(installed)))
	case shape(latest) == shape(installed):
		order = compareNumbers(allNumbers(latest), allNumbers(installed))
	case !latestDate.IsZero() && !installedDate.IsZero():
		return latestDate.After(installedDate)
	case dotted.MatchString(latest) != dotted.MatchString(installed):
		return dotted.MatchString(latest)
	default:
		return true
	}
	if order != 0 {
		return order > 0
	}
	return IsPreRelease(installed) && !IsPreRelease(latest)
}

// MatchTag returns the release tag that a version seen on the machine belongs
// to, or "". A tag equal after cleaning wins; otherwise a dotted tag with equal
// numbers once trailing zeros are trimmed matches, preferring a final release
// over its pre-releases. 2.0.11.0 matches v2.0.11.
func MatchTag(seen string, tags []string) string {
	seenParts := numbers(firstRun(seen))
	if len(seenParts) < 2 {
		return ""
	}
	want := trimZeros(seenParts)
	for _, tag := range tags {
		if tag != "" && strings.EqualFold(Clean(tag), Clean(seen)) {
			return tag
		}
	}
	best, bestIsPre := "", false
	for _, tag := range tags {
		if tag == "" || !dotted.MatchString(tag) || !slices.Equal(trimZeros(numbers(firstRun(tag))), want) {
			continue
		}
		isPre := IsPreRelease(tag) || strings.Contains(tag, "-")
		if best == "" || (bestIsPre && !isPre) {
			best, bestIsPre = tag, isPre
		}
	}
	return best
}

// shape is the tag with its numbers blanked, so build-75 and build-83 match
// but nx2-build-34 does not.
func shape(tag string) string {
	return digits.ReplaceAllString(strings.ToLower(Clean(tag)), "#")
}

func firstRun(tag string) string { return numberRun.FindString(tag) }

func allNumbers(tag string) []int64 {
	return parseAll(digits.FindAllString(tag, -1))
}

func numbers(run string) []int64 {
	if run == "" {
		return nil
	}
	return parseAll(strings.Split(run, "."))
}

func parseAll(parts []string) []int64 {
	out := make([]int64, len(parts))
	for i, part := range parts {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			// Only an overflow can fail here: the parts are digits.
			n = math.MaxInt64
		}
		out[i] = n
	}
	return out
}

func compareNumbers(a, b []int64) int {
	for i := range max(len(a), len(b)) {
		var x, y int64
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func trimZeros(parts []int64) []int64 {
	for len(parts) > 1 && parts[len(parts)-1] == 0 {
		parts = parts[:len(parts)-1]
	}
	return parts
}
