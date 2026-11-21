package fetch_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"
)

var tool = source.RepoRef{Owner: "acme", Repo: "tool"}

func TestAThreatStopsTheDownloadWhateverElseIsTrue(t *testing.T) {
	for name, scan := range map[string]platform.ScanResult{
		"named":   {State: platform.ScanThreat, Threat: "Virus:DOS/EICAR_Test_File"},
		"counted": {State: platform.ScanThreat},
		"removed": {State: platform.ScanThreat, Removed: true},
	} {
		in, _ := verifyCase(t, "setup.exe", content)
		in.Asset.Digest = "sha256:" + digest(content) // The publisher's checksum matches.
		fake := &platform.Fake{Scanned: scan, Result: platform.Signature{State: platform.SignatureTrusted, Signer: "Example Corp"}}
		in.Platform, in.Checks.Scan, in.TrustUnsigned = fake, true, true
		r := verify(t, in)
		if r.Verdict != fetch.VerdictStop || !slices.Equal(r.Findings, []fetch.Finding{fetch.FindingThreat}) || r.Scan != scan {
			t.Errorf("%s: result = %+v", name, r)
		}
		if len(fake.Checked()) != 0 {
			t.Errorf("%s: checked the signature of a file already rejected", name)
		}
	}
}

func TestAThreatIsFoundInAFileThatIsAlreadyGone(t *testing.T) {
	in, _ := verifyCase(t, "setup.exe", content)
	if err := os.Remove(in.Path); err != nil {
		t.Fatal(err)
	}
	in.Platform, in.Checks.Scan = &platform.Fake{Scanned: platform.ScanResult{State: platform.ScanThreat, Removed: true}}, true
	if r := verify(t, in); r.Verdict != fetch.VerdictStop {
		t.Fatalf("result = %+v", r)
	}
}

func TestAScanThatCouldNotBeMadeNeverBlocks(t *testing.T) {
	for name, fake := range map[string]*platform.Fake{
		"unavailable": {Scanned: platform.ScanResult{State: platform.ScanUnavailable}},
		"failed":      {ScanErr: errors.New("the scanner crashed")},
	} {
		in, _ := verifyCase(t, "tool.zip", content)
		in.Platform, in.Checks.Scan = fake, true
		r := verify(t, in)
		if r.Verdict != fetch.VerdictProceed || r.Scan.State != platform.ScanUnavailable {
			t.Errorf("%s: result = %+v", name, r)
		}
	}
}

func TestAScanOnlyHappensWhenItIsOn(t *testing.T) {
	in, _ := verifyCase(t, "tool.zip", content)
	fake := &platform.Fake{Scanned: platform.ScanResult{State: platform.ScanThreat}}
	in.Platform = fake
	if r := verify(t, in); r.Verdict != fetch.VerdictProceed || r.Scan.State != "" || len(fake.ScannedPaths()) != 0 {
		t.Fatalf("result = %+v", r)
	}
	in.Checks.Scan = true
	if r := verify(t, in); r.Verdict != fetch.VerdictStop || !slices.Equal(fake.ScannedPaths(), []string{in.Path}) {
		t.Fatalf("result = %+v", r)
	}
}

func provenance(p source.Provenance, err error) fetch.ProvenanceLookup {
	return func(context.Context, string) (source.Provenance, error) { return p, err }
}

func TestProvenanceVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		found      source.Provenance
		err        error
		unattended bool
		want       fetch.Verdict
		findings   []fetch.Finding
	}{
		{name: "built from the same repository", found: source.Provenance{Found: true, Repository: "Acme/Tool", Workflow: "release.yml"}, want: fetch.VerdictProceed},
		{name: "a link to a record in this repository", found: source.Provenance{Found: true}, want: fetch.VerdictProceed},
		{name: "built from another repository asks", found: source.Provenance{Found: true, Repository: "evil/tool"}, want: fetch.VerdictAsk, findings: []fetch.Finding{fetch.FindingProvenanceMismatch}},
		{name: "built from another repository skips unattended", found: source.Provenance{Found: true, Repository: "evil/tool"}, unattended: true, want: fetch.VerdictSkip, findings: []fetch.Finding{fetch.FindingProvenanceMismatch}},
		{name: "no record is the normal case", want: fetch.VerdictProceed},
		{name: "a failed lookup is no answer", err: errors.New("rate limited"), want: fetch.VerdictProceed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, _ := verifyCase(t, "tool.zip", content)
			in.Ref, in.Checks.Provenance, in.Unattended = tool, true, tc.unattended
			in.Provenance = provenance(tc.found, tc.err)
			r := verify(t, in)
			if r.Verdict != tc.want || !slices.Equal(r.Findings, tc.findings) {
				t.Fatalf("verdict = %v findings = %v, want %v %v", r.Verdict, r.Findings, tc.want, tc.findings)
			}
		})
	}
}

func TestTrustingUnsignedProgramsDoesNotExcuseABuildFromAnotherRepository(t *testing.T) {
	in, _ := verifyCase(t, "setup.exe", content)
	in.Platform = &platform.Fake{Result: platform.Signature{State: platform.SignatureUnsigned}}
	in.Ref, in.Checks.Provenance, in.TrustUnsigned = tool, true, true
	in.Provenance = provenance(source.Provenance{Found: true, Repository: "evil/tool"}, nil)
	r := verify(t, in)
	if r.Verdict != fetch.VerdictAsk || !slices.Equal(r.Findings, []fetch.Finding{fetch.FindingProvenanceMismatch}) {
		t.Fatalf("result = %+v", r)
	}
}

func TestProvenanceIsOnlyAskedForWhenItIsOn(t *testing.T) {
	in, _ := verifyCase(t, "tool.zip", content)
	asked := ""
	in.Provenance = func(_ context.Context, sum string) (source.Provenance, error) {
		asked = sum
		return source.Provenance{}, nil
	}
	verify(t, in)
	if asked != "" {
		t.Fatalf("asked about %s with the check off", asked)
	}
	in.Checks.Provenance = true
	verify(t, in)
	if asked != digest(content) {
		t.Fatalf("asked about %q, want the file's hash", asked)
	}
}

func lookup(report fetch.VirusTotalReport, err error) fetch.VirusTotalLookup {
	return func(context.Context, string) (fetch.VirusTotalReport, error) { return report, err }
}

func TestVirusTotalVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		report     fetch.VirusTotalReport
		unattended bool
		want       fetch.Verdict
		findings   []fetch.Finding
	}{
		{name: "clean", report: fetch.VirusTotalReport{Status: fetch.VirusTotalReported, Total: 70}, want: fetch.VerdictProceed},
		{name: "flagged asks", report: fetch.VirusTotalReport{Status: fetch.VirusTotalReported, Flagged: 3, Total: 70}, want: fetch.VerdictAsk, findings: []fetch.Finding{fetch.FindingVirusTotal}},
		{name: "flagged skips unattended", report: fetch.VirusTotalReport{Status: fetch.VirusTotalReported, Flagged: 1, Total: 70}, unattended: true, want: fetch.VerdictSkip, findings: []fetch.Finding{fetch.FindingVirusTotal}},
		{name: "never seen", report: fetch.VirusTotalReport{Status: fetch.VirusTotalNeverSeen}, want: fetch.VerdictProceed},
		{name: "no results yet", report: fetch.VirusTotalReport{Status: fetch.VirusTotalNoResults}, want: fetch.VerdictProceed},
		{name: "key rejected", report: fetch.VirusTotalReport{Status: fetch.VirusTotalKeyRejected}, want: fetch.VerdictProceed},
		{name: "limit reached", report: fetch.VirusTotalReport{Status: fetch.VirusTotalLimited}, want: fetch.VerdictProceed},
		{name: "unavailable", report: fetch.VirusTotalReport{Status: fetch.VirusTotalUnavailable}, want: fetch.VerdictProceed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, _ := verifyCase(t, "tool.zip", content)
			in.Checks.VirusTotal, in.Unattended, in.VirusTotal = true, tc.unattended, lookup(tc.report, nil)
			r := verify(t, in)
			if r.Verdict != tc.want || !slices.Equal(r.Findings, tc.findings) || r.VirusTotal != tc.report {
				t.Fatalf("result = %+v, want %v %v", r, tc.want, tc.findings)
			}
		})
	}
}

func TestEveryReasonToAskIsListed(t *testing.T) {
	in, _ := verifyCase(t, "setup.exe", content)
	in.Platform = &platform.Fake{Result: platform.Signature{State: platform.SignatureUnsigned}}
	in.Ref, in.Checks.Provenance, in.Checks.VirusTotal = tool, true, true
	in.Provenance = provenance(source.Provenance{Found: true, Repository: "evil/tool"}, nil)
	in.VirusTotal = lookup(fetch.VirusTotalReport{Status: fetch.VirusTotalReported, Flagged: 2, Total: 60}, nil)
	r := verify(t, in)
	want := []fetch.Finding{fetch.FindingUnsigned, fetch.FindingNoChecksum, fetch.FindingProvenanceMismatch, fetch.FindingVirusTotal}
	if r.Verdict != fetch.VerdictAsk || !slices.Equal(r.Findings, want) {
		t.Fatalf("result = %+v", r)
	}
}

func TestAnInvalidSignatureIsNotLookedUp(t *testing.T) {
	in, _ := verifyCase(t, "setup.exe", content)
	in.Platform = &platform.Fake{Result: platform.Signature{State: platform.SignatureInvalid}}
	in.Checks.VirusTotal = true
	in.VirusTotal = func(context.Context, string) (fetch.VirusTotalReport, error) {
		t.Error("looked up a file that is already rejected")
		return fetch.VirusTotalReport{}, nil
	}
	if r := verify(t, in); r.Verdict != fetch.VerdictStop {
		t.Fatalf("result = %+v", r)
	}
}

func TestACanceledLookupFailsTheVerification(t *testing.T) {
	in, _ := verifyCase(t, "tool.zip", content)
	in.Checks.VirusTotal = true
	in.VirusTotal = lookup(fetch.VirusTotalReport{}, context.Canceled)
	if _, err := fetch.Verify(t.Context(), in); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// sentRequest is what the test server of VirusTotal was sent.
type sentRequest struct {
	method, path, key string
	body              []byte
}

// virusTotalServer answers every lookup with the given status and body, and
// reports what it was sent.
func virusTotalServer(t *testing.T, status int, body string) (fetch.VirusTotal, <-chan sentRequest) {
	t.Helper()
	sent := make(chan sentRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		sent <- sentRequest{method: r.Method, path: r.URL.Path, key: r.Header.Get("X-Apikey"), body: data}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return fetch.VirusTotal{BaseURL: server.URL, UserAgent: "Release Sync/test"}, sent
}

func TestVirusTotalLookupSendsOnlyTheHash(t *testing.T) {
	vt, sent := virusTotalServer(t, 404, "")
	if _, err := vt.Lookup(t.Context(), "my-key", digest(content)); err != nil {
		t.Fatal(err)
	}
	got := <-sent
	if got.method != http.MethodGet || got.path != "/api/v3/files/"+digest(content) || got.key != "my-key" || len(got.body) != 0 {
		t.Fatalf("sent %+v", got)
	}
}

func TestVirusTotalStatuses(t *testing.T) {
	const stats = `{"data":{"attributes":{"last_analysis_stats":{"malicious":2,"suspicious":1,"harmless":60,"undetected":10,"timeout":3}}}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   fetch.VirusTotalReport
	}{
		{"flagged", 200, stats, fetch.VirusTotalReport{Status: fetch.VirusTotalReported, Flagged: 3, Total: 73}},
		{"clean", 200, `{"data":{"attributes":{"last_analysis_stats":{"harmless":60,"undetected":10}}}}`, fetch.VirusTotalReport{Status: fetch.VirusTotalReported, Total: 70}},
		{"not analysed yet", 200, `{"data":{"attributes":{"last_analysis_stats":{}}}}`, fetch.VirusTotalReport{Status: fetch.VirusTotalNoResults}},
		{"never seen", 404, `{"error":{"code":"NotFoundError"}}`, fetch.VirusTotalReport{Status: fetch.VirusTotalNeverSeen}},
		{"key rejected 401", 401, "", fetch.VirusTotalReport{Status: fetch.VirusTotalKeyRejected}},
		{"key rejected 403", 403, "", fetch.VirusTotalReport{Status: fetch.VirusTotalKeyRejected}},
		{"limit reached", 429, "", fetch.VirusTotalReport{Status: fetch.VirusTotalLimited}},
		{"server error", 503, "", fetch.VirusTotalReport{Status: fetch.VirusTotalUnavailable}},
		{"garbled reply", 200, "not json", fetch.VirusTotalReport{Status: fetch.VirusTotalUnavailable}},
	} {
		vt, _ := virusTotalServer(t, tc.status, tc.body)
		got, err := vt.Lookup(t.Context(), "key", digest(content))
		if err != nil || got != tc.want {
			t.Errorf("%s: report = %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
}

func TestVirusTotalThatCannotBeReachedIsUnavailable(t *testing.T) {
	vt, _ := virusTotalServer(t, 200, "")
	server := httptest.NewServer(http.NotFoundHandler())
	vt.BaseURL = server.URL
	server.Close()
	if got, err := vt.Lookup(t.Context(), "key", digest(content)); err != nil || got.Status != fetch.VirusTotalUnavailable {
		t.Fatalf("report = %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := vt.Lookup(ctx, "key", digest(content)); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled lookup: err = %v", err)
	}
}
