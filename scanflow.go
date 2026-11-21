package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auron-labs/mygo-framework/errorreport"
	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/scan"
	"github.com/aaronflorey/release-sync/source"

	"github.com/oneo-me/onevra/source/components"
)

const (
	scanJob        = "scan"
	scanWingetJob  = "scan-winget"
	scanFindAllJob = "scan-find-all"
	scanAddJob     = "scan-add"
	// requestsPerLookup is what looking up a linked repository costs the
	// request allowance: its metadata and its releases.
	requestsPerLookup = 2
)

func scanLookupJob(key string) string { return "scan-lookup:" + key }
func scanFindJob(key string) string   { return "scan-find:" + key }
func scanLinkJob(key string) string   { return "scan-link:" + key }

// scanBusy reports a job that reads or changes the scan's results as a whole,
// which another such job must wait for.
func (s *screen) scanBusy() bool {
	for _, name := range []string{scanJob, scanWingetJob, scanFindAllJob, scanAddJob} {
		if status, ok := s.jobs.Status(name); ok && status.State == jobs.Running {
			return true
		}
	}
	return false
}

// startReportingJob runs fn as the named job, ignoring a request while it is
// already running or after shutdown.
func (s *screen) startReportingJob(name string, fn jobs.Func) {
	err := s.jobs.Start(name, fn)
	if err != nil && !errors.Is(err, jobs.ErrRunning) && !errors.Is(err, jobs.ErrStopped) {
		log.Print(err)
	}
}

// editScan changes one row of the scan's results.
func (s *screen) editScan(key string, change func(*scanRow)) {
	s.scanPage.results.Update(func(r *scanResults) { *r = r.edited(key, change) })
}

// setScanResult sets the line under the Scan page's card.
func (s *screen) setScanResult(text string) {
	s.scanPage.results.Update(func(r *scanResults) { r.result = text })
}

// searchGap is the time between searches by name.
func (s *screen) searchGap() time.Duration {
	if s.searchPace != 0 {
		return s.searchPace
	}
	return scan.SearchGap(s.github.HasToken())
}

// searchPacer spaces the searches by name, whichever job makes them.
func (s *screen) searchPacer() *scan.Pacer {
	gap := s.searchGap()
	s.pacerMu.Lock()
	defer s.pacerMu.Unlock()
	if s.pacer == nil || s.pacerGap != gap {
		s.pacer, s.pacerGap = scan.NewPacer(gap), gap
	}
	return s.pacer
}

// onPC is what the Library already holds on this machine.
func onPC(entries []catalog.Entry) scan.Library {
	lib := scan.Library{Keys: map[string]bool{}}
	for _, e := range entries {
		in, ok := e.Installed()
		if !ok {
			continue
		}
		if in.UninstallKey != "" {
			lib.Keys[in.UninstallKey] = true
		}
		if in.Folder != "" {
			lib.Folders = append(lib.Folders, in.Folder)
			if in.MainExe != "" {
				lib.Programs = append(lib.Programs, filepath.Join(in.Folder, filepath.FromSlash(in.MainExe)))
			}
		}
	}
	return lib
}

// sourceErrorKind is the kind of failure a request had, when it was the site's.
func sourceErrorKind(err error) (source.ErrorKind, bool) {
	var se *source.Error
	if !errors.As(err, &se) {
		return 0, false
	}
	return se.Kind, true
}

func isRateLimited(err error) bool {
	kind, ok := sourceErrorKind(err)
	return ok && kind == source.KindRateLimited
}

func isNotFound(err error) bool {
	kind, ok := sourceErrorKind(err)
	return ok && kind == source.KindNotFound
}

// startScan reads the machine and looks up what it finds, in the background.
func (s *screen) startScan() {
	if s.scanBusy() {
		return
	}
	src := scan.Sources{
		Platform: s.platform, Locations: s.scanLocations, Folders: slices.Clone(s.prefs.Get().ScanFolders),
		Library: onPC(s.catalog.Entries().Get()),
	}
	s.startReportingJob(scanJob, func(ctx context.Context, report jobs.Report) error { return s.runScan(ctx, report, src) })
}

func (s *screen) cancelScan() {
	for _, name := range []string{scanJob, scanWingetJob, scanFindAllJob} {
		s.jobs.Cancel(name)
	}
}

// runScan is the scan job: it reads the machine, shows what it found, then
// looks up the releases of the programs that are linked to a repository.
func (s *screen) runScan(ctx context.Context, report jobs.Report, src scan.Sources) error {
	s.scanPage.results.Set(scanResults{})
	report(0, 0, locales.ScanReading())
	res, err := scan.Read(ctx, src, func(done, total int) { report(done, total, locales.ScanLinking(done, total)) })
	switch {
	case ctx.Err() != nil:
		s.setScanResult(locales.ScanCanceled())
		return fmt.Errorf("scan this PC: %w", context.Cause(ctx))
	case errors.Is(err, platform.ErrNotSupported):
		s.setScanResult(locales.ScanUnsupported())
		return nil
	case err != nil:
		s.setScanResult(locales.ScanFailed(err.Error()))
		s.record(ctx, activity.Error, 0, locales.ActivityScanFailed(err.Error()))
		return fmt.Errorf("scan this PC: %w", err)
	}
	s.reportSkipped(res.Skipped)

	pre := s.prefs.Get().DefaultIncludePrereleases
	rows := make([]scanRow, len(res.Candidates))
	var lookups []string
	for i, c := range res.Candidates {
		rows[i] = scanRow{Candidate: c}
		if c.Linked() {
			_, rows[i].tracked = s.catalog.Find(c.Link.Ref)
			if !rows[i].tracked {
				rows[i].releases = releasesPending
				lookups = append(lookups, c.Key)
			}
		}
	}
	s.scanPage.results.Set(scanResults{rows: rows, scanned: true})
	limited := s.lookupAll(ctx, report, lookups, pre)
	return s.finishScan(ctx, limited)
}

// reportSkipped logs the places a scan could not read, and reports those that
// are not simply missing from this system.
func (s *screen) reportSkipped(skipped []error) {
	var failures []error
	for _, err := range skipped {
		log.Printf("scan this PC: %v", err)
		if !errors.Is(err, platform.ErrNotSupported) {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		errorreport.Capture(errors.Join(failures...), "scan.read", "jobs")
	}
}

// settlePending marks the rows whose releases were never read as not checked,
// as a scan that was canceled leaves them.
func (s *screen) settlePending() {
	s.scanPage.results.Update(func(r *scanResults) {
		if !slices.ContainsFunc(r.rows, func(row scanRow) bool { return row.releases == releasesPending }) {
			return
		}
		rows := slices.Clone(r.rows)
		for i := range rows {
			if rows[i].releases == releasesPending {
				rows[i].releases = releasesUnchecked
			}
		}
		r.rows = rows
	})
}

// finishScan says how the scan ended, in the result line and in Activity.
func (s *screen) finishScan(ctx context.Context, limited bool) error {
	s.settlePending()
	if ctx.Err() != nil {
		s.setScanResult(locales.ScanCanceled())
		return fmt.Errorf("scan this PC: %w", context.Cause(ctx))
	}
	results := s.scanPage.results.Get()
	linked := len(results.linked())
	others := len(results.rows) - linked
	text := locales.ScanDone(linked, others)
	if limited {
		text = locales.ScanLimited()
	}
	s.setScanResult(text)
	level := activity.Info
	if limited {
		level = activity.Warning
	}
	s.record(ctx, level, 0, locales.ActivityScanned(linked, others))
	return nil
}

// lookupAll reads the releases of the linked repositories of the rows, a few at
// a time, in the check lane. It stops looking when the request allowance runs
// out, and reports whether it did.
func (s *screen) lookupAll(ctx context.Context, report jobs.Report, keys []string, includePrereleases bool) (limited bool) {
	total := len(keys)
	if total == 0 {
		return false
	}
	var (
		wg      sync.WaitGroup
		stopped atomic.Bool
		done    atomic.Int64
		started []string
	)
	report(0, total, locales.ScanChecking(0, total))
	for _, key := range keys {
		wg.Add(1)
		name := scanLookupJob(key)
		err := s.lanes.Start(laneCheck, name, func(jobCtx context.Context, _ jobs.Report) error {
			defer wg.Done()
			// The job stops with the scan that started it.
			jobCtx, cancel := context.WithCancel(jobCtx)
			defer cancel()
			defer context.AfterFunc(ctx, cancel)()
			if jobCtx.Err() == nil {
				if stopped.Load() {
					s.editScan(key, func(r *scanRow) { r.releases = releasesUnchecked })
				} else if s.lookupRow(jobCtx, key, includePrereleases) {
					stopped.Store(true)
				}
			}
			finished := int(done.Add(1))
			report(finished, total, locales.ScanChecking(finished, total))
			return nil
		})
		if err != nil {
			wg.Done()
			if !errors.Is(err, jobs.ErrStopped) {
				log.Print(err)
			}
			break
		}
		started = append(started, name)
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		// A job still waiting in its lane is stopped for good, and would never
		// say it is done.
		for _, name := range started {
			s.jobs.Cancel(name)
		}
	}
	return stopped.Load()
}

// lookupRow reads the releases of the row's repository and shows what they say.
// It reports that the request allowance ran out. A repository that cannot be
// found sends the row to the programs with no repository.
func (s *screen) lookupRow(ctx context.Context, key string, includePrereleases bool) (limited bool) {
	row, ok := s.scanPage.results.Get().row(key)
	if !ok || !row.Linked() {
		return false
	}
	if a := s.catalog.Allowance(); a.Known && a.Remaining < requestsPerLookup {
		s.editScan(key, func(r *scanRow) { r.releases = releasesUnchecked })
		return true
	}
	preview, err := s.catalog.Preview(ctx, row.Link.Ref)
	switch {
	case ctx.Err() != nil:
	case err == nil:
		_, tracked := s.catalog.Find(preview.Repository.Ref())
		s.editScan(key, func(r *scanRow) {
			*r = r.withPreview(preview, includePrereleases)
			r.tracked = tracked
			r.selected = r.Link.Kind.Verified() && !tracked
		})
	case isNotFound(err):
		s.editScan(key, func(r *scanRow) {
			r.Candidate, r.releases, r.selected, r.note = r.Unlinked(), releasesUnknown, false, locales.ScanNoteDead()
		})
	case isRateLimited(err):
		s.editScan(key, func(r *scanRow) { r.releases = releasesUnchecked })
		return true
	default:
		log.Printf("look up %s for %s: %v", row.Link.Ref, row.Name, err)
		s.editScan(key, func(r *scanRow) { r.releases = releasesFailed })
	}
	return false
}

// startFind searches GitHub by the name of one program.
func (s *screen) startFind(key string) {
	s.startReportingJob(scanFindJob(key), func(ctx context.Context, _ jobs.Report) error {
		if _, limited := s.findRow(ctx, key); limited {
			s.toast(components.ToastWarning, locales.ScanNoteLimit())
		}
		return nil
	})
}

// findRow searches GitHub by the row's name and links the best match, as a
// guess. It reports whether a repository was linked, and whether the search
// allowance is spent.
func (s *screen) findRow(ctx context.Context, key string) (found, limited bool) {
	row, ok := s.scanPage.results.Get().row(key)
	if !ok || row.Linked() || row.busy {
		return false, false
	}
	s.editScan(key, func(r *scanRow) { r.busy, r.note = true, "" })
	defer s.editScan(key, func(r *scanRow) { r.busy = false })
	if err := s.searchPacer().Wait(ctx); err != nil {
		return false, false
	}
	match, err := scan.Find(ctx, s.searcher, row.Candidate)
	var note string
	switch {
	case ctx.Err() != nil:
		return false, false
	case errors.Is(err, scan.ErrNameTooShort):
		note = locales.ScanNoteShort()
	case errors.Is(err, scan.ErrNoMatch):
		note = locales.ScanNoteNoMatch()
	case isRateLimited(err):
		note, limited = locales.ScanNoteLimit(), true
	case err != nil:
		log.Printf("search for %s: %v", row.Name, err)
		note = errorText(err)
	default:
		s.linkMatch(ctx, key, match)
		return true, false
	}
	s.editScan(key, func(r *scanRow) { r.note = note })
	return false, limited
}

// linkMatch links the row to the repository a search found, and reads its
// releases, which costs one request: the search already said the rest.
func (s *screen) linkMatch(ctx context.Context, key string, match scan.Match) {
	pre := s.prefs.Get().DefaultIncludePrereleases
	preview, err := s.catalog.PreviewRepository(ctx, match.Repo)
	_, tracked := s.catalog.Find(match.Repo.Ref())
	s.editScan(key, func(r *scanRow) {
		r.Candidate = r.WithLink(match.Repo.Ref(), match.Kind())
		r.tracked = tracked
		switch {
		case err == nil:
			*r = r.withPreview(preview, pre)
			r.selected = match.Kind() == scan.LinkLikely && !tracked && r.latest != ""
		case isRateLimited(err):
			r.releases = releasesUnchecked
		default:
			log.Printf("read the releases of %s: %v", match.Repo.Ref(), err)
			r.releases = releasesFailed
		}
	})
}

// startFindAll searches GitHub by name for every program in the list of
// programs with no repository, at the pace the search allowance allows.
func (s *screen) startFindAll() {
	if s.scanBusy() {
		return
	}
	results := s.scanPage.results.Get()
	var keys []string
	for _, row := range results.others(!s.scanPage.showSystem) {
		keys = append(keys, row.Key)
	}
	if len(keys) == 0 {
		return
	}
	s.startReportingJob(scanFindAllJob, func(ctx context.Context, report jobs.Report) error {
		looked, found := 0, 0
		for i, key := range keys {
			if ctx.Err() != nil {
				break
			}
			row, ok := s.scanPage.results.Get().row(key)
			if !ok {
				continue
			}
			report(i, len(keys), locales.ScanFindAllProgress(row.Name, i+1, len(keys)))
			hit, limited := s.findRow(ctx, key)
			looked++
			if hit {
				found++
			}
			if limited {
				break
			}
		}
		if looked == len(keys) {
			s.setScanResult(locales.ScanFindAllDone(looked, found))
		} else {
			s.setScanResult(locales.ScanFindAllStopped(looked, found))
		}
		return nil
	})
}

// startLink links a program to the repository the user named.
func (s *screen) startLink(key string, ref source.RepoRef) {
	pre := s.prefs.Get().DefaultIncludePrereleases
	s.startReportingJob(scanLinkJob(key), func(ctx context.Context, _ jobs.Report) error {
		row, ok := s.scanPage.results.Get().row(key)
		if !ok {
			return nil
		}
		s.editScan(key, func(r *scanRow) { r.busy, r.note = true, "" })
		defer s.editScan(key, func(r *scanRow) { r.busy = false })
		preview, err := s.catalog.Preview(ctx, ref)
		if err != nil {
			if ctx.Err() == nil {
				text := errorText(err)
				s.editScan(key, func(r *scanRow) { r.note = text })
				s.toast(components.ToastError, text)
			}
			return nil
		}
		_, tracked := s.catalog.Find(preview.Repository.Ref())
		s.editScan(key, func(r *scanRow) {
			r.Candidate = r.WithLink(preview.Repository.Ref(), scan.LinkUser)
			*r = r.withPreview(preview, pre)
			r.tracked, r.selected = tracked, !tracked && r.latest != ""
		})
		s.toast(components.ToastSuccess, locales.ScanToastLinked(row.Name, preview.Repository.Ref().String()))
		return nil
	})
}

// startWinget reads the packages winget lists, and adds those whose details name
// a repository to the programs linked to GitHub.
func (s *screen) startWinget() {
	if s.scanBusy() {
		return
	}
	pre := s.prefs.Get().DefaultIncludePrereleases
	s.startReportingJob(scanWingetJob, func(ctx context.Context, report jobs.Report) error {
		report(0, 0, locales.ScanWingetReading())
		found, err := scan.FromWinget(ctx, s.platform, func(done, total int) { report(done, total, locales.ScanWingetChecking(done, total)) })
		switch {
		case ctx.Err() != nil:
			s.setScanResult(locales.ScanCanceled())
			return fmt.Errorf("read winget packages: %w", context.Cause(ctx))
		case errors.Is(err, platform.ErrNotSupported):
			s.setScanResult(locales.ScanWingetMissing())
			return nil
		case err != nil && found == nil:
			s.setScanResult(locales.ScanFailed(err.Error()))
			return fmt.Errorf("read winget packages: %w", err)
		case err != nil:
			// Some packages could not be read; the rest were.
			log.Printf("read winget packages: %v", err)
			errorreport.Capture(err, "scan.winget", "jobs")
		}
		added := s.mergeWinget(found)
		limited := s.lookupAll(ctx, report, added, pre)
		s.settlePending()
		if ctx.Err() != nil {
			s.setScanResult(locales.ScanCanceled())
			return fmt.Errorf("read winget packages: %w", context.Cause(ctx))
		}
		if limited {
			s.setScanResult(locales.ScanLimited())
			return nil
		}
		s.setScanResult(locales.ScanWingetDone(len(added)))
		s.record(ctx, activity.Info, 0, locales.ScanWingetDone(len(added)))
		return nil
	})
}

// mergeWinget adds the packages to the scan's rows and returns the keys of the
// rows that now need their releases read. A package of a repository the
// Library or another row has is not added again, and one that has the name of a
// program with no repository is that program's repository.
func (s *screen) mergeWinget(found []scan.Candidate) []string {
	var added []string
	s.scanPage.results.Update(func(r *scanResults) {
		rows := slices.Clone(r.rows)
		for _, c := range found {
			ref := c.Link.Ref
			if _, tracked := s.catalog.Find(ref); tracked || slices.ContainsFunc(rows, func(row scanRow) bool {
				return row.Linked() && strings.EqualFold(row.Link.Ref.String(), ref.String())
			}) {
				continue
			}
			if i := slices.IndexFunc(rows, func(row scanRow) bool { return !row.Linked() && strings.EqualFold(row.Name, c.Name) }); i >= 0 {
				rows[i].Candidate = rows[i].WithLink(ref, scan.LinkProgram)
				rows[i].releases, rows[i].note = releasesPending, ""
				added = append(added, rows[i].Key)
				continue
			}
			rows = append(rows, scanRow{Candidate: c, releases: releasesPending})
			added = append(added, c.Key)
		}
		slices.SortStableFunc(rows, func(a, b scanRow) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
		r.rows, r.scanned = rows, true
	})
	return added
}

// setScanSelected ticks or unticks a row.
func (s *screen) setScanSelected(key string, on bool) {
	s.editScan(key, func(r *scanRow) { r.selected = on })
}

// selectAllScanned ticks or unticks every row that can be.
func (s *screen) selectAllScanned(on bool) {
	s.scanPage.results.Update(func(r *scanResults) {
		rows := slices.Clone(r.rows)
		for i := range rows {
			if rows[i].canSelect() {
				rows[i].selected = on
			}
		}
		r.rows = rows
	})
}

// adoptionRecord is what the Library records of the program on the machine, nil
// when there is only a version to say: the application did not put the program
// there, and a tool another manager keeps is not its to update in place.
func adoptionRecord(r scanRow, repo string) *install.Record {
	switch r.Origin {
	case scan.OriginProgram:
		rec := install.AdoptEntry(r.Program, repo)
		return &rec
	case scan.OriginFolder:
		rec := install.AdoptPortable(r.Folder, r.MainExe)
		return &rec
	case scan.OriginBin, scan.OriginScoop, scan.OriginWinget:
	}
	return nil
}

// addScanned adds the ticked programs to the Library at the versions they have.
// Nothing is installed again.
func (s *screen) addScanned() {
	var picks []scanRow
	for _, row := range s.scanPage.results.Get().rows {
		if row.ticked() {
			picks = append(picks, row)
		}
	}
	if len(picks) == 0 || s.scanBusy() {
		return
	}
	pre := s.prefs.Get().DefaultIncludePrereleases
	s.startReportingJob(scanAddJob, func(ctx context.Context, report jobs.Report) error {
		var added, updates int
		limited := false
		for i, row := range picks {
			if ctx.Err() != nil || limited {
				break
			}
			report(i, len(picks), row.Name)
			entry, err := s.adoptRow(ctx, row, pre)
			switch {
			case errors.Is(err, catalog.ErrExists):
				s.editScan(row.Key, func(r *scanRow) { r.tracked, r.selected = true, false })
			case ctx.Err() != nil:
			case isRateLimited(err):
				limited = true
			case err != nil:
				log.Printf("add %s from the scan: %v", row.Name, err)
				s.record(ctx, activity.Warning, 0, locales.ActivityScanAddFailed(row.Name, errorText(err)))
			default:
				s.editScan(row.Key, func(r *scanRow) { r.tracked, r.selected = true, false })
				s.record(ctx, activity.Success, entry.Item.ID, locales.ActivityScanAdded(entry.FullName()))
				added++
				if entry.HasUpdate() {
					updates++
				}
			}
		}
		s.finishAdding(added, updates, limited)
		return nil //nolint:nilerr // A program that could not be added is recorded in Activity; the job has nothing more to report.
	})
}

// adoptRow tracks the row's repository in the Library as software the user
// already has.
func (s *screen) adoptRow(ctx context.Context, row scanRow, includePrereleases bool) (catalog.Entry, error) {
	preview := row.preview
	if preview == nil {
		p, err := s.catalog.Preview(ctx, row.Link.Ref)
		if err != nil {
			return catalog.Entry{}, err //nolint:wrapcheck // The catalog says what it was previewing.
		}
		preview = &p
	}
	opts := catalog.AddOptions{IncludePrereleases: includePrereleases, AlreadyHave: true, LinkGuessed: !row.Link.Kind.Verified()}
	installed := preview.Draft(opts).TagForVersion(row.Version)
	if installed == "" {
		installed = strings.TrimSpace(row.Version)
	}
	opts.InstalledVersion = installed
	opts.Install = adoptionRecord(row, preview.Repository.Name)
	return s.catalog.Add(ctx, *preview, opts) //nolint:wrapcheck // The catalog says what it was adding.
}

// finishAdding tells the person how adding went, and shows the updates the
// added programs have.
func (s *screen) finishAdding(added, updates int, limited bool) {
	switch {
	case limited:
		s.toast(components.ToastWarning, locales.ScanToastAddedLimited(added))
	case updates > 0:
		s.toast(components.ToastSuccess, locales.ScanToastAddedUpdates(added, updates))
	case added > 0:
		s.toast(components.ToastSuccess, locales.ScanToastAdded(added))
	}
	if updates > 0 {
		s.onUI(func() { s.navigate("updates") })
	}
}
