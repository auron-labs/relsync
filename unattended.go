package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/aaronflorey/release-sync/activity"
	"github.com/aaronflorey/release-sync/background"
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/locales"
)

// needsUserError is what an unattended operation returns instead of asking: it
// reached something only a person can decide, so the Item is left for them.
type needsUserError struct {
	reason background.Reason
	// detail says more, as the findings of a verification.
	detail string
}

func (e *needsUserError) Error() string {
	if e.detail != "" {
		return fmt.Sprintf("an unattended update needs a person: %s: %s", e.reason, e.detail)
	}
	return "an unattended update needs a person: " + e.reason.String()
}

// refusal is the error an unattended operation returns in place of asking q.
func (q question) refusal() error {
	reason := q.unattended
	if reason == background.Eligible {
		reason = background.NeedsAttention
	}
	return &needsUserError{reason: reason, detail: strings.Join(q.warnings, "; ")}
}

// isUnattended reports whether the operation on the Item runs with nobody watching.
func (s *screen) isUnattended(id int64) bool {
	op, ok := s.opOf(id)
	return ok && op.unattended
}

// operationLeft ends an unattended operation that stopped for the person to
// decide. It leaves the Item as it was, with the reason in Activity; a running
// program is retried at the next check and is not recorded each time.
func (s *screen) operationLeft(ctx context.Context, e catalog.Entry, rq installRequest, needs *needsUserError) {
	op, ok := s.takeOp(rq.itemID)
	if !ok {
		return
	}
	log.Printf("%s %s was not installed unattended: %v", e.FullName(), rq.tag, needs)
	result := outcomeRetry
	if needs.reason.Announce() {
		result = outcomeLeft
		s.record(context.WithoutCancel(ctx), activity.Warning, rq.itemID, notUpdatedText(e, needs.reason, needs.detail))
	}
	if op.finished != nil {
		op.finished(result)
	}
}

// notUpdatedText is the Activity entry for an Item that was not updated
// automatically, empty for a reason that is not worth recording.
func notUpdatedText(e catalog.Entry, reason background.Reason, detail string) string {
	name := e.FullName()
	switch reason {
	case background.NotManaged:
		return locales.ActivityAutoNotManaged(name, appName)
	case background.LinkUnconfirmed:
		return locales.ActivityAutoLink(name)
	case background.NeedsAttention:
		return locales.ActivityAutoAttention(name)
	case background.NeedsElevation:
		return locales.ActivityAutoElevation(name)
	case background.NeedsInterface:
		return locales.ActivityAutoInterface(name)
	case background.LicenseQuestion:
		return locales.ActivityAutoLicense(name)
	case background.VerificationFindings:
		return locales.ActivityAutoVerify(name, detail)
	case background.Eligible, background.NotNewer, background.ProgramRunning:
	}
	return ""
}
