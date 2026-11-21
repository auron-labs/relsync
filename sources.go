package main

import "github.com/aaronflorey/release-sync/source"

// learnSites tells the sources which hosts the Library has Items on, so an
// address on one of them is read as the kind of site it is without asking it.
func (s *screen) learnSites() {
	for _, e := range s.catalog.Entries().Get() {
		if !source.IsGitHub(e.Item.Source) {
			s.sources.Learn(e.Item.Source)
		}
	}
}
