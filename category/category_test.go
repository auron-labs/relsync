package category

import "testing"

func TestOfPicksTheCategoryOfRealRepositories(t *testing.T) {
	for _, c := range []struct {
		name, description string
		topics            []string
		want              Key
	}{
		{"dolphin", "Dolphin is a GameCube / Wii / Triforce emulator for Windows, Linux, macOS and Android.", []string{"emulator", "gamecube", "wii"}, Games},
		{"mpv", "Command line video player", []string{"video", "player", "mpv"}, Media},
		{"krita", "Krita is a free and open source cross-platform application that offers an end-to-end solution for creating digital art files from scratch.", []string{"painting", "drawing", "art"}, Graphics},
		{"joplin", "Joplin - the privacy-focused note taking app with sync capabilities for Windows, macOS, Linux, Android and iOS.", []string{"notes", "markdown", "todo"}, Productivity},
		{"bitwarden", "Bitwarden infrastructure/deployment scripts", []string{"password-manager", "vault"}, Security},
		{"yt-dlp", "A feature-rich command-line audio/video downloader", []string{"downloader", "youtube"}, Internet},
		{"Element", "A glossy Matrix collaboration client for the web.", []string{"matrix", "chat", "messaging"}, Communication},
		{"ollama", "Get up and running with large language models.", []string{"llm", "ollama", "ai"}, AI},
		{"PowerToys", "Windows system utilities to maximize productivity", []string{"powertoys", "utilities", "windows-tweaks"}, System},
		{"neovim", "Vim-fork focused on extensibility and usability", []string{"neovim", "vim", "text-editor"}, Developer},
	} {
		if got := Of(c.name, c.description, c.topics); got != c.want {
			t.Errorf("Of(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOfIsOtherWhenNothingScoresEnough(t *testing.T) {
	for _, c := range []struct {
		name, description string
		topics            []string
	}{
		{"zzz", "", nil},
		{"widget", "A small thing for a thing", []string{"misc"}},
		// One weak word in the description is worth less than the threshold.
		{"widget", "A fun mod", nil},
	} {
		if got := Of(c.name, c.description, c.topics); got != Other {
			t.Errorf("Of(%q, %q, %v) = %q, want Other", c.name, c.description, c.topics, got)
		}
	}
}

func TestOfFindsWordsWherePeopleWriteThem(t *testing.T) {
	for _, c := range []struct {
		name  string
		input [3]any
		want  Key
	}{
		{"a camel-case name is split into words", [3]any{"VideoPlayer", "", []string(nil)}, Media},
		{"a topic counts through its hyphenated parts", [3]any{"widget", "", []string{"retro-handheld"}}, Games},
		{"a phrase is found whole in the description", [3]any{"widget", "Your password manager, with zero-knowledge storage", []string(nil)}, Security},
		{"a name fragment is found anywhere in the name", [3]any{"xdolphinx", "", []string(nil)}, Games},
	} {
		got := Of(c.input[0].(string), c.input[1].(string), c.input[2].([]string))
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// synthetic is a repository whose name holds the words the rules below look
// for, each worth 3 to a rule that lists it as strong.
func synthetic(name string) evidence { return newEvidence(name, "", nil) }

func TestTheFirstListedCategoryWinsATie(t *testing.T) {
	list := []rule{
		{Key: Games, Strong: []string{"alpha"}},
		{Key: Media, Strong: []string{"beta"}},
	}
	if got := choose(list, synthetic("alpha beta")); got != Games {
		t.Errorf("tie went to %q, want the first listed", got)
	}
	if got := choose([]rule{list[1], list[0]}, synthetic("alpha beta")); got != Media {
		t.Errorf("tie went to %q, want the first listed", got)
	}
}

func TestAWeightedCategoryNeedsMoreEvidenceThanASpecificOne(t *testing.T) {
	general := rule{Key: Developer, Strong: []string{"alpha", "gamma"}, Weight: 0.75}
	specific := rule{Key: Games, Strong: []string{"beta", "delta"}}
	// Equal raw totals: 6 for each. Weighted, the general one is worth 4.5.
	if got := choose([]rule{general, specific}, synthetic("alpha gamma beta delta")); got != Games {
		t.Errorf("equal evidence went to %q, want the unweighted category", got)
	}
	// With more raw evidence than the specific one, the general category still wins.
	if got := choose([]rule{specific, general}, synthetic("alpha gamma beta")); got != Developer {
		t.Errorf("6 weighted to 4.5 beat 3: got %q", got)
	}
	// The weight can push a category under the threshold: 2 * 0.75 is 1.5.
	small := rule{Key: Developer, Strong: []string{"alpha"}, Weight: 0.75}
	if got := choose([]rule{small}, newEvidence("", "alpha", nil)); got != Other {
		t.Errorf("a weighted 1.5 reached the threshold: %q", got)
	}
	if got := choose([]rule{{Key: Developer, Strong: []string{"alpha"}}}, newEvidence("", "alpha", nil)); got != Developer {
		t.Errorf("an unweighted 2 missed the threshold: %q", got)
	}
}

func TestEveryCategoryHasEvidenceAndOtherIsLast(t *testing.T) {
	keys := All()
	if len(keys) != 11 || keys[len(keys)-1] != Other {
		t.Fatalf("All() = %v", keys)
	}
	for _, r := range rules() {
		if len(r.Strong) == 0 || len(r.Weak) == 0 || len(r.NameFragments) == 0 {
			t.Errorf("%s has an empty list", r.Key)
		}
	}
}

func TestAIFragmentNeedsItsHyphenAtTheStartOfAWord(t *testing.T) {
	for _, name := range []string{"plain", "mainframe", "Thai-recipes", "maintain"} {
		if got := Of(name, "", nil); got != Other {
			t.Errorf("Of(%q) = %q, want Other: \"ai\" is only part of a word", name, got)
		}
	}
	for _, name := range []string{"ai-studio", "my-ai-studio"} {
		if got := Of(name, "", nil); got != AI {
			t.Errorf("Of(%q) = %q, want AI", name, got)
		}
	}
}
