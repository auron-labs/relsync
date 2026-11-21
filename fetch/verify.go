package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/source"
)

// MaxSidecarSize is the largest checksum file that is read.
const MaxSidecarSize = 1 << 20

// Verdict is what the install pipeline does with a verified download.
type Verdict int

// The verdicts.
const (
	// VerdictProceed means nothing stands in the way.
	VerdictProceed Verdict = iota
	// VerdictAsk means the user decides: the findings are shown with a choice
	// to go on. It is only given to an interactive operation.
	VerdictAsk
	// VerdictSkip is VerdictAsk for an unattended operation: nobody can be
	// asked, so the file is left alone.
	VerdictSkip
	// VerdictStop means the file must not be used and is to be deleted.
	VerdictStop
)

// Finding is one thing the checks found that a person should know about.
type Finding int

// The findings. Their order is the order they are listed in.
const (
	// FindingChecksumMismatch is a checksum the publisher posted that the file does not match.
	FindingChecksumMismatch Finding = iota
	// FindingSignatureInvalid is a signature that does not match the file.
	FindingSignatureInvalid
	// FindingUnsigned is an executable with no signature.
	FindingUnsigned
	// FindingUntrusted is an executable whose signature the system does not trust.
	FindingUntrusted
	// FindingNoChecksum is that nothing the publisher posted could back up an unsigned executable.
	FindingNoChecksum
	// FindingThreat is a threat the system's antivirus found, or a file it removed.
	FindingThreat
	// FindingProvenanceMismatch is a build record that names another repository.
	FindingProvenanceMismatch
	// FindingVirusTotal is a file some antivirus engines on VirusTotal flag.
	FindingVirusTotal
)

// Checks says which checks run. Each is governed by its own setting.
type Checks struct {
	Checksum  bool
	Signature bool
	// Scan has the system's antivirus scan the file.
	Scan bool
	// Provenance asks GitHub for a signed build record of the file.
	Provenance bool
	// VirusTotal looks the file's hash up on VirusTotal.
	VirusTotal bool
}

// None reports that no check is switched on.
func (c Checks) None() bool { return c == Checks{} }

// SidecarReader returns the content of a checksum file of the release. An
// error means the file could not be read, which is not a verdict on the
// download.
type SidecarReader func(ctx context.Context, asset source.Asset) ([]byte, error)

// ProvenanceLookup asks for the build records of a file by its SHA-256. A
// failure is not a verdict on the download.
type ProvenanceLookup func(ctx context.Context, sha256 string) (source.Provenance, error)

// Input is a finished download and what is known about it.
type Input struct {
	Path string
	// SHA256 is the file's digest in hex when already known, from the
	// download; empty makes Verify read the file.
	SHA256 string
	Asset  source.Asset
	// Siblings are the other files of the release, where checksum files are looked for.
	Siblings []source.Asset
	// DigestOrigin names the site, for a digest it published.
	DigestOrigin string
	// Sidecar reads a checksum file; nil means none can be read.
	Sidecar SidecarReader
	// Platform checks signatures and scans for threats.
	Platform platform.Platform
	Checks   Checks
	// Ref is the repository the file is from, which a build record is compared with.
	Ref source.RepoRef
	// Provenance asks for the signed build record of a hash; nil means none can be asked.
	Provenance ProvenanceLookup
	// VirusTotal looks a hash up; nil means it cannot be.
	VirusTotal VirusTotalLookup
	// Unattended says nobody can be asked.
	Unattended bool
	// TrustUnsigned is the user's standing decision to accept an unsigned
	// program from this Item. It never excuses a bad checksum or signature.
	TrustUnsigned bool
}

// Result is the outcome of verifying one download. The install pipeline acts
// on Verdict and does not work it out again.
type Result struct {
	SHA256 string
	// Expected is the checksum the publisher posted, empty when none was found.
	Expected string
	// ExpectedFrom is where it came from: the site's name for a digest it
	// publishes, otherwise the checksum file's name.
	ExpectedFrom string
	// Executable is whether the file is a kind that is signature checked.
	Executable bool
	Signature  platform.Signature
	// Scan is what the system's antivirus said; its State is empty when no scan was made.
	Scan platform.ScanResult
	// Provenance is the build record GitHub holds, if it holds one, and
	// ProvenanceElsewhere says it names another repository than the file's own.
	Provenance          source.Provenance
	ProvenanceElsewhere bool
	// VirusTotal is the outcome of the lookup; its Status is VirusTotalNotChecked when none was made.
	VirusTotal VirusTotalReport
	Findings   []Finding
	Verdict    Verdict
}

// ChecksumMatched reports that the publisher posted a checksum and it matches.
func (r Result) ChecksumMatched() bool { return r.Expected != "" && r.Expected == r.SHA256 }

// ChecksumMismatched reports that the publisher posted a checksum and it does not match.
func (r Result) ChecksumMismatched() bool { return r.Expected != "" && r.Expected != r.SHA256 }

// Verify checks a finished download. It fails only when ctx ends or the file
// cannot be read; everything it finds is in the Result.
func Verify(ctx context.Context, in Input) (Result, error) {
	r := Result{SHA256: strings.ToLower(in.SHA256), Executable: isSignable(in.Asset.Name)}
	if in.Checks.Scan && in.Platform != nil {
		scan, err := scanFile(ctx, in)
		if err != nil {
			return Result{}, err
		}
		r.Scan = scan
		// Nothing else is worth checking in a file that is dangerous or already gone.
		if scan.State == platform.ScanThreat {
			r.Findings, r.Verdict = []Finding{FindingThreat}, VerdictStop
			return r, nil
		}
	}
	if r.SHA256 == "" {
		sum, err := HashFile(in.Path)
		if err != nil {
			return Result{}, err
		}
		r.SHA256 = sum
	}
	if in.Checks.Checksum {
		var err error
		if r.Expected, r.ExpectedFrom, err = expectedChecksum(ctx, in); err != nil {
			return Result{}, err
		}
	}
	if r.ChecksumMismatched() {
		r.Findings, r.Verdict = []Finding{FindingChecksumMismatch}, VerdictStop
		return r, nil
	}
	if in.Checks.Signature && r.Executable {
		sig, err := checkSignature(ctx, in)
		if err != nil {
			return Result{}, err
		}
		r.Signature = sig
	}
	if r.Signature.State != platform.SignatureInvalid {
		if err := lookUp(ctx, in, &r); err != nil {
			return Result{}, err
		}
	}
	r.Findings, r.Verdict = judge(r, in)
	return r, nil
}

// judge applies the verdict table to what the checks found, the one place
// that decides what a download comes to. A matching checksum lets an unsigned
// program through without a question, and the person's standing trust of an
// Item's unsigned programs excuses only that; an invalid signature stops a
// download whatever else is true; a build record from another repository or
// engines on VirusTotal that flag the file are asked about.
func judge(r Result, in Input) ([]Finding, Verdict) {
	if r.Signature.State == platform.SignatureInvalid {
		return []Finding{FindingSignatureInvalid}, VerdictStop
	}
	var findings []Finding
	if concern := r.Executable && !r.ChecksumMatched() && !in.TrustUnsigned; concern {
		switch r.Signature.State {
		case platform.SignatureUnsigned:
			findings = append([]Finding{FindingUnsigned}, noChecksum(r)...)
		case platform.SignatureUntrusted:
			findings = append([]Finding{FindingUntrusted}, noChecksum(r)...)
		case platform.SignatureNotChecked, platform.SignatureTrusted, platform.SignatureInvalid:
		}
	}
	if r.ProvenanceElsewhere {
		findings = append(findings, FindingProvenanceMismatch)
	}
	if r.VirusTotal.IsFlagged() {
		findings = append(findings, FindingVirusTotal)
	}
	if len(findings) == 0 {
		return nil, VerdictProceed
	}
	return findings, askOrSkip(in)
}

func noChecksum(r Result) []Finding {
	if r.Expected == "" {
		return []Finding{FindingNoChecksum}
	}
	return nil
}

func askOrSkip(in Input) Verdict {
	if in.Unattended {
		return VerdictSkip
	}
	return VerdictAsk
}

// scanFile asks the platform to scan the download. A scan that cannot be made
// is the same as no answer: it is noted and never blocks.
func scanFile(ctx context.Context, in Input) (platform.ScanResult, error) {
	scan, err := in.Platform.Scan(ctx, in.Path)
	if err != nil {
		if ctx.Err() != nil {
			return platform.ScanResult{}, fmt.Errorf("scan %s: %w", in.Asset.Name, ctx.Err())
		}
		log.Printf("scan %s: %v", in.Asset.Name, err)
		return platform.ScanResult{State: platform.ScanUnavailable}, nil
	}
	return scan, nil
}

// lookUp asks the outside services about the file's hash: GitHub for a build
// record and VirusTotal for the verdicts of antivirus engines. A service that
// does not answer leaves its part of the Result empty, so the download is not
// held up by it.
func lookUp(ctx context.Context, in Input, r *Result) error {
	if in.Checks.Provenance && in.Provenance != nil {
		p, err := in.Provenance(ctx, r.SHA256)
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("look up the build record of %s: %w", in.Asset.Name, ctx.Err())
		case err != nil:
			log.Printf("look up the build record of %s: %v", in.Asset.Name, err)
		default:
			r.Provenance, r.ProvenanceElsewhere = p, p.BuiltElsewhere(in.Ref)
		}
	}
	if in.Checks.VirusTotal && in.VirusTotal != nil {
		report, err := in.VirusTotal(ctx, r.SHA256)
		if err != nil {
			return fmt.Errorf("look up %s on VirusTotal: %w", in.Asset.Name, err)
		}
		r.VirusTotal = report
	}
	return nil
}

// checkSignature asks the platform. A platform that fails to answer is the
// same as one that cannot check: the signature is not checked, never trusted.
func checkSignature(ctx context.Context, in Input) (platform.Signature, error) {
	if in.Platform == nil {
		return platform.Signature{State: platform.SignatureNotChecked}, nil
	}
	sig, err := in.Platform.CheckSignature(ctx, in.Path)
	if err != nil {
		if ctx.Err() != nil {
			return platform.Signature{}, fmt.Errorf("check signature: %w", ctx.Err())
		}
		log.Printf("check signature of %s: %v", in.Asset.Name, err)
		return platform.Signature{State: platform.SignatureNotChecked}, nil
	}
	return sig, nil
}

// expectedChecksum finds the checksum the publisher posted for the asset: the
// digest the site publishes, else one from a checksum file of the release.
// A checksum file that cannot be read is skipped; the answer is then "none".
func expectedChecksum(ctx context.Context, in Input) (digest, from string, err error) {
	if d, ok := siteDigest(in.Asset); ok {
		return d, in.DigestOrigin, nil
	}
	if in.Sidecar == nil {
		return "", "", nil
	}
	for _, side := range sidecarCandidates(in.Asset.Name, in.Siblings) {
		if side.Size >= MaxSidecarSize {
			continue
		}
		data, readErr := in.Sidecar(ctx, side)
		if ctx.Err() != nil {
			return "", "", fmt.Errorf("read %s: %w", side.Name, ctx.Err())
		}
		if readErr != nil {
			log.Printf("read checksum file %s: %v", side.Name, readErr)
			continue
		}
		if d, ok := findDigest(string(data), in.Asset.Name, side.Name); ok {
			return d, side.Name, nil
		}
	}
	return "", "", nil
}

// siteDigest is the SHA-256 the site publishes for an asset, "sha256:" and 64 hex digits.
func siteDigest(a source.Asset) (string, bool) {
	value, ok := strings.CutPrefix(strings.ToLower(a.Digest), "sha256:")
	if !ok || len(value) != sha256.Size*2 {
		return "", false
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", false
	}
	return value, true
}

// isSignable reports the file kinds whose code signature is checked:
// executables, installers and MSIX packages. Archives never are.
func isSignable(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range []string{".exe", ".msi", ".msix", ".msixbundle", ".appx", ".appxbundle"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// HashFile returns the SHA-256 of the file in lower-case hex.
func HashFile(path string) (sum string, err error) {
	f, err := os.Open(path) // #nosec G304 -- the caller names the file it downloaded.
	if err != nil {
		return "", local("open "+path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", local("read "+path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
