package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/auron-labs/mygo-framework/jobs"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/source"

	"github.com/oneo-me/onevra/source/components"
)

// transferState is where a download is, as the person watching sees it.
type transferState int

const (
	// transferRunning is waiting in the queue or downloading; the lane says which.
	transferRunning transferState = iota
	transferVerifying
	// transferDone is a file that downloaded and passed its checks.
	transferDone
	// transferNeedsCheck is a file that downloaded and is kept, with findings to read.
	transferNeedsCheck
	transferFailed
	transferCanceled
)

// transfer is one file being downloaded, or the outcome of a download the
// person has not yet dismissed. It is shared with the goroutines that run
// downloads, so it lives in a state cell.
type transfer struct {
	// key is the job's name.
	key    string
	itemID int64
	tag    string
	file   string
	state  transferState

	done, total int64
	speed       float64

	// summary is the verification summary of a finished download.
	summary  string
	findings []fetch.Finding
	// verified is the result the findings are explained from.
	verified fetch.Result
	// problem is why a download failed or was rejected.
	problem string
	// path is where a finished download is.
	path string
}

func (t transfer) active() bool { return t.state == transferRunning || t.state == transferVerifying }

// downloadJob names the job that downloads one file of one release of an Item.
func downloadJob(itemID int64, tag, file string) string {
	return "download:" + strconv.FormatInt(itemID, 10) + ":" + tag + ":" + file
}

// putTransfer adds a transfer or replaces the one with the same key.
func (s *screen) putTransfer(t transfer) {
	s.transfers.Update(func(all *[]transfer) {
		next := slices.Clone(*all)
		if i := slices.IndexFunc(next, func(x transfer) bool { return x.key == t.key }); i >= 0 {
			next[i] = t
		} else {
			next = append(next, t)
		}
		*all = next
	})
}

// changeTransfer changes the transfer with key, if it is still listed.
func (s *screen) changeTransfer(key string, change func(*transfer)) {
	s.transfers.Update(func(all *[]transfer) {
		i := slices.IndexFunc(*all, func(x transfer) bool { return x.key == key })
		if i < 0 {
			return
		}
		next := slices.Clone(*all)
		change(&next[i])
		*all = next
	})
}

func (s *screen) dropTransfer(key string) {
	s.transfers.Update(func(all *[]transfer) {
		*all = slices.DeleteFunc(slices.Clone(*all), func(x transfer) bool { return x.key == key })
	})
}

// transfersOf lists the downloads of an Item, oldest first.
func (s *screen) transfersOf(itemID int64) []transfer {
	var out []transfer
	for _, t := range s.transfers.Get() {
		if t.itemID == itemID {
			out = append(out, t)
		}
	}
	return out
}

// download is everything a download job needs, fixed when the job starts so a
// settings change midway does not change it.
type download struct {
	key     string
	entry   catalog.Entry
	release source.Release
	asset   source.Asset
	root    string
	checks  fetch.Checks
}

// verifyChecks is which checks the settings turn on for a download.
func verifyChecks(p preferences) fetch.Checks {
	return fetch.Checks{
		Checksum: p.VerifyDownloads, Signature: p.VerifyDownloads,
		Scan: p.ScanDownloads, Provenance: p.CheckProvenance, VirusTotal: p.UseVirusTotal,
	}
}

// downloadRoot is the folder downloads go in.
func (s *screen) downloadRoot(p preferences) string {
	if p.DownloadFolder != "" {
		return p.DownloadFolder
	}
	return s.dirs.downloads
}

// downloadFile downloads one file of a release, verifies it, and shows it in
// the file manager. It queues behind other downloads, three run at once.
func (s *screen) downloadFile(e catalog.Entry, r source.Release, a source.Asset) {
	p := s.prefs.Get()
	d := download{
		key: downloadJob(e.Item.ID, r.Tag, a.Name), entry: e, release: r, asset: a, root: s.downloadRoot(p),
		checks: verifyChecks(p),
	}
	if status, ok := s.jobs.Status(d.key); ok && status.State == jobs.Running {
		return
	}
	s.putTransfer(transfer{key: d.key, itemID: e.Item.ID, tag: r.Tag, file: a.Name, total: a.Size})
	err := s.lanes.Start(laneDownload, d.key, func(ctx context.Context, report jobs.Report) error {
		return s.runDownload(ctx, report, d)
	})
	if err != nil {
		s.dropTransfer(d.key)
		if !errors.Is(err, jobs.ErrRunning) && !errors.Is(err, jobs.ErrStopped) {
			log.Print(err)
		}
	}
}

// cancelDownload stops a download; its .part file is kept so it can be resumed.
func (s *screen) cancelDownload(key string) {
	queued := s.lanes.Queued(key)
	if !s.jobs.Cancel(key) {
		return
	}
	// A job that never left the queue never runs, so nothing else would say it ended.
	if queued {
		s.changeTransfer(key, func(t *transfer) { t.state = transferCanceled })
	}
}

// runDownload is the download job: fetch, verify, then act on the verdict.
// Only failures that are not the network's or the publisher's fault are
// returned, so the job manager reports those and nothing routine.
func (s *screen) runDownload(ctx context.Context, report jobs.Report, d download) error {
	file, result, err := s.fetchVerified(ctx, d,
		func(p fetch.Progress) {
			s.changeTransfer(d.key, func(t *transfer) { t.done, t.total, t.speed = p.Done, p.Total, p.BytesPerSecond })
			report(int(p.Done>>10), int(p.Total>>10), "")
		},
		func() { s.changeTransfer(d.key, func(t *transfer) { t.state = transferVerifying }) })
	if err != nil {
		return s.downloadStopped(ctx, d, err)
	}
	return s.downloadVerified(ctx, d, file, result)
}

// fetchVerified downloads one file of a release and checks it. onProgress hears
// of the transfer, and onVerifying that the transfer is over.
func (s *screen) fetchVerified(ctx context.Context, d download, onProgress func(fetch.Progress), onVerifying func()) (fetch.File, fetch.Result, error) {
	dest := fetch.DestPath(d.root, d.entry.Item.Owner, d.entry.Item.Repo, d.release.Tag, d.asset.Name)
	access := s.sources.AssetRequest(d.entry.Ref(), d.asset)
	file, err := s.fetcher.Download(ctx, fetch.Request{URL: access.URL, Header: access.Header, Dest: dest, Size: d.asset.Size}, onProgress)
	if err != nil {
		return fetch.File{}, fetch.Result{}, fmt.Errorf("fetch %s: %w", d.asset.Name, err)
	}
	onVerifying()
	ref := d.entry.Ref()
	in := fetch.Input{
		Path: file.Path, SHA256: file.SHA256, Asset: d.asset, Siblings: d.release.Assets, Ref: ref,
		DigestOrigin: source.SiteLabel(ref.SourceKey()), Sidecar: s.sidecarReader(ref), Platform: s.platform,
		Checks: d.checks, TrustUnsigned: d.entry.Item.TrustUnsigned,
	}
	if source.IsGitHub(ref.Source) {
		// Build records exist only on GitHub.
		in.Provenance = func(ctx context.Context, sum string) (source.Provenance, error) {
			return s.github.Provenance(ctx, ref, sum)
		}
	}
	in.VirusTotal = s.virusTotalLookup(ctx, d.checks.VirusTotal)
	result, err := fetch.Verify(ctx, in)
	if err != nil {
		return fetch.File{}, fetch.Result{}, fmt.Errorf("verify %s: %w", d.asset.Name, err)
	}
	return file, result, nil
}

// sidecarTimeout bounds the fetch of one checksum file.
const sidecarTimeout = 15 * time.Second

// sidecarReader reads checksum files of a release through the site's asset
// endpoint, so those of a private repository can be read too.
func (s *screen) sidecarReader(ref source.RepoRef) fetch.SidecarReader {
	return func(ctx context.Context, a source.Asset) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, sidecarTimeout)
		defer cancel()
		access := s.sources.AssetRequest(ref, a)
		return s.fetcher.Get(ctx, fetch.Request{URL: access.URL, Header: access.Header}, fetch.MaxSidecarSize)
	}
}

// downloadStopped records a download that ended without a file. A cancelled
// one ends the job as cancelled; failures of the network or the publisher are
// recorded for the person and end it normally; a failure on this computer's
// disk is also returned, to be reported.
func (s *screen) downloadStopped(ctx context.Context, d download, err error) error {
	// The job's context is already cancelled when the download is.
	bg := context.WithoutCancel(ctx)
	if ctx.Err() != nil {
		s.changeTransfer(d.key, func(t *transfer) { t.state = transferCanceled })
		s.record(bg, activity.Info, d.entry.Item.ID, locales.ActivityDownloadCanceled(d.asset.Name, d.entry.FullName()))
		return fmt.Errorf("download %s: %w", d.asset.Name, ctx.Err())
	}
	log.Printf("download %s of %s: %v", d.asset.Name, d.entry.FullName(), err)
	reason := downloadErrorText(err)
	s.changeTransfer(d.key, func(t *transfer) { t.state, t.problem = transferFailed, reason })
	s.record(bg, activity.Error, d.entry.Item.ID, locales.ActivityDownloadFailed(d.asset.Name, d.entry.FullName(), reason))
	s.toast(components.ToastError, reason)
	var disk *fetch.LocalError
	if errors.As(err, &disk) {
		return fmt.Errorf("download %s of %s: %w", d.asset.Name, d.entry.FullName(), err)
	}
	return nil
}

// downloadVerified acts on the verdict. A rejected file is deleted. Any other
// file is kept and shown in the file manager; one with findings says so.
func (s *screen) downloadVerified(ctx context.Context, d download, file fetch.File, result fetch.Result) error {
	// The outcome is recorded even if the person cancels as the file lands.
	ctx = context.WithoutCancel(ctx)
	summary := verifySummary(result, d.checks)
	itemID, name, full := d.entry.Item.ID, d.asset.Name, d.entry.FullName()
	switch result.Verdict {
	case fetch.VerdictStop:
		reason := rejectedText(result)
		s.changeTransfer(d.key, func(t *transfer) {
			t.state, t.problem, t.findings, t.verified = transferFailed, reason, result.Findings, result
		})
		s.record(ctx, activity.Error, itemID, locales.ActivityDownloadBlocked(name, full, reason))
		s.toast(components.ToastError, reason)
		if err := os.Remove(file.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete rejected download %s: %w", file.Path, err)
		}
		return nil
	case fetch.VerdictAsk, fetch.VerdictSkip:
		s.changeTransfer(d.key, func(t *transfer) {
			t.state, t.summary, t.findings, t.verified, t.path = transferNeedsCheck, summary, result.Findings, result, file.Path
		})
		s.record(ctx, activity.Warning, itemID, locales.ActivityDownloadedCheck(name, full, summary))
		s.toast(components.ToastWarning, locales.ToastDownloadedCheck(name))
	case fetch.VerdictProceed:
		s.changeTransfer(d.key, func(t *transfer) { t.state, t.summary, t.path = transferDone, summary, file.Path })
		s.record(ctx, activity.Success, itemID, locales.ActivityDownloaded(name, full, summary))
		s.toast(components.ToastSuccess, locales.ToastDownloaded(name))
	}
	s.showFile(file.Path)
	return nil
}

// retryDownload starts a failed or cancelled download again; a kept .part
// file is resumed.
func (s *screen) retryDownload(e catalog.Entry, t transfer) {
	i := slices.IndexFunc(e.Releases, func(r source.Release) bool { return r.Tag == t.tag })
	if i < 0 {
		return
	}
	release := e.Releases[i]
	if j := slices.IndexFunc(release.Assets, func(a source.Asset) bool { return a.Name == t.file }); j >= 0 {
		s.downloadFile(e, release, release.Assets[j])
	}
}

// cleanDownloads removes what the download folder no longer needs: unfinished
// downloads a week old and, unless they are kept, finished ones.
func (s *screen) cleanDownloads() {
	p := s.prefs.Get()
	root, keep := s.downloadRoot(p), p.KeepDownloads
	s.background(func(context.Context) error {
		if err := fetch.Cleanup(root, keep, time.Now()); err != nil {
			log.Printf("clean download folder: %v", err)
			return fmt.Errorf("clean download folder %s: %w", filepath.Clean(root), err)
		}
		return nil
	})
}
