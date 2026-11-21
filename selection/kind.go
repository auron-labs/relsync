package selection

import "slices"

var (
	toolTopics = []string{"cli", "command-line", "command-line-tool", "terminal", "tui"}
	appTopics  = []string{"gui", "desktop", "desktop-app", "electron", "tauri", "qt", "wpf", "winui"}
)

// GuessKind guesses what an Item is before anything is downloaded. A
// release with a real installer is an App; then the repository's topics speak;
// then files named for a compiler target, or a bare {name}.exe, mark a Tool;
// otherwise it is an App.
func GuessKind(assets []Asset, topics []string, repo string) ItemKind {
	var triple, bare bool
	for _, a := range assets {
		p := parseName(a.Name)
		class, kind := classify(p, ItemApp)
		if class != ClassCandidate {
			continue
		}
		if kind == ArtifactMSI || kind == ArtifactMSIX || (kind == ArtifactSetupExe && p.has(installerTokens...)) {
			return ItemApp
		}
		triple = triple || (p.has("pc", "unknown", "apple") && len(p.facts.os) > 0 && len(p.facts.arch) > 0)
		bare = bare || (p.ext == ".exe" && productKey(p.base[:len(p.base)-len(p.ext)]) == productKey(repo))
	}
	switch {
	case slices.ContainsFunc(topics, func(t string) bool { return slices.Contains(toolTopics, t) }):
		return ItemTool
	case slices.ContainsFunc(topics, func(t string) bool { return slices.Contains(appTopics, t) }):
		return ItemApp
	case triple || bare:
		return ItemTool
	}
	return ItemApp
}
