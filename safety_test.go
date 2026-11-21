package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"
)

// safetyFixture is an install fixture with the antivirus scan and the build
// record check turned on, as a fresh installation has them.
func newSafetyFixture(t *testing.T) *installFixture {
	t.Helper()
	f := newInstallFixture(t)
	f.screen.prefs.Update(func(p *preferences) { p.ScanDownloads, p.CheckProvenance = true, true })
	return f
}

// publishPortable publishes a release of a portable app and tracks it.
func (f *installFixture) publishPortable() catalog.Entry {
	f.t.Helper()
	f.source.publish("app", "", f.release("v1.0.0", 1, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v1.0.0")}))
	return f.track("app", catalog.AddOptions{})
}

func TestADownloadWindowsSecurityFlagsIsStoppedAndDeleted(t *testing.T) {
	f := newSafetyFixture(t)
	f.fake.Scanned = platform.ScanResult{State: platform.ScanThreat, Threat: "Virus:DOS/EICAR_Test_File"}
	entry := f.publishPortable()
	f.show(entry)

	click(t, f.tt, "Install")
	f.finish()

	want := "Windows Security says this download is dangerous (Virus:DOS/EICAR_Test_File). Release Sync stopped and deleted it."
	got := f.entry(entry)
	if got.Item.InstallError != want || got.InstalledVersion() != "" {
		t.Errorf("error %q, installed %q", got.Item.InstallError, got.InstalledVersion())
	}
	downloads := slices.DeleteFunc(filesIn(t, f.screen.downloadRoot(f.screen.prefs.Get())), func(name string) bool { return strings.HasSuffix(name, ".fetch-folder") })
	if len(filesIn(t, f.apps)) != 0 || len(downloads) != 0 {
		t.Errorf("the dangerous download or an install was left: %v %v", filesIn(t, f.apps), downloads)
	}
	if last := f.lastActivity(); last.Level != activity.Error || !strings.Contains(last.Message, want) {
		t.Errorf("activity = %+v", last)
	}
	if scanned := f.fake.ScannedPaths(); len(scanned) != 1 || !strings.HasSuffix(scanned[0], "app-win-x64.zip") {
		t.Errorf("scanned = %v", scanned)
	}
}

func TestAThreatWithoutANameAndAFileTheScannerRemovedAreStoppedToo(t *testing.T) {
	for scan, want := range map[platform.ScanResult]string{
		{State: platform.ScanThreat}:                "Windows Security says this download is dangerous. Release Sync stopped and deleted it.",
		{State: platform.ScanThreat, Removed: true}: "Windows Security removed this download because it is dangerous. Release Sync stopped the install.",
	} {
		f := newSafetyFixture(t)
		f.fake.Scanned = scan
		entry := f.publishPortable()
		f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
		f.finish()
		if got := f.entry(entry).Item.InstallError; got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	}
}

func TestACleanScanIsPartOfTheSummaryAndAScanThatCannotRunNeverBlocks(t *testing.T) {
	f := newSafetyFixture(t)
	entry := f.publishPortable()
	f.show(entry)
	click(t, f.tt, "Download file only")
	f.finish()
	f.settle()
	if last := f.lastActivity(); last.Level != activity.Success || !strings.Contains(last.Message, "Windows Security: clean") {
		t.Errorf("activity = %+v", last)
	}

	f.fake.Scanned = platform.ScanResult{State: platform.ScanUnavailable}
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.finish()
	if f.entry(entry).InstalledVersion() != "v1.0.0" {
		t.Fatal("a scan that could not run stopped the install")
	}
	if got := f.lastActivity().Message; !strings.Contains(got, "Installed acme/app v1.0.0") {
		t.Errorf("activity = %q", got)
	}
}

func TestScanningCanBeTurnedOff(t *testing.T) {
	f := newSafetyFixture(t)
	f.screen.prefs.Update(func(p *preferences) { p.ScanDownloads = false })
	f.fake.Scanned = platform.ScanResult{State: platform.ScanThreat}
	entry := f.publishPortable()
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.finish()
	if f.entry(entry).InstalledVersion() != "v1.0.0" || len(f.fake.ScannedPaths()) != 0 {
		t.Fatalf("scanned %v with the scan off", f.fake.ScannedPaths())
	}
}

// virusTotalReport is a VirusTotal that answers with the given stats and
// reports the requests it gets.
type virusTotalReport struct {
	mu       sync.Mutex
	requests []string
	keys     []string
}

func (v *virusTotalReport) serve(status int, body string) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		v.requests = append(v.requests, r.Method+" "+r.URL.Path)
		v.keys = append(v.keys, r.Header.Get("X-Apikey"))
		v.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return server
}

func (v *virusTotalReport) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.requests)
}

const flaggedBody = `{"data":{"attributes":{"last_analysis_stats":{"malicious":2,"suspicious":0,"harmless":60,"undetected":8}}}}`

// withVirusTotal turns the VirusTotal check on with a saved key and a stand-in service.
func (f *installFixture) withVirusTotal(status int, body string) *virusTotalReport {
	f.t.Helper()
	report := &virusTotalReport{}
	server := report.serve(status, body)
	f.t.Cleanup(server.Close)
	f.screen.virusTotal.BaseURL = server.URL
	f.screen.prefs.Update(func(p *preferences) { p.UseVirusTotal = true })
	if err := f.tokens.Set(f.t.Context(), virusTotalKeyName, []byte("my-vt-key")); err != nil {
		f.t.Fatal(err)
	}
	return report
}

func TestFlaggedByAntivirusEnginesAsksAndInstallsOnlyOnYes(t *testing.T) {
	f := newSafetyFixture(t)
	report := f.withVirusTotal(200, flaggedBody)
	entry := f.publishPortable()

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.waitFor("2 of 70 antivirus engines on VirusTotal flag this file")
	requireText(t, f.tt, "Some antivirus engines flag this download")
	requireNoText(t, f.tt, "Install app-win-x64.zip?")
	click(t, f.tt, "Cancel")
	f.finish()
	if f.entry(entry).InstalledVersion() != "" || len(filesIn(t, f.apps)) != 0 {
		t.Fatal("a flagged file was installed against the answer")
	}

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.answer("Some antivirus engines flag this download", "Install anyway")
	f.finish()
	if f.entry(entry).InstalledVersion() != "v1.0.0" {
		t.Fatal("a flagged file was not installed after the person said yes")
	}
	// Only the hash goes to VirusTotal, as a lookup.
	for _, r := range report.seen() {
		if !strings.HasPrefix(r, "GET /api/v3/files/") || len(strings.TrimPrefix(r, "GET /api/v3/files/")) != 64 {
			t.Errorf("request to VirusTotal: %q", r)
		}
	}
	report.mu.Lock()
	defer report.mu.Unlock()
	if len(report.keys) == 0 || report.keys[0] != "my-vt-key" {
		t.Errorf("keys sent = %v", report.keys)
	}
}

func TestAnUnattendedUpdateIsLeftWhenEnginesFlagTheFile(t *testing.T) {
	f := newSafetyFixture(t)
	entry := f.publishPortable()
	f.install(entry)
	f.withVirusTotal(200, flaggedBody)
	f.source.publish("app", "", f.release("v1.1.0", 2, "app-win-x64.zip", map[string][]byte{"app.exe": gui("v2")}), f.entry(entry).Releases[0])
	f.refresh(entry.Item.ID)

	var result outcome = -1
	var wg sync.WaitGroup
	wg.Add(1)
	if !f.screen.queueInstall(entry.Item.ID, "v1.1.0", true, func(o outcome) { result = o; wg.Done() }) {
		t.Fatal("the update did not start")
	}
	f.finish()
	wg.Wait()
	if result != outcomeLeft || f.entry(entry).InstalledVersion() != "v1.0.0" {
		t.Errorf("outcome %v, installed %q: the flagged update must be left for the person", result, f.entry(entry).InstalledVersion())
	}
	if got := f.lastActivity().Message; !strings.Contains(got, "2 of 70 antivirus engines on VirusTotal flag this file") {
		t.Errorf("activity = %q", got)
	}
}

func TestVirusTotalNotesNeverBlockAndAreInTheSummary(t *testing.T) {
	for status, want := range map[int]string{
		404: "VirusTotal: never seen", 401: "VirusTotal: your API key was rejected",
		429: "VirusTotal: limit reached", 503: "VirusTotal: unavailable right now",
	} {
		f := newSafetyFixture(t)
		f.withVirusTotal(status, "")
		entry := f.publishPortable()
		f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
		f.finish()
		if f.entry(entry).InstalledVersion() != "v1.0.0" {
			t.Errorf("status %d blocked the install", status)
		}
		if got := f.lastActivity().Message; !strings.Contains(got, "Installed") {
			t.Errorf("status %d: activity = %q", status, got)
		}
		f.show(entry)
		f.screen.downloadFile(f.entry(entry), f.entry(entry).Releases[0], f.entry(entry).Releases[0].Assets[0])
		f.settle()
		if got := f.lastActivity().Message; !strings.Contains(got, want) {
			t.Errorf("status %d: summary %q lacks %q", status, got, want)
		}
	}
}

func TestNothingIsSentToVirusTotalWithoutAKeyOrWhenItIsOff(t *testing.T) {
	f := newSafetyFixture(t)
	report := f.withVirusTotal(200, flaggedBody)
	if err := f.tokens.Delete(t.Context(), virusTotalKeyName); err != nil {
		t.Fatal(err)
	}
	entry := f.publishPortable()
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.finish()
	if f.entry(entry).InstalledVersion() != "v1.0.0" || len(report.seen()) != 0 {
		t.Fatalf("with no key: installed %q, requests %v", f.entry(entry).InstalledVersion(), report.seen())
	}

	if err := f.tokens.Set(t.Context(), virusTotalKeyName, []byte("key")); err != nil {
		t.Fatal(err)
	}
	f.screen.prefs.Update(func(p *preferences) { p.UseVirusTotal = false })
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.finish()
	if len(report.seen()) != 0 {
		t.Fatalf("with the check off: requests %v", report.seen())
	}
}

// attestationsServer stands in for GitHub: it answers a lookup of a file's
// build records with the workflow of repo, or with nothing.
func (f *installFixture) attestationsFrom(repo string) {
	f.t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/attestations/sha256:") {
			http.NotFound(w, r)
			return
		}
		if repo == "" {
			http.NotFound(w, r)
			return
		}
		statement := fmt.Sprintf(`{"predicate":{"buildDefinition":{"externalParameters":{"workflow":{"repository":"https://github.com/%s","path":".github/workflows/release.yml"}}}}}`, repo)
		_, _ = fmt.Fprintf(w, `{"attestations":[{"bundle":{"dsseEnvelope":{"payload":%q}}}]}`, base64.StdEncoding.EncodeToString([]byte(statement)))
	}))
	f.t.Cleanup(server.Close)
	f.screen.github = source.NewGitHub(source.GitHubOptions{BaseURL: server.URL, UserAgent: "test"})
}

func TestABuildFromAnotherRepositoryIsAskedAboutAndOneFromThisRepositoryIsNoted(t *testing.T) {
	f := newSafetyFixture(t)
	f.attestationsFrom("someone-else/app")
	entry := f.publishPortable()

	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.waitFor("Its build record says it was built from a different repository (someone-else/app).")
	f.answer("Install app-win-x64.zip?", "Cancel")
	f.finish()
	if f.entry(entry).InstalledVersion() != "" {
		t.Fatal("a build from another repository was installed against the answer")
	}

	f.attestationsFrom("acme/app")
	f.screen.startInstall(entry.Item.ID, "v1.0.0", nil)
	f.finish()
	if f.entry(entry).InstalledVersion() != "v1.0.0" {
		t.Fatal("a build from this repository was asked about")
	}
	f.show(entry)
	f.screen.downloadFile(f.entry(entry), f.entry(entry).Releases[0], f.entry(entry).Releases[0].Assets[0])
	f.settle()
	if got := f.lastActivity().Message; !strings.Contains(got, "build provenance: built from acme/app by .github/workflows/release.yml") {
		t.Errorf("summary = %q", got)
	}
}

func TestNoBuildRecordIsSaidNothingAbout(t *testing.T) {
	f := newSafetyFixture(t)
	f.attestationsFrom("")
	entry := f.publishPortable()
	f.show(entry)
	f.screen.downloadFile(entry, f.entry(entry).Releases[0], f.entry(entry).Releases[0].Assets[0])
	f.settle()
	if got := f.lastActivity().Message; strings.Contains(got, "provenance") {
		t.Errorf("summary = %q", got)
	}
}

func TestTheVirusTotalKeyIsKeptOnlyInTheCredentialStore(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	tt.SetSize(windowWidth, 2400)
	e.screen.openSettings("security")
	requireText(t, tt, "Verify checksums and signatures", "Scan downloads with Windows Security", "Check build provenance (GitHub)",
		"Safety hints when adding", "Look files up on VirusTotal", "VirusTotal API key")
	requireNoText(t, tt, "Remove key")

	typeInto(t, tt, "Your VirusTotal API key", "vt-secret-key")
	click(t, tt, "Save key")
	e.settle()
	requireText(t, tt, "Key saved.", "Remove key")
	if got, err := e.tokens.Get(t.Context(), virusTotalKeyName); err != nil || string(got) != "vt-secret-key" {
		t.Fatalf("stored key = %q, %v", got, err)
	}
	if e.screen.vtKey.input != "" {
		t.Error("the key stayed in the field")
	}
	assertTokenNotOnDisk(t, e, "vt-secret-key")

	click(t, tt, "Remove key")
	e.settle()
	requireText(t, tt, "Key removed.")
	if _, err := e.tokens.Get(t.Context(), virusTotalKeyName); err == nil {
		t.Error("the removed key is still stored")
	}
}

func TestTheSecuritySwitchesChangeThePreferences(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	tt := e.open()
	tt.SetSize(windowWidth, 2400)
	e.screen.openSettings("security")
	tt.Frame()
	for _, toggle := range []string{
		"Toggle scanning downloads with Windows Security", "Toggle checking build provenance",
		"Toggle looking files up on VirusTotal", "Toggle safety hints when adding",
	} {
		click(t, tt, toggle)
	}
	got := e.screen.prefs.Get()
	if got.ScanDownloads || got.CheckProvenance || !got.UseVirusTotal || got.SafetyHints {
		t.Errorf("preferences = %+v", got)
	}
}

// setRepository changes what the stand-in site says about a repository.
func (f *fakeSource) setRepository(name string, change func(*source.Repository)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.ToLower(testOwner + "/" + name)
	repo := f.repos[key]
	change(&repo)
	f.repos[key] = repo
}

// ownerServer stands in for GitHub's account lookup.
func ownerServer(t *testing.T, body string) *source.GitHub {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/acme" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return source.NewGitHub(source.GitHubOptions{BaseURL: server.URL, UserAgent: "test"})
}

func TestTheAddDialogShowsSafetyHintsWarningsFirst(t *testing.T) {
	prefs := defaultPreferences()
	prefs.InstallAfterAdding = false
	e := newEnv(t, prefs)
	e.source.publish("tool", "A tool", releaseWith("v1.0.0", 1, "tool-x64-setup.exe"))
	e.source.setRepository("tool", func(r *source.Repository) {
		r.Created, r.Pushed, r.Stars, r.License, r.Fork = time.Now().AddDate(0, 0, -12), time.Now(), 3, "", true
	})
	e.screen.github = ownerServer(t, fmt.Sprintf(`{"login":"acme","name":"Acme Inc","type":"Organization","created_at":%q}`, time.Now().AddDate(-5, 0, 0).Format(time.RFC3339)))
	tt := e.open()
	tt.SetSize(windowWidth, 2400)
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "At a glance", "This repository is only 12 days old.", "Very few people have starred it (3).", "No license is stated.",
		"This is a fork of another project.", "Published by an organization (Acme Inc).")
	texts := strings.Join(tt.Texts(), "\n")
	if strings.Index(texts, "This repository is only 12 days old.") > strings.Index(texts, "This is a fork of another project.") ||
		strings.Index(texts, "This is a fork of another project.") > strings.Index(texts, "Published by an organization") {
		t.Errorf("hints are not in the order warnings, facts, good signs: %q", tt.Texts())
	}
	click(t, tt, "Add") // Hints never block adding.
	e.settle()
	if _, ok := e.screen.catalog.Find(source.RepoRef{Owner: testOwner, Repo: "tool"}); !ok {
		t.Error("the repository was not added")
	}
}

func TestSafetyHintsCanBeTurnedOffAndUnknownDetailsSayNothing(t *testing.T) {
	prefs := defaultPreferences()
	prefs.SafetyHints = false
	e := newEnv(t, prefs)
	e.source.publish("tool", "A tool", releaseWith("v1.0.0", 1, "tool-x64-setup.exe"))
	e.source.setRepository("tool", func(r *source.Repository) { r.Created = time.Now().AddDate(0, 0, -12) })
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireText(t, tt, "Will install: tool-x64-setup.exe")
	requireNoText(t, tt, "At a glance")
}

func TestARepositoryWhoseDetailsAreNotKnownGetsNoHints(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "A tool", releaseWith("v1.0.0", 1, "tool-x64-setup.exe"))
	// The stand-in site gives no creation date, as a saved snapshot has none.
	tt := e.open()
	typeInto(t, tt, "Search or paste a repository link", "acme/tool")
	click(t, tt, "Add")
	e.settle()
	requireNoText(t, tt, "At a glance", "No license is stated.")
}

func TestRepositoryDetailsShowTheSameHints(t *testing.T) {
	e := newEnv(t, defaultPreferences())
	e.source.publish("tool", "A tool", releaseWith("v1.0.0", 1, "tool-x64-setup.exe"))
	e.source.setRepository("tool", func(r *source.Repository) {
		r.Created, r.Pushed, r.Stars, r.License, r.Archived = time.Now().AddDate(-4, 0, 0), time.Now().AddDate(-3, 0, 0), 2000, "MIT", true
	})
	e.screen.github = ownerServer(t, fmt.Sprintf(`{"login":"acme","type":"User","created_at":%q}`, time.Now().AddDate(-5, 0, 0).Format(time.RFC3339)))
	tt := e.open()
	tt.SetSize(windowWidth, 2400)
	repo, err := e.source.Repository(t.Context(), source.RepoRef{Owner: testOwner, Repo: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	e.screen.showRepository(repo)
	e.settle()
	requireText(t, tt, "At a glance", "It's archived, so it is no longer maintained.", "Widely used (2.0k stars).", "Active for 4.0 years.")
	requireNoText(t, tt, "Not updated since", "No license is stated.")
}
