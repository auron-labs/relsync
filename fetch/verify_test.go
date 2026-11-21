package fetch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"
)

// release is a release's files and the checksum files' content, as a fake
// Source supplies them.
type release struct {
	assets   []source.Asset
	sidecars map[string]string
	// reads records the checksum files that were fetched, in order.
	reads []string
}

func (r *release) read(_ context.Context, a source.Asset) ([]byte, error) {
	r.reads = append(r.reads, a.Name)
	text, ok := r.sidecars[a.Name]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(text), nil
}

// verifyCase builds an input for a downloaded file with the given name and content.
func verifyCase(t *testing.T, name string, content []byte, siblings ...string) (fetch.Input, *release) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	rel := &release{sidecars: map[string]string{}}
	rel.assets = append(rel.assets, source.Asset{Name: name, Size: int64(len(content))})
	for _, s := range siblings {
		rel.assets = append(rel.assets, source.Asset{Name: s, Size: 100})
	}
	return fetch.Input{
		Path: path, Asset: source.Asset{Name: name, Size: int64(len(content))}, Siblings: rel.assets,
		DigestOrigin: "GitHub", Sidecar: rel.read, Platform: &platform.Fake{Result: platform.Signature{State: platform.SignatureNotChecked}},
		Checks: fetch.Checks{Checksum: true, Signature: true},
	}, rel
}

func verify(t *testing.T, in fetch.Input) fetch.Result {
	t.Helper()
	r, err := fetch.Verify(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var content = []byte("the file")

func TestASiteDigestThatMatchesIsVerified(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content, "checksums.txt")
	in.Asset.Digest = "sha256:" + strings.ToUpper(digest(content))
	r := verify(t, in)
	if !r.ChecksumMatched() || r.ExpectedFrom != "GitHub" || r.Verdict != fetch.VerdictProceed || r.SHA256 != digest(content) {
		t.Fatalf("result = %+v", r)
	}
	if len(rel.reads) != 0 {
		t.Errorf("read checksum files %v although the site published a digest", rel.reads)
	}
}

func TestASiteDigestThatDiffersStopsTheDownload(t *testing.T) {
	in, _ := verifyCase(t, "tool.exe", content)
	in.Asset.Digest = "sha256:" + digest([]byte("something else"))
	// The signature is fine; the checksum alone decides.
	in.Platform = &platform.Fake{Result: platform.Signature{State: platform.SignatureTrusted, Signer: "Example Corp"}}
	r := verify(t, in)
	if !r.ChecksumMismatched() || r.Verdict != fetch.VerdictStop || !slices.Equal(r.Findings, []fetch.Finding{fetch.FindingChecksumMismatch}) {
		t.Fatalf("result = %+v", r)
	}
	if got := in.Platform.(*platform.Fake).Checked(); len(got) != 0 {
		t.Errorf("checked the signature of a file already rejected: %v", got)
	}
}

func TestAnUnusableSiteDigestIsIgnored(t *testing.T) {
	for _, d := range []string{"", "sha512:abcd", "sha256:short", "sha256:" + strings.Repeat("z", 64)} {
		in, _ := verifyCase(t, "tool.zip", content)
		in.Asset.Digest = d
		if r := verify(t, in); r.Expected != "" || r.Verdict != fetch.VerdictProceed {
			t.Errorf("digest %q: result = %+v", d, r)
		}
	}
}

func TestEachSidecarNameFormIsFound(t *testing.T) {
	for _, name := range []string{"tool.zip.sha256", "tool.zip.sha256sum", "tool.zip.sha256.txt", "tool.zip.sha-256", "TOOL.ZIP.SHA256"} {
		in, rel := verifyCase(t, "tool.zip", content, name)
		rel.sidecars[name] = digest(content) + "\n"
		r := verify(t, in)
		if !r.ChecksumMatched() || r.ExpectedFrom != name {
			t.Errorf("%s: result = %+v", name, r)
		}
	}
}

func TestSidecarsNamedAfterTheAssetAreTriedFirstInOrder(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content, "checksums.txt", "tool.zip.sha256sum", "tool.zip.sha256")
	rel.sidecars["tool.zip.sha256"] = "not a checksum"
	rel.sidecars["tool.zip.sha256sum"] = "not a checksum either"
	rel.sidecars["checksums.txt"] = digest(content) + "  tool.zip\n"
	r := verify(t, in)
	if r.ExpectedFrom != "checksums.txt" {
		t.Fatalf("result = %+v", r)
	}
	if want := []string{"tool.zip.sha256", "tool.zip.sha256sum", "checksums.txt"}; !slices.Equal(rel.reads, want) {
		t.Errorf("reads = %v, want %v", rel.reads, want)
	}
}

func TestAnyChecksumListNameIsConsidered(t *testing.T) {
	for _, name := range []string{"SHA256SUMS", "tool-1.0-sha-256.txt", "checksums.txt", "CHECKSUM", "tool_checksums.sha256"} {
		in, rel := verifyCase(t, "tool.zip", content, name)
		rel.sidecars[name] = digest(content) + "  tool.zip\n"
		if r := verify(t, in); r.ExpectedFrom != name {
			t.Errorf("%s: result = %+v", name, r)
		}
	}
}

func TestASignatureFileIsNotAChecksumList(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content, "checksums.txt.sig", "checksums.txt.asc", "checksums.txt.minisig", "checksums.txt.sigstore.json", "checksums.pem")
	for name := range map[string]bool{"checksums.txt.sig": true, "checksums.txt.asc": true, "checksums.txt.minisig": true, "checksums.txt.sigstore.json": true, "checksums.pem": true} {
		rel.sidecars[name] = digest(content) + "  tool.zip\n"
	}
	r := verify(t, in)
	if r.Expected != "" || len(rel.reads) != 0 {
		t.Fatalf("result = %+v, reads = %v", r, rel.reads)
	}
}

func TestChecksumListFormats(t *testing.T) {
	sum := digest(content)
	other := digest([]byte("other"))
	for name, text := range map[string]string{
		"sha256sum text mode":   other + "  other.zip\n" + sum + "  tool.zip\n",
		"sha256sum binary mode": sum + " *tool.zip\n",
		"BSD style":             "SHA256 (other.zip) = " + other + "\nSHA256 (tool.zip) = " + sum + "\n",
		"uppercase digest":      strings.ToUpper(sum) + "  tool.zip\n",
		"folder in the name":    sum + "  ./dist/tool.zip\n",
		"windows folder":        sum + "  dist\\tool.zip\r\n",
	} {
		in, rel := verifyCase(t, "tool.zip", content, "checksums.txt")
		rel.sidecars["checksums.txt"] = text
		if r := verify(t, in); !r.ChecksumMatched() {
			t.Errorf("%s: result = %+v", name, r)
		}
	}
	in, rel := verifyCase(t, "my tool (1).zip", content, "checksums.txt")
	rel.sidecars["checksums.txt"] = "SHA256 (my tool (1).zip) = " + sum + "\n"
	if r := verify(t, in); !r.ChecksumMatched() {
		t.Errorf("BSD style with parentheses in the name: result = %+v", r)
	}
}

func TestAListThatDoesNotNameTheAssetProvidesNothing(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content, "checksums.txt")
	rel.sidecars["checksums.txt"] = digest(content) + "  other.zip\n" + digest(content) + "\n"
	if r := verify(t, in); r.Expected != "" {
		t.Fatalf("a hash for another file was taken: %+v", r)
	}
}

func TestASingleHashInAFileNamedAfterTheAssetCounts(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content, "tool.zip.sha256")
	rel.sidecars["tool.zip.sha256"] = "  " + strings.ToUpper(digest(content)) + "\r\n"
	if r := verify(t, in); !r.ChecksumMatched() {
		t.Fatalf("result = %+v", r)
	}
}

func TestAMismatchInAChecksumFileStopsTheDownload(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content, "checksums.txt")
	rel.sidecars["checksums.txt"] = digest([]byte("tampered")) + "  tool.zip\n"
	if r := verify(t, in); r.Verdict != fetch.VerdictStop || !r.ChecksumMismatched() {
		t.Fatalf("result = %+v", r)
	}
}

func TestChecksumFilesThatCannotBeReadAreSkipped(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content, "tool.zip.sha256", "checksums.txt")
	rel.sidecars["checksums.txt"] = digest(content) + "  tool.zip\n" // the first one fails to read
	r := verify(t, in)
	if !r.ChecksumMatched() || r.ExpectedFrom != "checksums.txt" {
		t.Fatalf("result = %+v", r)
	}
	in, _ = verifyCase(t, "tool.zip", content, "checksums.txt")
	if r := verify(t, in); r.Expected != "" || r.Verdict != fetch.VerdictProceed {
		t.Fatalf("with nothing readable: %+v", r)
	}
}

func TestAChecksumFileOfAMegabyteOrMoreIsNotRead(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content)
	in.Siblings = append(in.Siblings, source.Asset{Name: "checksums.txt", Size: fetch.MaxSidecarSize})
	rel.sidecars["checksums.txt"] = digest(content) + "  tool.zip\n"
	if r := verify(t, in); r.Expected != "" || len(rel.reads) != 0 {
		t.Fatalf("result = %+v, reads = %v", r, rel.reads)
	}
}

func TestCancellingWhileReadingAChecksumFileFailsVerification(t *testing.T) {
	in, _ := verifyCase(t, "tool.zip", content, "checksums.txt")
	ctx, cancel := context.WithCancel(t.Context())
	in.Sidecar = func(context.Context, source.Asset) ([]byte, error) {
		cancel()
		return nil, errors.New("interrupted")
	}
	if _, err := fetch.Verify(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestChecksumCheckingCanBeTurnedOff(t *testing.T) {
	in, rel := verifyCase(t, "tool.zip", content, "checksums.txt")
	in.Asset.Digest = "sha256:" + digest([]byte("different"))
	rel.sidecars["checksums.txt"] = digest([]byte("different")) + "  tool.zip\n"
	in.Checks.Checksum = false
	if r := verify(t, in); r.Expected != "" || r.Verdict != fetch.VerdictProceed || len(rel.reads) != 0 {
		t.Fatalf("result = %+v", r)
	}
}

func TestADigestFromTheDownloadIsUsedAndOtherwiseTheFileIsRead(t *testing.T) {
	in, _ := verifyCase(t, "tool.zip", content)
	in.SHA256 = strings.ToUpper(digest(content))
	in.Path = filepath.Join(t.TempDir(), "never-read")
	if r := verify(t, in); r.SHA256 != digest(content) {
		t.Errorf("SHA256 = %s", r.SHA256)
	}
	in, _ = verifyCase(t, "tool.zip", content)
	if r := verify(t, in); r.SHA256 != digest(content) {
		t.Errorf("SHA256 = %s", r.SHA256)
	}
	in.Path = filepath.Join(t.TempDir(), "missing")
	if _, err := fetch.Verify(t.Context(), in); err == nil {
		t.Error("a missing file verified")
	}
}

// signedCase is one row of the verdict table.
type signedCase struct {
	name      string
	file      string
	signature platform.Signature
	// checksum is "match" when the publisher posted a matching checksum.
	checksum   string
	unattended bool
	trust      bool
	want       fetch.Verdict
	findings   []fetch.Finding
}

func TestTheVerdictTable(t *testing.T) {
	unsigned := platform.Signature{State: platform.SignatureUnsigned}
	untrusted := platform.Signature{State: platform.SignatureUntrusted, Signer: "Unknown Ltd"}
	trusted := platform.Signature{State: platform.SignatureTrusted, Signer: "Example Corp"}
	invalid := platform.Signature{State: platform.SignatureInvalid, Signer: "Example Corp"}
	for _, tc := range []signedCase{
		{name: "trusted executable proceeds", file: "setup.exe", signature: trusted, want: fetch.VerdictProceed},
		{name: "invalid signature stops", file: "setup.exe", signature: invalid, want: fetch.VerdictStop, findings: []fetch.Finding{fetch.FindingSignatureInvalid}},
		{name: "invalid signature stops unattended", file: "setup.msi", signature: invalid, unattended: true, want: fetch.VerdictStop, findings: []fetch.Finding{fetch.FindingSignatureInvalid}},
		{name: "invalid signature stops even with a matching checksum", file: "setup.exe", signature: invalid, checksum: "match", want: fetch.VerdictStop, findings: []fetch.Finding{fetch.FindingSignatureInvalid}},
		{name: "invalid signature is not excused by trust", file: "setup.exe", signature: invalid, trust: true, want: fetch.VerdictStop, findings: []fetch.Finding{fetch.FindingSignatureInvalid}},
		{name: "unsigned with no checksum asks", file: "setup.exe", signature: unsigned, want: fetch.VerdictAsk, findings: []fetch.Finding{fetch.FindingUnsigned, fetch.FindingNoChecksum}},
		{name: "untrusted with no checksum asks", file: "setup.exe", signature: untrusted, want: fetch.VerdictAsk, findings: []fetch.Finding{fetch.FindingUntrusted, fetch.FindingNoChecksum}},
		{name: "unsigned unattended skips", file: "setup.exe", signature: unsigned, unattended: true, want: fetch.VerdictSkip, findings: []fetch.Finding{fetch.FindingUnsigned, fetch.FindingNoChecksum}},
		{name: "unsigned msix asks", file: "app.msix", signature: unsigned, want: fetch.VerdictAsk, findings: []fetch.Finding{fetch.FindingUnsigned, fetch.FindingNoChecksum}},
		{name: "a matching checksum lets an unsigned executable through", file: "setup.exe", signature: unsigned, checksum: "match", want: fetch.VerdictProceed},
		{name: "a matching checksum lets an untrusted one through", file: "setup.exe", signature: untrusted, checksum: "match", unattended: true, want: fetch.VerdictProceed},
		{name: "trust silences the unsigned question", file: "setup.exe", signature: unsigned, trust: true, want: fetch.VerdictProceed},
		{name: "trust silences the untrusted question unattended", file: "setup.exe", signature: untrusted, trust: true, unattended: true, want: fetch.VerdictProceed},
		{name: "archives are never asked about signatures", file: "tool.zip", signature: unsigned, want: fetch.VerdictProceed},
		{name: "not checked never blocks", file: "setup.exe", signature: platform.Signature{State: platform.SignatureNotChecked}, want: fetch.VerdictProceed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, _ := verifyCase(t, tc.file, content)
			fake := &platform.Fake{Result: tc.signature}
			in.Platform, in.Unattended, in.TrustUnsigned = fake, tc.unattended, tc.trust
			if tc.checksum == "match" {
				in.Asset.Digest = "sha256:" + digest(content)
			}
			r := verify(t, in)
			if r.Verdict != tc.want || !slices.Equal(r.Findings, tc.findings) {
				t.Fatalf("verdict = %v findings = %v, want %v %v", r.Verdict, r.Findings, tc.want, tc.findings)
			}
			asked := len(fake.Checked()) > 0
			if wantAsked := strings.HasSuffix(tc.file, ".exe") || strings.HasSuffix(tc.file, ".msi") || strings.HasSuffix(tc.file, ".msix"); asked != wantAsked {
				t.Errorf("signature checked = %v for %s", asked, tc.file)
			}
		})
	}
}

func TestAnUnsignedExecutableWithAChecksumFileThatDoesNotListItStillAsks(t *testing.T) {
	in, rel := verifyCase(t, "setup.exe", content, "checksums.txt")
	rel.sidecars["checksums.txt"] = digest(content) + "  other.exe\n"
	in.Platform = &platform.Fake{Result: platform.Signature{State: platform.SignatureUnsigned}}
	r := verify(t, in)
	if r.Verdict != fetch.VerdictAsk || !slices.Contains(r.Findings, fetch.FindingNoChecksum) {
		t.Fatalf("result = %+v", r)
	}
}

func TestSignatureCheckingCanBeTurnedOffSeparately(t *testing.T) {
	in, _ := verifyCase(t, "setup.exe", content)
	fake := &platform.Fake{Result: platform.Signature{State: platform.SignatureInvalid}}
	in.Platform, in.Checks = fake, fetch.Checks{Checksum: true}
	if r := verify(t, in); r.Verdict != fetch.VerdictProceed || len(fake.Checked()) != 0 {
		t.Fatalf("result = %+v", r)
	}
}

func TestAPlatformThatCannotAnswerNeverBlocks(t *testing.T) {
	in, _ := verifyCase(t, "setup.exe", content)
	in.Platform = &platform.Fake{Err: errors.New("powershell is missing")}
	r := verify(t, in)
	if r.Verdict != fetch.VerdictProceed || r.Signature.State != platform.SignatureNotChecked {
		t.Fatalf("result = %+v", r)
	}
}

func TestDownloadPathsAreSafe(t *testing.T) {
	root := "downloads"
	for _, tc := range []struct {
		owner, repo, tag, name string
		want                   string
	}{
		{"acme", "tool", "v1.0.0", "tool.zip", filepath.Join(root, "acme-tool", "v1.0.0", "tool.zip")},
		{"acme", "tool", "release/v1", "tool.zip", filepath.Join(root, "acme-tool", "release_v1", "tool.zip")},
		{"acme", "tool", "..", "..", filepath.Join(root, "acme-tool", "_", "_")},
		{"acme", "tool", "v1", `..\..\evil.exe`, filepath.Join(root, "acme-tool", "v1", ".._.._evil.exe")},
		{"acme", "tool", "v1:rc?", "a<b>.zip", filepath.Join(root, "acme-tool", "v1_rc_", "a_b_.zip")},
		{"acme", "tool", "v1", "name. ", filepath.Join(root, "acme-tool", "v1", "name")},
	} {
		if got := fetch.DestPath(root, tc.owner, tc.repo, tc.tag, tc.name); got != tc.want {
			t.Errorf("DestPath(%q, %q) = %s, want %s", tc.tag, tc.name, got, tc.want)
		}
	}
}
