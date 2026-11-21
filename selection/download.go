package selection

// newestChecked is how many of the newest releases HasDownload looks at.
const newestChecked = 4

// HasDownload reports whether a repository has a download for this machine:
// whether any of its newest four releases (newest first, each a list of files)
// has a file that could be installed, as an App or as a Tool. It is stricter
// than choosing a file for an Item: an archive only counts when its name states
// the operating system, since otherwise it may be for another one.
func HasDownload(releases [][]Asset, repo string, policy Policy) bool {
	for _, files := range releases[:min(len(releases), newestChecked)] {
		for _, kind := range []ItemKind{ItemApp, ItemTool} {
			res := Resolve(Request{Assets: files, Repo: repo, Kind: kind, Policy: policy})
			for _, c := range res.Candidates {
				if !c.ByHand && (c.Kind != ArtifactArchive || len(c.OS) > 0) {
					return true
				}
			}
		}
	}
	return false
}
