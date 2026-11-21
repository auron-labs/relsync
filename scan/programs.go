package scan

import (
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/platform"
)

// FromPrograms makes a candidate of each entry of the installed programs.
// Entries with the same display name are one candidate, and entries the
// Library already holds, by their key, are left out. The result is in name order.
func FromPrograms(programs []platform.Program, held map[string]bool) []Candidate {
	seen := map[string]bool{}
	var found []Candidate
	for _, p := range programs {
		name := strings.ToLower(p.Name)
		if held[p.Key] || seen[name] {
			continue
		}
		seen[name] = true
		found = append(found, Candidate{
			Key: "program:" + p.Key, Origin: OriginProgram, Name: p.Name, Publisher: p.Publisher, Version: p.Version,
			Program: p, Texts: []string{p.UpdateURL, p.AboutURL, p.HelpLink, p.Comments},
		})
	}
	sortByName(found)
	return found
}

func sortByName(candidates []Candidate) {
	slices.SortStableFunc(candidates, func(a, b Candidate) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
}
