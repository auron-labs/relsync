package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/auron-labs/mygo-framework/database"

	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/database/migrations"
	"github.com/aaronflorey/release-sync/source"
)

// fakeSource answers from maps and records what it was asked.
type fakeSource struct {
	mu        sync.Mutex
	repos     map[string]source.Repository
	pages     map[string]source.ReleasePage
	failures  map[string]error
	allowance source.Allowance
	// repoCalls and releaseCalls record requests; etags are the ETags sent.
	repoCalls, releaseCalls []string
	etags                   []string
	// hold, when set, blocks Releases until it is closed.
	hold    chan struct{}
	running int
	peak    int
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		repos: map[string]source.Repository{}, pages: map[string]source.ReleasePage{}, failures: map[string]error{},
	}
}

func key(ref source.RepoRef) string {
	if ref.Source == "" {
		return strings.ToLower(ref.String())
	}
	return strings.ToLower(ref.Source + "|" + ref.String())
}

func (f *fakeSource) publish(owner, name string, page source.ReleasePage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := source.RepoRef{Owner: owner, Repo: name}
	f.repos[key(ref)] = source.Repository{
		Owner: owner, Name: name, Description: name + " description", Stars: 42, Language: "Go",
		Topics: []string{"cli"}, AvatarURL: "https://avatars.test/" + owner,
	}
	f.pages[key(ref)] = page
}

func (f *fakeSource) Repository(_ context.Context, ref source.RepoRef) (source.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoCalls = append(f.repoCalls, key(ref))
	if err := f.failures[key(ref)]; err != nil {
		return source.Repository{}, err
	}
	repo, ok := f.repos[key(ref)]
	if !ok {
		return source.Repository{}, &source.Error{Kind: source.KindNotFound, Status: 404, Resource: ref.String()}
	}
	return repo, nil
}

func (f *fakeSource) Releases(ctx context.Context, ref source.RepoRef, etag string) (source.ReleasePage, error) {
	f.mu.Lock()
	f.releaseCalls = append(f.releaseCalls, key(ref))
	f.etags = append(f.etags, etag)
	f.running++
	f.peak = max(f.peak, f.running)
	hold := f.hold
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
	}()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return source.ReleasePage{}, fmt.Errorf("fake: %w", ctx.Err())
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failures[key(ref)]; err != nil {
		return source.ReleasePage{}, err
	}
	page, ok := f.pages[key(ref)]
	if !ok {
		return source.ReleasePage{}, &source.Error{Kind: source.KindNotFound, Status: 404, Resource: ref.String()}
	}
	if etag != "" && etag == page.ETag {
		return source.ReleasePage{NotModified: true, ETag: etag}, nil
	}
	return page, nil
}

func (f *fakeSource) Allowance() source.Allowance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allowance
}

func (f *fakeSource) counts() (repos, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.repoCalls), len(f.releaseCalls)
}

var day = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func rel(tag string, daysAfter int, opts ...func(*source.Release)) source.Release {
	r := source.Release{
		Tag: tag, Name: tag, Notes: "notes for " + tag, Published: day.AddDate(0, 0, daysAfter),
		Assets: []source.Asset{{Name: "tool-x64-setup.exe", Size: 1 << 20}},
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func pre(r *source.Release) { r.Prerelease = true }

func page(etag string, releases ...source.Release) source.ReleasePage {
	return source.ReleasePage{ETag: etag, Releases: releases}
}

type harness struct {
	t       *testing.T
	path    string
	db      *database.Database
	source  *fakeSource
	catalog *catalog.Catalog
}

func openHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, path: filepath.Join(t.TempDir(), "app.db"), source: newFakeSource()}
	h.open()
	return h
}

func (h *harness) open() {
	h.t.Helper()
	db, err := database.Open(h.t.Context(), h.path, migrations.FS())
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = db.Close() })
	c, err := catalog.Open(h.t.Context(), db.SQL, h.source, func(err error) string { return "described: " + err.Error() })
	if err != nil {
		h.t.Fatal(err)
	}
	h.db, h.catalog = db, c
}

// reopen simulates a restart on the same database file.
func (h *harness) reopen() {
	h.t.Helper()
	if err := h.db.Close(); err != nil {
		h.t.Fatal(err)
	}
	h.open()
}

func (h *harness) add(name string, opts catalog.AddOptions) catalog.Entry {
	h.t.Helper()
	p, err := h.catalog.Preview(h.t.Context(), source.RepoRef{Owner: "acme", Repo: name})
	if err != nil {
		h.t.Fatal(err)
	}
	e, err := h.catalog.Add(h.t.Context(), p, opts)
	if err != nil {
		h.t.Fatal(err)
	}
	return e
}

func (h *harness) check(id int64, force bool) catalog.CheckResult {
	h.t.Helper()
	res, err := h.catalog.Check(h.t.Context(), id, force)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) get(id int64) catalog.Entry {
	h.t.Helper()
	e, ok := h.catalog.Get(id)
	if !ok {
		h.t.Fatalf("item %d is not in the catalog", id)
	}
	return e
}

func latestTag(t *testing.T, e catalog.Entry) string {
	t.Helper()
	r, ok := e.Latest()
	if !ok {
		return ""
	}
	return r.Tag
}

func TestAddTracksARepositoryAndSurvivesARestart(t *testing.T) {
	h := openHarness(t)
	h.source.publish("Acme", "Tool", page(`"e1"`, rel("v1.1.0", 5, func(r *source.Release) {
		r.Assets = []source.Asset{{Name: "tool.zip", Size: 9, URL: "https://x/tool.zip", Digest: "sha256:ab", Downloads: 4}}
	}), rel("v1.0.0", 1)))

	e := h.add("tool", catalog.AddOptions{})
	if e.Item.Owner != "Acme" || e.Item.Repo != "Tool" || e.Item.Stars != 42 || !e.Item.MetadataFetched || e.Item.Description == "" {
		t.Fatalf("item = %+v", e.Item)
	}
	if latestTag(t, e) != "v1.1.0" || e.Status(false) != catalog.StatusNotInstalled {
		t.Fatalf("latest %q, status %v", latestTag(t, e), e.Status(false))
	}

	h.reopen()
	got, ok := h.catalog.Find(source.RepoRef{Owner: "ACME", Repo: "tool"})
	if !ok || got.Item.ID != e.Item.ID || got.Item.ETag != `"e1"` || len(got.Releases) != 2 {
		t.Fatalf("after restart: %+v, %v", got, ok)
	}
	if assets := got.Releases[0].Assets; len(assets) != 1 || assets[0].Digest != "sha256:ab" || assets[0].Downloads != 4 {
		t.Errorf("assets after restart = %+v", assets)
	}
	if !got.Releases[0].Published.Equal(day.AddDate(0, 0, 5)) || len(got.Item.Topics) != 1 {
		t.Errorf("release after restart = %+v, topics %v", got.Releases[0], got.Item.Topics)
	}
}

func TestAddingATrackedRepositoryReturnsTheExistingItem(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	first := h.add("tool", catalog.AddOptions{})

	p, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: "ACME", Repo: "TOOL"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.catalog.Add(t.Context(), p, catalog.AddOptions{})
	if !errors.Is(err, catalog.ErrExists) || again.Item.ID != first.Item.ID {
		t.Fatalf("Add = %+v, %v", again.Item, err)
	}
	if n := len(h.catalog.Entries().Get()); n != 1 {
		t.Errorf("%d items after adding twice", n)
	}
}

func TestPreviewFailureTracksNothing(t *testing.T) {
	h := openHarness(t)
	_, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: "no", Repo: "such"})
	var se *source.Error
	if !errors.As(err, &se) || se.Kind != source.KindNotFound {
		t.Fatalf("Preview = %v", err)
	}
	if len(h.catalog.Entries().Get()) != 0 {
		t.Error("a failed preview tracked something")
	}
}

func TestARepositoryWithNoReleasesCanBeTracked(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "empty", page(""))
	e := h.add("empty", catalog.AddOptions{})
	if e.Status(false) != catalog.StatusError || e.Item.LastFailure != catalog.FailNoReleases {
		t.Fatalf("status %v, failure %q", e.Status(false), e.Item.LastFailure)
	}
	if _, ok := e.Latest(); ok {
		t.Error("an empty repository has a latest release")
	}
	// Only pre-releases exist: nothing is tracked until the Item includes them.
	h.source.publish("acme", "beta", page("", rel("v0.1.0-rc1", 1, pre)))
	b := h.add("beta", catalog.AddOptions{})
	if b.Status(false) != catalog.StatusError {
		t.Errorf("pre-release-only status = %v", b.Status(false))
	}
	if err := h.catalog.SetIncludePrereleases(t.Context(), b.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := h.get(b.Item.ID); got.Status(false) != catalog.StatusNotInstalled || latestTag(t, got) != "v0.1.0-rc1" {
		t.Errorf("after including pre-releases: status %v, latest %q", got.Status(false), latestTag(t, got))
	}
}

func TestIAlreadyHaveThisRecordsTheInstalledVersion(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "blank", page("", rel("v2.0.0", 2), rel("v1.0.0", 1)))
	h.source.publish("acme", "typed", page("", rel("v2.0.0", 2), rel("v1.0.0", 1)))

	blank := h.add("blank", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "  "})
	if blank.InstalledVersion() != "v2.0.0" || blank.Status(false) != catalog.StatusUpToDate {
		t.Errorf("blank: installed %q, status %v", blank.InstalledVersion(), blank.Status(false))
	}
	typed := h.add("typed", catalog.AddOptions{AlreadyHave: true, InstalledVersion: " 1.0.0 "})
	if typed.InstalledVersion() != "1.0.0" || typed.Status(false) != catalog.StatusUpdate {
		t.Errorf("typed: installed %q, status %v", typed.InstalledVersion(), typed.Status(false))
	}
	if typed.Install == nil || typed.Install.At.IsZero() {
		t.Errorf("install record = %+v", typed.Install)
	}
	h.source.publish("acme", "unticked", page("", rel("v1", 1)))
	if e := h.add("unticked", catalog.AddOptions{InstalledVersion: "v1"}); e.InstalledVersion() != "" {
		t.Errorf("a version typed without ticking the box was recorded: %q", e.InstalledVersion())
	}
}

func TestCheckFindsNewReleasesAndUsesTheETag(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page(`"e1"`, rel("v1.0.0", 1)))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true})
	id := e.Item.ID

	// Nothing changed: the conditional request keeps the cached releases.
	if res := h.check(id, false); res.Failure != nil {
		t.Fatal(res.Failure)
	}
	if got := h.source.etags[len(h.source.etags)-1]; got != `"e1"` {
		t.Errorf("etag sent = %q", got)
	}
	if got := h.get(id); len(got.Releases) != 1 || got.Item.LastChecked.IsZero() {
		t.Errorf("after not modified: %+v", got)
	}

	h.source.publish("acme", "tool", page(`"e2"`, rel("v1.1.0", 3), rel("v1.0.0", 1)))
	h.check(id, false)
	got := h.get(id)
	if latestTag(t, got) != "v1.1.0" || got.Item.ETag != `"e2"` || got.Status(false) != catalog.StatusUpdate || !got.HasUpdate() {
		t.Fatalf("after a new release: latest %q etag %q status %v", latestTag(t, got), got.Item.ETag, got.Status(false))
	}

	// A forced check ignores the stored ETag.
	h.check(id, true)
	if got := h.source.etags[len(h.source.etags)-1]; got != "" {
		t.Errorf("forced check sent etag %q", got)
	}
}

func TestCheckFetchesMetadataOnlyOnce(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	p, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: "acme", Repo: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := h.catalog.Add(t.Context(), p, catalog.AddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	repos, _ := h.source.counts()
	h.check(e.Item.ID, true)
	h.check(e.Item.ID, true)
	if after, _ := h.source.counts(); after != repos {
		t.Errorf("repository requested %d more times for an Item whose metadata is known", after-repos)
	}
}

func TestMetadataIsSkippedWhenTheAnonymousAllowanceIsNearlySpent(t *testing.T) {
	for _, c := range []struct {
		name      string
		allowance source.Allowance
		fetched   bool
	}{
		{"unknown", source.Allowance{}, true},
		{"plenty", source.Allowance{Known: true, Remaining: 26}, true},
		{"nearly spent", source.Allowance{Known: true, Remaining: 25}, false},
		{"authenticated", source.Allowance{Known: true, Remaining: 3, Authenticated: true}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := openHarness(t)
			// An Item added before its metadata was ever fetched, as a Library from an import would be.
			h.source.publish("acme", "tool", page("", rel("v1", 1)))
			h.source.allowance = c.allowance
			e := addWithoutMetadata(t, h, "acme", "tool")
			before, _ := h.source.counts()
			h.check(e.Item.ID, false)
			after, _ := h.source.counts()
			if (after-before == 1) != c.fetched {
				t.Errorf("repository requests = %d, want fetched=%v", after-before, c.fetched)
			}
			if got := h.get(e.Item.ID); got.Item.MetadataFetched != c.fetched {
				t.Errorf("MetadataFetched = %v", got.Item.MetadataFetched)
			}
		})
	}
}

// addWithoutMetadata tracks a repository the way a check would find it: the
// preview reports no metadata, so the Item has not had it fetched yet.
func addWithoutMetadata(t *testing.T, h *harness, owner, name string) catalog.Entry {
	t.Helper()
	p, err := h.catalog.Preview(t.Context(), source.RepoRef{Owner: owner, Repo: name})
	if err != nil {
		t.Fatal(err)
	}
	e, err := h.catalog.Add(t.Context(), p, catalog.AddOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL.ExecContext(t.Context(), `UPDATE items SET metadata_fetched = 0, stars = 0 WHERE id = ?`, e.Item.ID); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	return h.get(e.Item.ID)
}

func TestFailedChecksAreRecordedAndRecover(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page(`"e1"`, rel("v1.0.0", 1)))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true})
	id := e.Item.ID

	delete(h.source.pages, "acme/tool")
	res := h.check(id, true)
	var se *source.Error
	if !errors.As(res.Failure, &se) || se.Kind != source.KindNotFound {
		t.Fatalf("failure = %v", res.Failure)
	}
	got := h.get(id)
	if !got.Item.RepoMissing || got.Item.LastFailure != catalog.FailNotFound || !strings.HasPrefix(got.Item.LastFailureText, "described: ") {
		t.Errorf("item after not found = %+v", got.Item)
	}
	// Releases already known still show; the failure is for the error box.
	if got.Status(false) != catalog.StatusUpToDate || latestTag(t, got) != "v1.0.0" {
		t.Errorf("status %v, latest %q", got.Status(false), latestTag(t, got))
	}

	h.source.publish("acme", "tool", page(`"e2"`, rel("v1.0.0", 1)))
	h.check(id, true)
	if got := h.get(id); got.Item.RepoMissing || got.Item.LastFailure != catalog.FailNone || got.Item.LastFailureText != "" {
		t.Errorf("failure survived a good check: %+v", got.Item)
	}

	h.source.failures["acme/tool"] = &source.Error{Kind: source.KindUnreachable}
	h.check(id, true)
	if got := h.get(id); got.Item.LastFailure != catalog.FailOther || got.Item.RepoMissing {
		t.Errorf("item after unreachable = %+v", got.Item)
	}
}

func TestAnItemWithNoKnownReleaseShowsAnErrorWhenItsCheckFails(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "empty", page(""))
	e := h.add("empty", catalog.AddOptions{})
	h.source.failures["acme/empty"] = &source.Error{Kind: source.KindUnreachable}
	h.check(e.Item.ID, false)
	got := h.get(e.Item.ID)
	if got.Status(false) != catalog.StatusError || got.Item.LastFailure != catalog.FailOther {
		t.Errorf("status %v, failure %q", got.Status(false), got.Item.LastFailure)
	}
}

func TestRateLimitedResult(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	e := h.add("tool", catalog.AddOptions{})
	h.source.failures["acme/tool"] = &source.Error{Kind: source.KindRateLimited, Status: 403}
	if res := h.check(e.Item.ID, false); !res.RateLimited() {
		t.Errorf("result = %+v", res)
	}
	h.source.failures["acme/tool"] = &source.Error{Kind: source.KindUnreachable}
	if res := h.check(e.Item.ID, false); res.RateLimited() || res.Failure == nil {
		t.Errorf("result = %+v", res)
	}
}

func TestCheckOfARemovedItem(t *testing.T) {
	h := openHarness(t)
	if _, err := h.catalog.Check(t.Context(), 99, false); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("Check = %v", err)
	}
}

func TestMarkInstalledSkipAndHold(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v2.0.0", 3), rel("v1.0.0", 1)))
	e := h.add("tool", catalog.AddOptions{})
	id := e.Item.ID
	ctx := t.Context()

	if err := h.catalog.MarkInstalled(ctx, id, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := h.get(id); got.Status(false) != catalog.StatusUpdate || got.InstalledVersion() != "v1.0.0" {
		t.Fatalf("status %v, installed %q", got.Status(false), got.InstalledVersion())
	}

	if err := h.catalog.SetSkippedVersion(ctx, id, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := h.get(id); got.Status(false) != catalog.StatusIgnored || got.HasUpdate() || !got.Skipped() {
		t.Errorf("skipped: status %v", got.Status(false))
	}
	// A newer release than the skipped one is an update again.
	h.source.publish("acme", "tool", page("", rel("v2.1.0", 5), rel("v2.0.0", 3), rel("v1.0.0", 1)))
	h.check(id, true)
	if got := h.get(id); got.Status(false) != catalog.StatusUpdate {
		t.Errorf("a release newer than the skipped one: status %v", got.Status(false))
	}
	if err := h.catalog.SetSkippedVersion(ctx, id, ""); err != nil {
		t.Fatal(err)
	}

	if err := h.catalog.SetHoldUpdates(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if got := h.get(id); got.Status(false) != catalog.StatusIgnored || got.HasUpdate() {
		t.Errorf("held: status %v", got.Status(false))
	}

	// Marking the current version installed ends the update and survives a restart.
	if err := h.catalog.MarkInstalled(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if got := h.get(id); got.InstalledVersion() != "v2.1.0" || got.Status(false) != catalog.StatusUpToDate || !got.Item.HoldUpdates {
		t.Errorf("after restart: installed %q status %v hold %v", got.InstalledVersion(), got.Status(false), got.Item.HoldUpdates)
	}
}

func TestMarkInstalledWithoutAReleaseFails(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "empty", page(""))
	e := h.add("empty", catalog.AddOptions{})
	if err := h.catalog.MarkInstalled(t.Context(), e.Item.ID, ""); !errors.Is(err, catalog.ErrNoRelease) {
		t.Errorf("MarkInstalled = %v", err)
	}
	if err := h.catalog.MarkInstalled(t.Context(), 99, "v1"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("MarkInstalled of a missing item = %v", err)
	}
}

func TestStopTrackingRemovesTheItemAndItsData(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true})
	if err := h.catalog.Remove(t.Context(), e.Item.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.catalog.Get(e.Item.ID); ok {
		t.Error("removed item is still in the catalog")
	}
	for _, table := range []string{"items", "releases", "installs"} {
		var n int
		if err := h.db.SQL.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s has %d rows after removal (%v)", table, n, err)
		}
	}
	// The repository can be tracked again.
	h.add("tool", catalog.AddOptions{})
}

func TestLatestIsTheHighestVersionNotTheNewestPublished(t *testing.T) {
	h := openHarness(t)
	// A maintenance release for the old line was published after 2.0.0.
	h.source.publish("acme", "tool", page("", rel("v1.9.5", 10), rel("v2.0.0", 5), rel("v1.9.4", 1)))
	e := h.add("tool", catalog.AddOptions{})
	if got := latestTag(t, e); got != "v2.0.0" {
		t.Errorf("latest = %q", got)
	}
}

func TestPreReleasesAndThrowawayBuilds(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("",
		rel("branch-fix-7", 9), rel("v2.0.0-rc1", 8, pre), rel("v1.5.0", 5), rel("pr-12", 4)))
	e := h.add("tool", catalog.AddOptions{})
	if got := latestTag(t, e); got != "v1.5.0" {
		t.Errorf("latest without pre-releases = %q, want the stable release over newer throwaways", got)
	}
	h.source.publish("acme", "tool2", page("", rel("v2.0.0-rc1", 8, pre), rel("v1.5.0", 5)))
	if got := latestTag(t, h.add("tool2", catalog.AddOptions{IncludePrereleases: true})); got != "v2.0.0-rc1" {
		t.Errorf("latest with pre-releases = %q", got)
	}
}

func TestOnlyThrowawayBuildsAreStillTracked(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("branch-main", 2), rel("branch-dev", 1)))
	e := h.add("tool", catalog.AddOptions{})
	if got := latestTag(t, e); got != "branch-main" && got != "branch-dev" {
		t.Errorf("latest = %q, throwaways are the only releases", got)
	}
}

func TestTrackedLaneFollowsTheNewestReleaseFamily(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "mono", page("",
		rel("desktop-v0.4.0", 6), rel("cli-v2.0.0", 5), rel("desktop-v0.3.0", 2), rel("cli-v1.0.0", 1)))
	e := h.add("mono", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "desktop-v0.3.0"})
	if e.Item.Lane != "desktop-v" {
		t.Fatalf("lane = %q", e.Item.Lane)
	}
	// cli-v2.0.0 has the higher number but is another product.
	if got := latestTag(t, e); got != "desktop-v0.4.0" {
		t.Errorf("latest = %q", got)
	}
	if n := len(e.NewerReleases()); n != 1 {
		t.Errorf("%d newer releases in the lane", n)
	}

	h.source.publish("acme", "single", page("", rel("v1.1.0", 2), rel("1.0.0", 1)))
	if lane := h.add("single", catalog.AddOptions{}).Item.Lane; lane != "" {
		t.Errorf("a v and a bare tag are one family, lane = %q", lane)
	}
}

func TestNewerReleasesAreTheNotesYouAreBehindOn(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("",
		rel("v1.3.0", 4), rel("v1.2.1-rc1", 3, pre), rel("v1.2.0", 2), rel("v1.1.0", 1), rel("v1.0.0", 0)))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "v1.1.0"})
	newer := e.NewerReleases()
	tags := make([]string, 0, len(newer))
	for _, r := range newer {
		tags = append(tags, r.Tag)
	}
	if got := strings.Join(tags, ","); got != "v1.3.0,v1.2.0" {
		t.Errorf("newer releases = %s", got)
	}
	h.source.publish("acme", "tool3", page("", rel("v1.0.0", 0)))
	if n := len(h.add("tool3", catalog.AddOptions{}).NewerReleases()); n != 0 {
		t.Errorf("an Item that is not installed has %d newer releases", n)
	}
}

func TestAVersionOutsideTheCachedReleasesIsTreatedAsOlder(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("nx2-build-90", 5), rel("nx2-build-80", 2)))
	e := h.add("tool", catalog.AddOptions{AlreadyHave: true, InstalledVersion: "build-212"})
	if !e.HasUpdate() {
		t.Error("a version from another tag style, older than every cached release, should be updatable")
	}
}

func TestOnlyTheNewest30ReleasesAreCachedWithBoundedNotes(t *testing.T) {
	h := openHarness(t)
	var releases []source.Release
	for i := 40; i > 0; i-- {
		r := rel(fmt.Sprintf("v1.%d.0", i), i)
		r.Notes = strings.Repeat("é", 4000)
		releases = append(releases, r)
	}
	h.source.publish("acme", "tool", page("", releases...))
	e := h.add("tool", catalog.AddOptions{})
	if len(e.Releases) != 30 || e.Releases[0].Tag != "v1.40.0" {
		t.Fatalf("%d releases, first %q", len(e.Releases), e.Releases[0].Tag)
	}
	if n := len(e.Releases[0].Notes); n > 5004 || !strings.HasSuffix(e.Releases[0].Notes, "…") {
		t.Errorf("notes are %d bytes", n)
	}
}

func TestCheckAllChecksAFewAtATimeAndSkipsBusyItems(t *testing.T) {
	h := openHarness(t)
	for i := range 6 {
		h.source.publish("acme", fmt.Sprintf("tool%d", i), page("", rel("v1", 1)))
		h.add(fmt.Sprintf("tool%d", i), catalog.AddOptions{})
	}
	manager := newManager(t)
	checker := catalog.NewChecker(h.catalog, manager)
	h.source.hold = make(chan struct{})

	type result struct {
		sum catalog.Summary
		err error
	}
	done := make(chan result)
	go func() {
		sum, err := checker.CheckAll(t.Context(), false, 2, func(int, int, string) {})
		done <- result{sum, err}
	}()
	waitFor(t, "two checks in flight", func() bool {
		h.source.mu.Lock()
		defer h.source.mu.Unlock()
		return h.source.running == 2
	})
	time.Sleep(20 * time.Millisecond) // a third check must not start while two hold the slots
	h.source.mu.Lock()
	running := h.source.running
	h.source.mu.Unlock()
	if running != 2 {
		t.Errorf("%d checks in flight with a limit of 2", running)
	}
	// The Item's busy state is its job's.
	first := h.catalog.Entries().Get()[0].Item.ID
	if st, ok := manager.Status(catalog.CheckJob(first)); !ok || st.State != "running" {
		t.Errorf("status of the first item's job = %+v, %v", st, ok)
	}
	close(h.source.hold)
	r := <-done
	if r.err != nil || r.sum.Checked != 6 || r.sum.Failed != 0 || r.sum.Skipped != 0 || r.sum.Total != 6 {
		t.Errorf("summary = %+v, %v", r.sum, r.err)
	}
	if h.source.peak > 2 {
		t.Errorf("peak concurrency %d exceeded the limit of 2", h.source.peak)
	}

	// An Item whose check is already running is skipped, not queued.
	h.source.hold = make(chan struct{})
	busy := h.catalog.Entries().Get()[0].Item.ID
	started := make(chan struct{})
	if err := checker.CheckItem(busy, false, func(context.Context, catalog.CheckResult, error) { close(started) }); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(h.source.hold)
	}()
	sum, err := checker.CheckAll(t.Context(), false, 3, func(int, int, string) {})
	if err != nil || sum.Skipped != 1 || sum.Checked != 5 {
		t.Errorf("summary with a busy item = %+v, %v", sum, err)
	}
	<-started
}

func TestCheckAllStopsAtARateLimit(t *testing.T) {
	h := openHarness(t)
	for i := range 5 {
		h.source.publish("acme", fmt.Sprintf("tool%d", i), page("", rel("v1", 1)))
		h.add(fmt.Sprintf("tool%d", i), catalog.AddOptions{})
	}
	for i := range 5 {
		h.source.failures[fmt.Sprintf("acme/tool%d", i)] = &source.Error{Kind: source.KindRateLimited, Status: 403}
	}
	checker := catalog.NewChecker(h.catalog, newManager(t))
	sum, err := checker.CheckAll(t.Context(), false, 1, func(int, int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.RateLimited() || sum.Checked != 1 || sum.Failed != 1 || sum.Skipped != 4 {
		t.Errorf("summary = %+v", sum)
	}
	if _, releases := h.source.counts(); releases != 5+1 { // five previews, then one check
		t.Errorf("%d release requests, the rest should have been skipped", releases)
	}
}

func TestCheckAllCanBeCanceled(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	h.add("tool", catalog.AddOptions{})
	h.source.hold = make(chan struct{})
	checker := catalog.NewChecker(h.catalog, newManager(t))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := checker.CheckAll(ctx, false, 4, func(int, int, string) {})
		done <- err
	}()
	waitFor(t, "the check to start", func() bool {
		h.source.mu.Lock()
		defer h.source.mu.Unlock()
		return h.source.running == 1
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("CheckAll = %v", err)
	}
	if got := h.catalog.Entries().Get()[0]; !got.Item.LastChecked.Equal(got.Item.AddedAt) {
		t.Error("a canceled check was recorded")
	}
}

func TestEntriesCellNotifiesSubscribers(t *testing.T) {
	h := openHarness(t)
	changed := make(chan int, 8)
	stop := h.catalog.Entries().Subscribe(func(e []catalog.Entry) { changed <- len(e) })
	defer stop()
	h.source.publish("acme", "tool", page("", rel("v1", 1)))
	h.add("tool", catalog.AddOptions{})
	select {
	case n := <-changed:
		if n != 1 {
			t.Errorf("subscriber saw %d entries", n)
		}
	case <-time.After(time.Second):
		t.Fatal("no notification")
	}
}

func TestTrustUnsignedSurvivesARestart(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("e", rel("v1.0.0", 1)))
	id := h.add("tool", catalog.AddOptions{}).Item.ID
	if h.get(id).Item.TrustUnsigned {
		t.Fatal("a new Item trusts unsigned programs")
	}
	if err := h.catalog.SetTrustUnsigned(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if !h.get(id).Item.TrustUnsigned {
		t.Fatal("the choice was not kept")
	}
	if err := h.catalog.SetTrustUnsigned(t.Context(), 999, true); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("unknown Item = %v", err)
	}
}

func TestCachedAssetsKeepTheirIDs(t *testing.T) {
	h := openHarness(t)
	r := rel("v1.0.0", 1)
	r.Assets = []source.Asset{{ID: 77, Name: "tool.zip", Size: 5, URL: "https://example.test/tool.zip", Digest: "sha256:ab"}}
	h.source.publish("acme", "tool", page("e", r))
	id := h.add("tool", catalog.AddOptions{}).Item.ID
	h.reopen()
	got := h.get(id).Releases[0].Assets
	if len(got) != 1 || got[0] != r.Assets[0] {
		t.Fatalf("assets = %+v", got)
	}
}

func TestNotifiedVersionsAndAutoUpdateSurviveARestart(t *testing.T) {
	h := openHarness(t)
	h.source.publish("acme", "tool", page("", rel("v1.0.0", 1)))
	h.source.publish("acme", "other", page("", rel("v1.0.0", 1)))
	tool, other := h.add("tool", catalog.AddOptions{}), h.add("other", catalog.AddOptions{})
	ctx := t.Context()

	if err := h.catalog.SetNotifiedVersions(ctx, map[int64]string{tool.Item.ID: "v1.0.0", other.Item.ID: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := h.catalog.SetAutoUpdate(ctx, tool.Item.ID, true); err != nil {
		t.Fatal(err)
	}
	// Turning automatic updates on makes the Item's update new again; the other Item is untouched.
	if got := h.get(tool.Item.ID); !got.Item.AutoUpdate || got.Item.NotifiedVersion != "" {
		t.Errorf("after turning auto-update on: %+v", got.Item)
	}
	if got := h.get(other.Item.ID); got.Item.NotifiedVersion != "v1.0.0" {
		t.Errorf("another Item's notified version = %q", got.Item.NotifiedVersion)
	}
	if err := h.catalog.SetNotifiedVersions(ctx, map[int64]string{tool.Item.ID: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	// Turning it off keeps what the user was told.
	if err := h.catalog.SetAutoUpdate(ctx, tool.Item.ID, false); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if got := h.get(tool.Item.ID); got.Item.AutoUpdate || got.Item.NotifiedVersion != "v1.0.0" {
		t.Errorf("after restart: %+v", got.Item)
	}

	if err := h.catalog.ForgetNotifiedVersions(ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range h.catalog.Entries().Get() {
		if e.Item.NotifiedVersion != "" {
			t.Errorf("%s still notified of %q", e.FullName(), e.Item.NotifiedVersion)
		}
	}
	if err := h.catalog.SetAutoUpdate(ctx, 999, true); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("SetAutoUpdate of an unknown Item = %v, want ErrNotFound", err)
	}
}
