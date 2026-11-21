package version

import (
	"testing"
	"time"
)

func TestIsNewerWithoutDates(t *testing.T) {
	for _, c := range []struct {
		latest, installed string
		want              bool
	}{
		// Real version numbers compare as numbers.
		{"v1.2.4", "v1.2.3", true},
		{"v1.2.3", "v1.2.4", false},
		{"v1.10.0", "v1.9.0", true},
		{"v0.4.6", "0.4.6", false},
		{"v1.22.0", "1.21.442", true},
		{"launcher@0.2.45", "launcher@0.2.31", true},
		{"v0.1.9.3", "v0.1.9.1", true},
		{"v2", "v1.5", true},
		{"v1.5", "v2", false},
		// A final release beats its own pre-release, never the reverse.
		{"v1.2.3", "v1.2.3-rc1", true},
		{"v1.2.3-rc1", "v1.2.3", false},
		{"v1.2.3", "v1.2.3-beta.2", true},
		// Build labels of one style compare by all their numbers.
		{"build-83", "build-75", true},
		{"build-75", "build-83", false},
		{"nx2-build-34", "nx2-build-25", true},
		{"nx2-build-25", "nx2-build-34", false},
		{"daily-20261106", "daily-20261105", true},
		// Unlike labels with no dates: a difference is an update, but a real
		// version number outranks a label and the two directions never both win.
		{"nx2-build-90", "build-83", true},
		{"latest", "stable", true},
		{"v2.0.11", "honest-eta", true},
		{"honest-eta", "v2.0.11", false},
		{"1.4", "nightly", true},
		{"nightly", "1.4", false},
		// The same version is never newer.
		{"build-83", "build-83", false},
		{"  v1.0 ", "1.0", false},
		// Pre-release words match whole words only.
		{"v1.0.0", "v1.0.0-predator", false},
	} {
		if got := IsNewer(c.latest, c.installed, time.Time{}, time.Time{}); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.latest, c.installed, got, c.want)
		}
	}
}

func TestIsNewerAcrossStylesUsesPublishDates(t *testing.T) {
	older := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		latest, installed string
		latestDate        time.Time
		installedDate     time.Time
		want              bool
	}{
		{"nx2-build-90", "build-83", newer, older, true},
		{"nx2-build-34", "build-83", older, newer, false},
		{"daily-20261105", "build-212", newer, older, true},
		{"build-212", "daily-20261105", older, newer, false},
		// Dates never override two tags of the same style.
		{"build-83", "build-75", older, newer, true},
		// An unknown date on either side leaves the label rules in charge.
		{"nx2-build-34", "build-83", older, time.Time{}, true},
	} {
		if got := IsNewer(c.latest, c.installed, c.latestDate, c.installedDate); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.latest, c.installed, got, c.want)
		}
	}
}

func TestIsOneOff(t *testing.T) {
	for tag, want := range map[string]bool{
		"branch-fix-oot-blackscreen-17ac56ed": true,
		"pr-1234":                             true,
		"feature/new-ui":                      true,
		"test_build":                          true,
		"Scratch-1":                           true,
		"daily-20261105":                      false,
		"build-83":                            false,
		"v1.2.3":                              false,
		"branchless-1.0":                      false,
		"prerelease-1":                        false,
	} {
		if got := IsOneOff(tag); got != want {
			t.Errorf("IsOneOff(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestMatchTag(t *testing.T) {
	tags := []string{"v2.0.11", "v2.0.10", "v2.0.8", "v2.0.8-rc.5", "26.06", "honest-eta"}
	for _, c := range []struct{ seen, want string }{
		{"2.0.11", "v2.0.11"},
		{"2.0.11.0", "v2.0.11"},
		{"v2.0.11", "v2.0.11"},
		// The release, not its release candidate.
		{"2.0.8", "v2.0.8"},
		{"26.06", "26.06"},
		// A placeholder file version matches nothing.
		{"1.0.0", ""},
		{"2.0.12", ""},
		{"", ""},
	} {
		if got := MatchTag(c.seen, tags); got != c.want {
			t.Errorf("MatchTag(%q) = %q, want %q", c.seen, got, c.want)
		}
	}
	// With only a pre-release of those numbers available, it is still the match.
	if got := MatchTag("2.0.8", []string{"v2.0.8-rc.5", "v2.0.7"}); got != "v2.0.8-rc.5" {
		t.Errorf("MatchTag fell back to %q", got)
	}
	// A release listed after its pre-release still wins.
	if got := MatchTag("2.0.8.0", []string{"v2.0.8-rc.5", "v2.0.8"}); got != "v2.0.8" {
		t.Errorf("MatchTag preferred %q", got)
	}
}

func TestLane(t *testing.T) {
	for tag, want := range map[string]string{
		"cli-v1.2.3":     "cli-v",
		"desktop-v0.4.0": "desktop-v",
		"v1.0.0":         "",
		"1.0.0":          "",
		"V2":             "",
		"build-83":       "build-",
		"Nightly":        "nightly",
	} {
		if got := Lane(tag); got != want {
			t.Errorf("Lane(%q) = %q, want %q", tag, got, want)
		}
	}
}
