package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// ScanState is the outcome of a scan with the system's antivirus.
type ScanState string

// The scan states. The zero value means no scan was made.
const (
	// ScanClean is a scan that found nothing.
	ScanClean ScanState = "clean"
	// ScanThreat is a scan that found a threat, or a file that was removed
	// while it was scanned.
	ScanThreat ScanState = "threat"
	// ScanUnavailable is a scan that could not be made: no scanner, a scan
	// that timed out or one that failed. It never blocks anything.
	ScanUnavailable ScanState = "unavailable"
)

// ScanResult is what the system's antivirus said about a file.
type ScanResult struct {
	State ScanState
	// Threat names the threat; empty when the scanner only counted them.
	Threat string
	// Removed says the file was gone when the scan ended: the antivirus took
	// it as soon as it was written, which is a finding in itself.
	Removed bool
}

var (
	// threatName is the "Threat : name" line of a scan's list of detections.
	threatName = regexp.MustCompile(`(?m)^\s*Threat\s*:\s*([^\r\n]+)`)
	// threatCount is the summary line of a scan that found something. It is
	// anchored to the end of the line so a file named "found 1 threats.exe"
	// does not look like a detection.
	threatCount = regexp.MustCompile(`(?im)found\s+[1-9]\d*\s+threats?\.?\s*$`)
	// failureCode is the HRESULT a failed scan reports.
	failureCode = regexp.MustCompile(`(?i)\b0x8[0-9a-f]{7}\b`)
)

// exitFoundOrFailed is the exit code of Windows' scanner for both a detection
// and a failed scan.
const exitFoundOrFailed = 2

// ParseScan reads the exit code and output of Windows' command-line scanner.
// Exit code 2 means a detection but also any failed scan, such as when another
// antivirus has replaced Windows Security, so it cannot be trusted alone: a
// failure always carries an error code, and any other output with that exit
// code may be a detection in a language the patterns do not read, so it still
// counts.
func ParseScan(exitCode int, output string) ScanResult {
	if m := threatName.FindStringSubmatch(output); m != nil {
		return ScanResult{State: ScanThreat, Threat: strings.TrimSpace(m[1])}
	}
	if threatCount.MatchString(output) {
		return ScanResult{State: ScanThreat}
	}
	switch {
	case exitCode == 0:
		return ScanResult{State: ScanClean}
	case exitCode == exitFoundOrFailed && strings.TrimSpace(output) != "" && !failureCode.MatchString(output):
		return ScanResult{State: ScanThreat}
	}
	return ScanResult{State: ScanUnavailable}
}

// scanner is the system's command-line antivirus.
type scanner struct {
	// locate finds the scanner's program, false when the system has none.
	locate func() (string, bool)
	// run scans path with the program and returns its exit code and output.
	run func(ctx context.Context, program, path string) (exitCode int, output string, err error)
	// timeout bounds one scan.
	timeout time.Duration
}

// scan runs the scanner on a file. A file that is gone before or after the
// scan was removed by the antivirus. A scanner that is missing, fails or runs
// out of time gives ScanUnavailable. Only the caller's own cancellation is an
// error.
func (s scanner) scan(ctx context.Context, path string) (ScanResult, error) {
	if gone(path) {
		return ScanResult{State: ScanThreat, Removed: true}, nil
	}
	program, ok := s.locate()
	if !ok {
		return ScanResult{State: ScanUnavailable}, nil
	}
	scanCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	exitCode, output, err := s.run(scanCtx, program, path)
	if err := ctx.Err(); err != nil {
		return ScanResult{}, fmt.Errorf("scan %s: %w", path, err)
	}
	if err != nil {
		// A scanner that fails is "couldn't scan", never a verdict.
		return ScanResult{State: ScanUnavailable}, nil
	}
	if gone(path) {
		return ScanResult{State: ScanThreat, Removed: true}, nil
	}
	return ParseScan(exitCode, output), nil
}

func gone(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

// newerScannerDir orders the version-named folders Windows keeps its scanner
// in, as -1, 0 or 1, comparing the numbers in the names so 4.18.9 is older
// than 4.18.24.
func newerScannerDir(a, b string) int {
	numbers := func(name string) []int {
		var out []int
		for part := range strings.FieldsFuncSeq(name, func(r rune) bool { return r < '0' || r > '9' }) {
			n := 0
			for _, digit := range part {
				n = n*10 + int(digit-'0')
			}
			out = append(out, n)
		}
		return out
	}
	x, y := numbers(a), numbers(b)
	for i := range min(len(x), len(y)) {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	return len(x) - len(y)
}
