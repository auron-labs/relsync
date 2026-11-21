package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/exchange"
	"github.com/aaronflorey/release-sync/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

const (
	importJob = "import"
	// maxImportBytes bounds the file an import reads.
	maxImportBytes = 8 << 20
	// metadataLimit is the largest import that fetches each repository's
	// metadata without a token, so a long list does not spend the anonymous
	// request allowance on descriptions.
	metadataLimit = 25
)

// exportName is the default file name of an export of the given kind.
func exportName(extension string) string { return appSlug + "-library." + extension }

// exportLibraryFile saves the Library as a JSON document.
func (s *screen) exportLibraryFile() {
	data, err := exchange.Export(s.catalog.Entries().Get(), s.catalog.Groups().Get(), time.Now())
	if err != nil {
		log.Print(err)
		s.queueToast(ui.Toast{Title: locales.ExportFailed(err.Error()), Type: components.ToastError})
		return
	}
	s.saveExport(data, mygo.SaveDialogOptions{
		Title: locales.ExportSaveTitle(), DefaultPath: exportName("json"),
		Filters: []mygo.FileFilter{{Name: locales.ExportJsonFiles(), Extensions: []string{"json"}}},
	})
}

// exportLinkFile saves the address of every repository, one to a line.
func (s *screen) exportLinkFile() {
	s.saveExport([]byte(exchange.LinkList(s.catalog.Entries().Get())), mygo.SaveDialogOptions{
		Title: locales.ExportSaveTitle(), DefaultPath: exportName("txt"),
		Filters: []mygo.FileFilter{{Name: locales.ExportTextFiles(), Extensions: []string{"txt"}}},
	})
}

// copyLinks puts every repository's address on the clipboard.
func (s *screen) copyLinks() {
	entries := s.catalog.Entries().Get()
	s.copyText(exchange.LinkList(entries))
	s.queueToast(ui.Toast{Title: locales.ExportCopied(len(entries)), Type: components.ToastSuccess})
}

// saveExport asks where to save data and writes it. The dialog blocks until it
// closes, so it runs off the UI thread.
func (s *screen) saveExport(data []byte, opts mygo.SaveDialogOptions) {
	opts.Parent = s.win
	go func() {
		path, err := s.savePath(opts)
		if err != nil {
			log.Printf("choose the export file: %v", err)
			s.toast(components.ToastError, locales.ExportFailed(err.Error()))
			return
		}
		if path == "" {
			return
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			log.Printf("write the export: %v", err)
			s.toast(components.ToastError, locales.ExportFailed(err.Error()))
			return
		}
		s.toast(components.ToastSuccess, locales.ExportSaved(filepath.Base(path)))
	}()
}

// browseImportFile asks which file to import.
func (s *screen) browseImportFile() {
	s.browse(mygo.OpenDialogOptions{
		Title:   locales.ImportBrowse(),
		Filters: []mygo.FileFilter{{Name: locales.ImportFileFilter(), Extensions: []string{"json", "txt", "csv", "md"}}},
	}, s.loadImportFile)
}

// loadImportFile reads the file to import in the background.
func (s *screen) loadImportFile(path string) {
	p := &s.importPage
	p.reading, p.problem = true, ""
	s.background(func(context.Context) error {
		text, err := readImportFile(path)
		s.onUI(func() {
			p.reading = false
			if err != nil {
				log.Printf("read %s: %v", path, err)
				p.problem = importFileProblem(err)
				return
			}
			p.fileName, p.fileText = filepath.Base(path), text
		})
		return nil
	})
}

var errNotText = errors.New("not a text file")

// readImportFile reads a list to import, refusing a file that is too large or
// is not text.
func readImportFile(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("open the file to import: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxImportBytes+1))
	switch {
	case err != nil:
		return "", fmt.Errorf("read the file to import: %w", err)
	case len(data) > maxImportBytes:
		return "", fmt.Errorf("read the file to import: %w", errImportTooLarge)
	case bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data):
		return "", fmt.Errorf("read the file to import: %w", errNotText)
	}
	return string(data), nil
}

var errImportTooLarge = errors.New("file too large")

func importFileProblem(err error) string {
	switch {
	case errors.Is(err, errImportTooLarge):
		return locales.ImportTooLarge()
	case errors.Is(err, errNotText):
		return locales.ImportNotText()
	}
	return locales.ImportReadFailed(err.Error())
}

// startImport imports what the page holds, as a job.
func (s *screen) startImport() {
	p := &s.importPage
	text := p.source()
	if strings.TrimSpace(text) == "" {
		return
	}
	options := exchange.Options{
		GroupID: parseGroupValue(p.group), AlreadyInstalled: p.alreadyInstalled,
		Prereleases: s.prefs.Get().DefaultIncludePrereleases,
	}
	if !slices.ContainsFunc(s.catalog.Groups().Get(), func(g catalog.Group) bool { return g.ID == options.GroupID }) {
		options.GroupID = 0
	}
	installAfter := p.installAfter && !p.alreadyInstalled
	p.result, p.problem = "", ""
	s.startReportingJob(importJob, func(ctx context.Context, report jobs.Report) error {
		return s.runImport(ctx, report, text, options, installAfter)
	})
}

// runImport is the import job: it reads the repositories in text and adds them
// one at a time.
func (s *screen) runImport(ctx context.Context, report jobs.Report, text string, options exchange.Options, installAfter bool) error {
	parsed, err := exchange.Parse(text, s.sources)
	switch {
	case errors.Is(err, exchange.ErrNewerFormat):
		s.setImportOutcome("", locales.ImportNewer(appName))
		return nil
	case len(parsed.Entries) == 0:
		s.setImportOutcome("", locales.ImportNone())
		return nil
	}
	options.Metadata = len(parsed.Entries) <= metadataLimit || s.github.HasToken()
	result := exchange.Import(ctx, s.catalog, parsed.Entries, options, func(p exchange.Progress) {
		report(p.Done+1, p.Total, locales.ImportProgress(p.Done+1, p.Total, p.Ref.String()))
	})
	for _, f := range result.Failed {
		log.Printf("import %s: %v", f.Ref, f.Err)
	}
	line := importSummary(result, parsed.Skipped)
	s.setImportOutcome(line, "")
	// The outcome is recorded even when the import was canceled.
	bg := context.WithoutCancel(ctx)
	level := activity.Success
	if len(result.Failed) > 0 || result.Stopped != nil {
		level = activity.Info
	}
	s.record(bg, level, 0, locales.ActivityImported(line))
	s.toast(components.ToastSuccess, line)
	if installAfter {
		s.installImported(result.Added)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("import: %w", ctx.Err())
	}
	return nil
}

// installImported starts installing each imported Item that has a file to install.
func (s *screen) installImported(added []catalog.Entry) {
	for _, e := range added {
		if offer, ok := e.LatestInstallable(); ok {
			s.startInstall(e.Item.ID, offer.Release.Tag, nil)
		}
	}
}

func (s *screen) setImportOutcome(result, problem string) {
	s.onUI(func() { s.importPage.result, s.importPage.problem = result, problem })
}

// importSummary is the line that says what an import did.
func importSummary(r exchange.Result, skipped int) string {
	parts := []string{locales.ImportDone(len(r.Added))}
	if r.AlreadyTracked > 0 {
		parts = append(parts, locales.ImportAlreadyTracked(r.AlreadyTracked))
	}
	if len(r.Failed) > 0 {
		parts = append(parts, locales.ImportFailed(len(r.Failed)))
	}
	if skipped > 0 {
		parts = append(parts, locales.ImportSkipped(skipped))
	}
	if r.Stopped != nil {
		reason := locales.ImportCanceled()
		if !errors.Is(r.Stopped, context.Canceled) {
			reason = errorText(r.Stopped)
		}
		parts = append(parts, locales.ImportStopped(reason))
	}
	return strings.Join(parts, " · ")
}
