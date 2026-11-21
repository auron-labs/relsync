package scan

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
)

// Library is what the Library already holds on this machine, so a scan does
// not offer it again.
type Library struct {
	// Keys are the installed-programs keys of the installs bound to an Item.
	Keys map[string]bool
	// Folders and Programs are the folders and program files of the Library's
	// installs.
	Folders, Programs []string
}

// Sources says where a scan looks.
type Sources struct {
	Platform  platform.Platform
	Locations Locations
	// Folders are the folders the user asked to have looked in.
	Folders []string
	Library Library
}

// Result is what a scan found.
type Result struct {
	// Candidates are in name order. Those the programs themselves, or the tool
	// records, link to a repository carry the Link.
	Candidates []Candidate
	// Skipped are the places that could not be read, each with why. The scan
	// went on without them.
	Skipped []error
}

// Read reads every place a program may be found: the installed programs,
// shortcuts, the folders the user added, and the records of bin and Scoop, then
// links what it can by what each program says about itself. It fails only when
// the installed programs cannot be listed. progress hears how many of the
// programs whose own files are read for a link are done.
func Read(ctx context.Context, src Sources, progress func(done, total int)) (Result, error) {
	programs, err := src.Platform.Programs(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list the installed programs: %w", err)
	}
	var res Result
	skip := func(err error) {
		if err != nil {
			res.Skipped = append(res.Skipped, err)
		}
	}
	installed := FromPrograms(programs, src.Library.Keys)
	covered := Covered{Folders: slices.Clone(src.Library.Folders), Programs: src.Library.Programs}
	covered.CoverPrograms(programs)
	if folders, err := src.Platform.Folders(); err != nil {
		skip(fmt.Errorf("locate the system folders: %w", err))
	} else {
		covered.Folders = append(covered.Folders, folders.System...)
	}
	shortcuts, err := src.Platform.ListShortcuts(ctx)
	skip(err)
	portable := append(FromShortcuts(src.Platform, shortcuts, covered), FromFolders(src.Platform, src.Folders, covered)...)

	tools, err := ReadBin(src.Locations.BinConfig)
	skip(err)
	var scoop []Candidate
	for _, root := range src.Locations.ScoopRoots {
		apps, err := ReadScoop(root)
		skip(err)
		scoop = append(scoop, apps...)
	}

	if err := linkByFiles(ctx, src.Platform, installed, progress); err != nil {
		return Result{}, err
	}
	for i := range installed {
		installed[i] = installed[i].linkedByText()
	}
	res.Candidates = oneForEachRepo(append(append(append(installed, portable...), scoop...), tools...))
	sortByName(res.Candidates)
	return res, nil
}

// linkByFiles adds the version information of each program's own file to the
// texts of those candidates that name no repository in the entry itself. The
// program is only read, never run. A file that cannot be read adds nothing.
func linkByFiles(ctx context.Context, p platform.Platform, candidates []Candidate, progress func(done, total int)) error {
	var todo []int
	for i, c := range candidates {
		if !c.linkedByText().Linked() {
			todo = append(todo, i)
		}
	}
	progress(0, len(todo))
	for done, i := range todo {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("read the programs: %w", err)
		}
		candidates[i].Texts = append(candidates[i].Texts, fileTexts(p, candidates[i].Program)...)
		progress(done+1, len(todo))
	}
	return nil
}

// fileTexts are the strings of the version information of an installed
// program's main file, or none when it has no file or cannot be read.
func fileTexts(p platform.Platform, program platform.Program) []string {
	folder, rel := install.MainProgram(program, "")
	if rel == "" {
		return nil
	}
	info, err := p.VersionInfo(filepath.Join(folder, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	return []string{info.Comments, info.LegalCopyright, info.LegalTrademarks, info.ProductName, info.CompanyName}
}

// oneForEachRepo keeps the first candidate linked to a repository as it is,
// and leaves the others unlinked: two programs found for one repository are one
// piece of software, and only one can be tracked.
func oneForEachRepo(candidates []Candidate) []Candidate {
	taken := map[string]bool{}
	seen := map[string]bool{}
	out := candidates[:0:0]
	for _, c := range candidates {
		if seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		if c.Linked() {
			key := strings.ToLower(c.Link.Ref.String())
			if taken[key] {
				c = c.Unlinked()
			}
			taken[key] = true
		}
		out = append(out, c)
	}
	return out
}
