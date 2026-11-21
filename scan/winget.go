package scan

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"
)

// wingetAtOnce is how many packages' details are read at the same time.
const wingetAtOnce = 3

// WingetPackage is a row of winget's list of installed packages.
type WingetPackage struct {
	Name, ID, Version string
}

// ParseWingetList reads the table `winget list` prints. The table's columns
// are as wide as the header says, so each row is cut at the header's columns.
// Output without the table has no packages.
func ParseWingetList(output string) []WingetPackage {
	// A lone carriage return ends a frame of the progress spinner winget draws first.
	lines := strings.Split(lineEnds.Replace(output), "\n")
	header := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "Name") && strings.Contains(l, " Id") && strings.Contains(l, "Version") {
			header = i
			break
		}
	}
	if header < 0 || header+2 > len(lines) {
		return nil
	}
	// Columns are counted in characters: names in other scripts are one wide.
	column := func(name string) int {
		i := strings.Index(lines[header], name)
		if i < 0 {
			return -1
		}
		return utf8.RuneCountInString(lines[header][:i])
	}
	idAt, versionAt := column(" Id")+1, column("Version")
	// The version ends where the column of the newest version, or the source, begins.
	versionEnd := math.MaxInt
	if at := column("Available"); at > 0 {
		versionEnd = at
	} else if at := column("Source"); at > 0 {
		versionEnd = at
	}
	var found []WingetPackage
	// The line below the header is its rule.
	for _, l := range lines[header+2:] {
		row := []rune(l)
		if len(row) <= versionAt {
			continue
		}
		cell := func(from, to int) string { return strings.TrimSpace(string(row[from:min(to, len(row))])) }
		name, id, version := cell(0, idAt), cell(idAt, versionAt), cell(versionAt, versionEnd)
		if fields := strings.Fields(version); len(fields) > 0 {
			version = fields[0]
		}
		if id != "" && strings.Contains(id, ".") && name != "" {
			found = append(found, WingetPackage{Name: name, ID: id, Version: version})
		}
	}
	return found
}

var lineEnds = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// wingetLinkFields are the details of a package that may name its project, most
// trusted first.
var wingetLinkFields = []string{"Homepage", "Publisher Url", "Package Url", "Publisher Support Url", "Release Notes Url"}

// WingetRepo finds the repository a package's details name, as `winget show`
// prints them, in its homepage or publisher links.
func WingetRepo(details string) (source.RepoRef, bool) {
	lines := strings.Split(lineEnds.Replace(details), "\n")
	for _, field := range wingetLinkFields {
		for _, l := range lines {
			if after, ok := strings.CutPrefix(strings.TrimSpace(l), field+":"); ok {
				if ref, found := RepoIn(after); found {
					return ref, true
				}
			}
		}
	}
	return source.RepoRef{}, false
}

// FromWinget lists the packages winget has installed and links each to the
// repository its details name. Packages whose details name none are left out.
// A package whose details cannot be read is skipped; the first such failure is
// returned beside what was found. progress hears how many packages are done.
func FromWinget(ctx context.Context, p platform.Platform, progress func(done, total int)) ([]Candidate, error) {
	list, err := p.Winget(ctx, "list", "--source", "winget", "--accept-source-agreements", "--disable-interactivity")
	if err != nil {
		return nil, fmt.Errorf("list winget packages: %w", err)
	}
	packages := ParseWingetList(list)
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		found    []Candidate
		skipped  error
		finished int
		slots    = make(chan struct{}, wingetAtOnce)
	)
	progress(0, len(packages))
	for _, pkg := range packages {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			details, err := p.Winget(ctx, "show", "--id", pkg.ID, "--exact", "--source", "winget", "--accept-source-agreements", "--disable-interactivity")
			mu.Lock()
			defer mu.Unlock()
			finished++
			progress(finished, len(packages))
			if err != nil {
				if skipped == nil && ctx.Err() == nil {
					skipped = fmt.Errorf("read winget package %s: %w", pkg.ID, err)
				}
				return
			}
			if ref, ok := WingetRepo(details); ok {
				found = append(found, Candidate{
					Key: "winget:" + strings.ToLower(pkg.ID), Origin: OriginWinget, Name: pkg.Name, Version: pkg.Version,
				}.WithLink(ref, LinkProgram))
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read winget packages: %w", err)
	}
	sortByName(found)
	return found, skipped
}
