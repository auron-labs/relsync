package source

import (
	"fmt"
	"time"
)

// ErrorKind says what went wrong with a request.
type ErrorKind int

// The kinds of request failure.
const (
	// KindNotFound is a missing repository, or a private one the token cannot see.
	KindNotFound ErrorKind = iota
	// KindTokenRejected is a token the site refused.
	KindTokenRejected
	// KindRateLimited is an exhausted request allowance.
	KindRateLimited
	// KindUnreachable is a network failure.
	KindUnreachable
	// KindTimedOut is a request that took too long.
	KindTimedOut
	// KindStatus is any other unsuccessful HTTP status.
	KindStatus
	// KindNoProject is an address on a host that neither Forgejo nor GitLab
	// has a project at.
	KindNoProject
)

// Error is a failed request, with what a caller needs to explain it.
type Error struct {
	Kind ErrorKind
	// Status is the HTTP status, 0 when none arrived.
	Status int
	// Site names the site that was asked, as SiteLabel does; empty is GitHub.
	Site string
	// Resource is what was asked for, such as owner/repo.
	Resource string
	// ResetAt is when a rate limit lifts, zero when unknown.
	ResetAt time.Time
	// Authenticated reports whether the request carried a token.
	Authenticated bool
	// Cause is the underlying transport error, if any.
	Cause error
}

func (e *Error) Error() string {
	var what string
	switch e.Kind {
	case KindNotFound:
		what = "not found"
	case KindTokenRejected:
		what = "token rejected"
	case KindRateLimited:
		what = "rate limited"
		if !e.ResetAt.IsZero() {
			what += " until " + e.ResetAt.UTC().Format(time.RFC3339)
		}
	case KindUnreachable:
		what = "unreachable"
	case KindTimedOut:
		what = "timed out"
	case KindStatus:
		what = fmt.Sprintf("HTTP %d", e.Status)
	case KindNoProject:
		what = "no project"
	}
	site := "github"
	if e.Site != "" {
		site = e.Site
	}
	msg := site + ": " + what
	if e.Resource != "" {
		msg += " (" + e.Resource + ")"
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Cause }
