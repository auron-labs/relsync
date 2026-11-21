// Package scan finds software that is on this PC already and works out which
// repository each program came from. Each place to look is a small reader that
// returns candidates; linking a candidate to a repository, and matching a name
// against search results, are pure functions over a candidate.
package scan

import (
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"
)

// Origin is where a candidate was found.
type Origin string

// The origins.
const (
	// OriginProgram is an entry of the system's list of installed programs.
	OriginProgram Origin = "program"
	// OriginFolder is a portable program found through a shortcut, or in a folder
	// the user added.
	OriginFolder Origin = "folder"
	// OriginBin is a tool in the configuration of bin.
	OriginBin Origin = "bin"
	// OriginScoop is an app with a manifest in Scoop's folders.
	OriginScoop Origin = "scoop"
	// OriginWinget is a package winget lists.
	OriginWinget Origin = "winget"
)

// LinkKind is how a candidate came to be linked to a repository.
type LinkKind int

// The kinds of Link.
const (
	// LinkNone is a candidate that is not linked.
	LinkNone LinkKind = iota
	// LinkProgram is a repository the program's own metadata, or a tool record,
	// points at. It is verified.
	LinkProgram
	// LinkUser is a repository the user gave. It is verified.
	LinkUser
	// LinkLikely is a repository found by name whose name and publisher agree.
	// It is guessed.
	LinkLikely
	// LinkPossible is a repository found by name alone. It is guessed.
	LinkPossible
)

// Verified reports a Link the program or the user vouches for.
func (k LinkKind) Verified() bool { return k == LinkProgram || k == LinkUser }

// Link is the repository a candidate is linked to, and how that came about.
type Link struct {
	Ref  source.RepoRef
	Kind LinkKind
}

// Candidate is a program found on the machine: its origin, the facts read from
// it and, once linked, the repository. Candidates are not kept between scans.
type Candidate struct {
	// Key identifies the candidate within a scan.
	Key    string
	Origin Origin
	Name   string
	// Publisher and Version are what the program says about itself; either may be empty.
	Publisher, Version string
	// Program is the entry of the installed programs, for OriginProgram.
	Program platform.Program
	// Folder is where a portable program, or a tool, is, and MainExe the program
	// in it relative to Folder, written with "/". Either may be empty.
	Folder, MainExe string
	// Texts are the places the program may name its repository in, most trusted first.
	Texts []string
	Link  Link
}

// Linked reports a candidate that has a repository.
func (c Candidate) Linked() bool { return c.Link.Kind != LinkNone }

// WithLink returns the candidate linked to ref.
func (c Candidate) WithLink(ref source.RepoRef, kind LinkKind) Candidate {
	c.Link = Link{Ref: ref, Kind: kind}
	return c
}

// Unlinked returns the candidate with no repository.
func (c Candidate) Unlinked() Candidate {
	c.Link = Link{}
	return c
}

// linkedByText links the candidate to the first repository its texts name.
func (c Candidate) linkedByText() Candidate {
	for _, text := range c.Texts {
		if ref, ok := RepoIn(text); ok {
			return c.WithLink(ref, LinkProgram)
		}
	}
	return c
}
