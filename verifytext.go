package main

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/aaronflorey/release-sync/fetch"
	"github.com/aaronflorey/release-sync/locales"
	"github.com/aaronflorey/release-sync/platform"
)

// verifySummary is the one line that says what verification found, for
// example "checksum verified (GitHub) · signed by Example Corp".
func verifySummary(r fetch.Result, checks fetch.Checks) string {
	if checks.None() {
		return locales.VerifyOff()
	}
	var parts []string
	switch r.Scan.State {
	case platform.ScanClean:
		parts = append(parts, locales.VerifyScanClean())
	case platform.ScanUnavailable:
		parts = append(parts, locales.VerifyScanUnavailable())
	case platform.ScanThreat:
	}
	if checks.Checksum {
		if r.ChecksumMatched() {
			parts = append(parts, locales.VerifyChecksumOk(r.ExpectedFrom))
		} else {
			parts = append(parts, locales.VerifyNoChecksum())
		}
	}
	if checks.Signature && r.Executable {
		parts = append(parts, signatureText(r.Signature))
	}
	if text := provenanceText(r); text != "" {
		parts = append(parts, text)
	}
	if text := virusTotalText(r.VirusTotal); text != "" {
		parts = append(parts, text)
	}
	return strings.Join(parts, " · ")
}

// provenanceText says what GitHub's build record shows. A file with no record,
// the normal case, and one built elsewhere, which is a finding to ask about,
// are not part of the summary.
func provenanceText(r fetch.Result) string {
	p := r.Provenance
	switch {
	case !p.Found || r.ProvenanceElsewhere:
		return ""
	case p.Repository == "":
		return locales.VerifyProvenanceHeld()
	case p.Workflow == "":
		return locales.VerifyProvenanceFrom(p.Repository)
	}
	return locales.VerifyProvenanceFromBy(p.Repository, p.Workflow)
}

// virusTotalText says what the lookup on VirusTotal came to.
func virusTotalText(r fetch.VirusTotalReport) string {
	switch r.Status {
	case fetch.VirusTotalReported:
		return locales.VerifyVirusTotalFlagged(r.Flagged, r.Total)
	case fetch.VirusTotalNeverSeen:
		return locales.VerifyVirusTotalNeverSeen()
	case fetch.VirusTotalNoResults:
		return locales.VerifyVirusTotalNoResults()
	case fetch.VirusTotalKeyRejected:
		return locales.VerifyVirusTotalKeyRejected()
	case fetch.VirusTotalLimited:
		return locales.VerifyVirusTotalLimited()
	case fetch.VirusTotalUnavailable:
		return locales.VerifyVirusTotalUnavailable()
	case fetch.VirusTotalNotChecked:
	}
	return ""
}

func signatureText(sig platform.Signature) string {
	switch sig.State {
	case platform.SignatureTrusted:
		if sig.Signer == "" {
			return locales.VerifySigned()
		}
		return locales.VerifySignedBy(sig.Signer)
	case platform.SignatureUnsigned:
		return locales.VerifyUnsigned()
	case platform.SignatureUntrusted:
		if sig.Signer == "" {
			return locales.VerifyUntrusted()
		}
		return locales.VerifyUntrustedBy(sig.Signer)
	case platform.SignatureInvalid, platform.SignatureNotChecked:
	}
	return locales.VerifySignatureNotChecked()
}

// findingText explains one finding of a result to the person deciding what to do.
func findingText(f fetch.Finding, r fetch.Result) string {
	switch f {
	case fetch.FindingChecksumMismatch:
		return locales.VerifyChecksumMismatch()
	case fetch.FindingSignatureInvalid:
		return locales.VerifySignatureInvalid()
	case fetch.FindingUnsigned:
		return locales.FindingUnsigned()
	case fetch.FindingUntrusted:
		return locales.FindingUntrusted()
	case fetch.FindingNoChecksum:
		return locales.FindingNoChecksum()
	case fetch.FindingThreat:
		return threatText(r.Scan)
	case fetch.FindingProvenanceMismatch:
		return locales.FindingProvenanceMismatch(r.Provenance.Repository)
	case fetch.FindingVirusTotal:
		return locales.FindingVirusTotal(r.VirusTotal.Flagged, r.VirusTotal.Total)
	}
	return ""
}

// threatText says what Windows Security found, and that the download was stopped.
func threatText(scan platform.ScanResult) string {
	switch {
	case scan.Removed:
		return locales.FindingThreatRemoved(appName)
	case scan.Threat == "":
		return locales.FindingThreatUnnamed(appName)
	}
	return locales.FindingThreat(scan.Threat, appName)
}

// rejectedText explains a download that verification stopped and deleted.
func rejectedText(r fetch.Result) string {
	switch {
	case len(r.Findings) == 0:
		return locales.VerifyDeleted()
	case r.Findings[0] == fetch.FindingThreat:
		// The text of a threat already says the file was stopped and deleted.
		return findingText(r.Findings[0], r)
	}
	return findingText(r.Findings[0], r) + " " + locales.VerifyDeleted()
}

// askTitle is the title of the question about a download's findings: it names
// the antivirus engines when they are the reason, and the file otherwise.
func askTitle(file string, r fetch.Result) string {
	if slices.Contains(r.Findings, fetch.FindingVirusTotal) {
		return locales.QuestionAntivirusTitle()
	}
	return locales.QuestionVerifyTitle(file)
}

// downloadErrorText says why a download failed, for the person who started it.
func downloadErrorText(err error) string {
	var status *fetch.StatusError
	var disk *fetch.LocalError
	switch {
	case errors.As(err, &disk):
		return locales.DownloadErrorDisk(disk.Error())
	case errors.As(err, &status) && status.Status == http.StatusNotFound:
		return locales.DownloadErrorNotFound()
	case errors.As(err, &status):
		return locales.DownloadErrorStatus(status.Status)
	case errors.Is(err, fetch.ErrSizeMismatch):
		return locales.DownloadErrorSize()
	case errors.Is(err, fetch.ErrStalled):
		return locales.DownloadErrorStalled()
	}
	return locales.DownloadErrorNetwork()
}
