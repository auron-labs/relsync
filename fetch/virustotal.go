package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

const (
	defaultVirusTotalURL = "https://www.virustotal.com"
	virusTotalTimeout    = 20 * time.Second
	// maxVirusTotalReply bounds what one report may make the client read.
	maxVirusTotalReply = 4 << 20
)

// VirusTotalStatus is what a VirusTotal lookup came to.
type VirusTotalStatus int

// The statuses. Every one but VirusTotalReported is a note, never a block.
const (
	// VirusTotalNotChecked is the zero value: no lookup was made.
	VirusTotalNotChecked VirusTotalStatus = iota
	// VirusTotalReported is a report with the engines' verdicts.
	VirusTotalReported
	// VirusTotalNeverSeen is a file VirusTotal has no record of, as a new release often is.
	VirusTotalNeverSeen
	// VirusTotalNoResults is a file VirusTotal knows but has not yet analysed.
	VirusTotalNoResults
	// VirusTotalKeyRejected is an API key VirusTotal does not accept.
	VirusTotalKeyRejected
	// VirusTotalLimited is a spent request allowance.
	VirusTotalLimited
	// VirusTotalUnavailable is any other failure to get an answer.
	VirusTotalUnavailable
)

// VirusTotalReport is the outcome of looking a file's hash up on VirusTotal.
type VirusTotalReport struct {
	Status VirusTotalStatus
	// Flagged is how many engines call the file malicious or suspicious, and
	// Total how many gave a verdict.
	Flagged, Total int
}

// IsFlagged reports a file some engine flags.
func (r VirusTotalReport) IsFlagged() bool { return r.Status == VirusTotalReported && r.Flagged > 0 }

// VirusTotalLookup looks a SHA-256 up. Only the hash is ever sent. An error
// means the caller gave up; every other failure is a status.
type VirusTotalLookup func(ctx context.Context, sha256 string) (VirusTotalReport, error)

// VirusTotal is a client of VirusTotal's file lookup.
type VirusTotal struct {
	// BaseURL defaults to VirusTotal's own; tests point it at a local server.
	BaseURL string
	// UserAgent identifies the application.
	UserAgent string
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
}

// Lookup asks VirusTotal about the file with the digest, using the person's
// own API key.
func (v VirusTotal) Lookup(ctx context.Context, apiKey, sha256 string) (VirusTotalReport, error) {
	ctx, cancel := context.WithTimeout(ctx, virusTotalTimeout)
	defer cancel()
	base := v.BaseURL
	if base == "" {
		base = defaultVirusTotalURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v3/files/"+url.PathEscape(sha256), nil)
	if err != nil {
		return VirusTotalReport{}, fmt.Errorf("build the VirusTotal request: %w", err)
	}
	req.Header.Set("X-Apikey", apiKey)
	req.Header.Set("Accept", "application/json")
	if v.UserAgent != "" {
		req.Header.Set("User-Agent", v.UserAgent)
	}
	client := v.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return unavailable(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return VirusTotalReport{Status: VirusTotalNeverSeen}, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return VirusTotalReport{Status: VirusTotalKeyRejected}, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return VirusTotalReport{Status: VirusTotalLimited}, nil
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return VirusTotalReport{Status: VirusTotalUnavailable}, nil
	}
	var body struct {
		Data struct {
			Attributes struct {
				Stats struct {
					Malicious  int `json:"malicious"`
					Suspicious int `json:"suspicious"`
					Harmless   int `json:"harmless"`
					Undetected int `json:"undetected"`
				} `json:"last_analysis_stats"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxVirusTotalReply)).Decode(&body); err != nil {
		return unavailable(ctx, err)
	}
	stats := body.Data.Attributes.Stats
	report := VirusTotalReport{
		Status: VirusTotalReported, Flagged: stats.Malicious + stats.Suspicious,
		Total: stats.Malicious + stats.Suspicious + stats.Harmless + stats.Undetected,
	}
	if report.Total == 0 {
		report.Status = VirusTotalNoResults
	}
	return report, nil
}

// unavailable is the report for a request that failed, or the caller's error
// when the caller is the reason it did.
func unavailable(ctx context.Context, err error) (VirusTotalReport, error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return VirusTotalReport{}, fmt.Errorf("look up on VirusTotal: %w", ctx.Err())
	}
	log.Printf("look up on VirusTotal: %v", err)
	return VirusTotalReport{Status: VirusTotalUnavailable}, nil
}
