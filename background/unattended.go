package background

import (
	"github.com/aaronflorey/release-sync/catalog"
	"github.com/aaronflorey/release-sync/install"
	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
)

// Reason is why an update is not installed with nobody watching. Eligible is
// the absence of one.
type Reason int

// The reasons, in the order Judge looks for them.
const (
	Eligible Reason = iota
	// NotManaged is an Item this application did not install, so there is no
	// install of its own to update.
	NotManaged
	// LinkUnconfirmed is a repository matched by name: an update from it could
	// be a different project's.
	LinkUnconfirmed
	// NotNewer is a release that is not newer than the installed one.
	NotNewer
	// NeedsAttention is a file that cannot be chosen without the person: the
	// stored choice matches nothing, or several files match equally.
	NeedsAttention
	// NeedsElevation is an install that would raise the administrator prompt.
	NeedsElevation
	// ProgramRunning is an app that would have to be closed.
	ProgramRunning
	// VerificationFindings is a download whose checks found something the
	// person should weigh.
	VerificationFindings
	// NeedsInterface is a setup program that cannot run without its window.
	NeedsInterface
	// LicenseQuestion is an installer that asks for a licence to be accepted.
	LicenseQuestion
)

func (r Reason) String() string {
	return [...]string{
		"eligible", "not installed by this application", "link not confirmed", "not newer", "needs attention",
		"needs administrator permission", "program running", "verification findings", "needs an interface", "license question",
	}[r]
}

// Announce reports whether an Item left for the person is still worth telling
// them about. A running program is retried at the next check, and a release
// that is not newer is no update.
func (r Reason) Announce() bool { return r != ProgramRunning && r != NotNewer }

// Facts are what Judge knows beyond the Entry: what the machine says.
type Facts struct {
	Entry catalog.Entry
	// Running says a program of the installed copy is running.
	Running bool
}

// Judge decides whether the Item's latest update may be installed with nobody
// watching: no question asked, no administrator prompt raised, no running
// program closed. The rows that depend on the downloaded file, the
// verification findings, whether a setup program has a quiet mode and whether
// a licence is asked for, cannot be known yet; the install itself refuses
// them with the same reasons.
func Judge(f Facts) Reason {
	e := f.Entry
	in, managed := e.Installed()
	offer, offered := e.LatestInstallable()
	switch {
	case !offered:
		return NotNewer
	case !managed:
		return NotManaged
	case e.Item.LinkGuessed:
		return LinkUnconfirmed
	case e.Relation(offer.Release.Tag) != catalog.RelationNewer:
		return NotNewer
	case e.Intent == nil || offer.Result.IntentBroken || offer.Result.Ambiguous:
		return NeedsAttention
	case needsElevation(in, offer.Pick().Kind):
		return NeedsElevation
	case f.Running && in.Method != install.MethodTool:
		return ProgramRunning
	}
	return Eligible
}

// needsElevation reports an install that cannot be made for the current user
// alone: an update of a copy installed for everyone, or one that changes how
// the app is installed.
func needsElevation(in catalog.Install, kind selection.ArtifactKind) bool {
	if in.Method == install.MethodInstaller {
		return in.Scope != platform.ScopeUser
	}
	return IsInstaller(kind)
}

// IsInstaller reports a file that is run to install rather than unpacked.
func IsInstaller(kind selection.ArtifactKind) bool {
	switch kind {
	case selection.ArtifactMSI, selection.ArtifactMSIX, selection.ArtifactSetupExe:
		return true
	case selection.ArtifactArchive, selection.ArtifactStandaloneExe:
	}
	return false
}

// RunsQuietly reports whether an installer can run with nobody watching: an MSI
// or MSIX package, a setup program built with a toolkit whose silent switches
// are known, or one the user gave arguments for.
func RunsQuietly(kind selection.ArtifactKind, framework install.Framework, args string) bool {
	switch kind {
	case selection.ArtifactMSI, selection.ArtifactMSIX:
		return true
	case selection.ArtifactSetupExe:
		return framework != install.FrameworkUnknown || args != ""
	case selection.ArtifactArchive, selection.ArtifactStandaloneExe:
	}
	return false
}
