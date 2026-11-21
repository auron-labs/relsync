package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aaronflorey/release-sync/platform"
	"github.com/aaronflorey/release-sync/selection"
)

var (
	// ErrCancelled is an installer or uninstaller the person cancelled, by
	// declining the administrator prompt or by closing the program. It is not a
	// failure.
	ErrCancelled = errors.New("installation was cancelled")
	// ErrNotListed is a program to uninstall that the system does not list.
	ErrNotListed = errors.New("the system does not list this program")
	// ErrStillListed is an uninstaller that finished with the program still in the
	// system's list of installed programs.
	ErrStillListed = errors.New("the program is still listed after its uninstaller finished")
)

// Exit codes of setup programs, and of Windows Installer, that are not failures.
const (
	exitRestartRequired = 3010
	exitRestartStarted  = 1641
	exitCancelled       = 1602
)

// SetupError is a setup program that ended with a code that does not say it
// succeeded.
type SetupError struct{ Code int }

func (e *SetupError) Error() string { return fmt.Sprintf("the installer closed with code %d", e.Code) }

// MSIError is an MSI install that failed. The log is kept for the person to read.
type MSIError struct {
	Code      int
	Diagnosis MSIDiagnosis
	// Log is the installer's log file, empty when it was not written.
	Log string
}

func (e *MSIError) Error() string {
	return fmt.Sprintf("the Windows Installer stopped with error %d (log: %s)", e.Code, e.Log)
}

// LicenseRequiredError is an MSI install that stopped because a licence
// property was not passed. Only the person may accept a licence, so the caller
// asks, and applies again with Setting added to the arguments.
type LicenseRequiredError struct {
	// Setting is the property to pass, as NAME=VALUE.
	Setting string
}

func (e *LicenseRequiredError) Error() string {
	return "the installer needs its licence accepted by passing " + e.Setting
}

// InstallerRequest is what an Installer install needs to know.
type InstallerRequest struct {
	// File is the verified download: an MSI package, a setup program or an MSIX
	// package, as Kind says.
	File string
	// Asset is the release file it was made from.
	Asset string
	Kind  selection.ArtifactKind
	// Item is what the Item is called, to find its entry in the list of installed
	// programs.
	Item Identity
	// Silent installs without an interface where the setup program knows how.
	Silent bool
	// Args are the user's own installer arguments for the Item.
	Args string
	// Folder is the folder to ask for, empty to leave it to the installer. It is
	// the folder the person chose, or on an update the one the app is in.
	Folder string
	// Everyone is the wish to install for every user of the PC.
	Everyone bool
	// Previous is the install being updated, nil for a fresh install.
	Previous *Record
	// Unattended runs the installer with nobody watching: always without its
	// interface, and never with the administrator prompt. An installer that
	// needs rights the application lacks fails instead.
	Unattended bool
	// Log is where an MSI's verbose log is written. It is kept when the install
	// fails and deleted when it succeeds.
	Log      string
	Platform platform.Platform
	Timing   Timing
}

// ApplyInstaller installs an MSI package, a setup program or an MSIX package,
// then finds out what it did: the new entry in the list of installed programs,
// and the program it starts. When the system does not list the program, the
// install is recorded without an entry. A declined administrator prompt, or an
// installer that reports being cancelled, is ErrCancelled.
func ApplyInstaller(ctx context.Context, req InstallerRequest) (Outcome, error) {
	switch req.Kind {
	case selection.ArtifactMSIX:
		return applyPackage(ctx, req)
	case selection.ArtifactMSI, selection.ArtifactSetupExe:
		return applyProgram(ctx, req)
	case selection.ArtifactArchive, selection.ArtifactStandaloneExe:
	}
	return Outcome{}, fmt.Errorf("%w: %s is a %s", ErrUnsupportedFormat, req.Asset, req.Kind)
}

func applyPackage(ctx context.Context, req InstallerRequest) (Outcome, error) {
	fullName, err := req.Platform.InstallPackage(ctx, req.File)
	if err != nil {
		return Outcome{}, err //nolint:wrapcheck // The platform says what it was installing.
	}
	out := Outcome{Record: Record{Method: MethodInstaller, Asset: req.Asset, Package: fullName, Scope: platform.ScopeUser}}
	if fullName == "" {
		out.Warning = errors.New("the package was installed but Windows did not report its name")
	}
	return out, nil
}

func applyProgram(ctx context.Context, req InstallerRequest) (Outcome, error) {
	before, err := req.Platform.Programs(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("list the installed programs: %w", err)
	}
	cmd, err := installCommand(req)
	if err != nil {
		return Outcome{}, err
	}
	code, err := req.Platform.Run(ctx, cmd)
	if errors.Is(err, platform.ErrElevationDeclined) {
		return Outcome{}, ErrCancelled
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("run the installer: %w", err)
	}
	restart, err := checkExit(code, req)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{RestartNeeded: restart}
	out.Record, out.Warning = bindEntry(ctx, req, before)
	return out, nil
}

// installCommand is the command that runs the request's installer.
func installCommand(req InstallerRequest) (platform.Command, error) {
	cmd, err := installerCommand(req)
	if req.Unattended {
		cmd.Elevated, cmd.NoPrompt = false, true
	}
	return cmd, err
}

func installerCommand(req InstallerRequest) (platform.Command, error) {
	silent := req.Silent || req.Unattended
	if req.Kind == selection.ArtifactMSI {
		return MSICommand(req.File, MSIOptions{
			Silent: silent, Args: req.Args, Folder: req.Folder, Log: req.Log,
			Everyone: ForEveryone(previousScope(req.Previous), req.Everyone, req.Args),
		}), nil
	}
	framework, err := DetectFramework(req.File)
	if err != nil {
		return platform.Command{}, err
	}
	return SetupCommand(req.File, framework, SetupOptions{Silent: silent, Args: req.Args, Folder: req.Folder}), nil
}

func previousScope(prev *Record) platform.Scope {
	if prev == nil {
		return ""
	}
	return prev.Scope
}

// checkExit turns the exit code of an installer into success, success that
// needs a restart, or the reason it failed. The log of a failed MSI is read for
// it and kept; that of one that worked is deleted.
func checkExit(code int, req InstallerRequest) (restart bool, err error) {
	switch code {
	case 0:
		removeLog(req)
		return false, nil
	case exitRestartRequired, exitRestartStarted:
		removeLog(req)
		return true, nil
	case exitCancelled:
		removeLog(req)
		return false, ErrCancelled
	}
	if req.Kind != selection.ArtifactMSI {
		return false, &SetupError{Code: code}
	}
	return false, msiFailure(code, req)
}

func removeLog(req InstallerRequest) {
	if req.Kind == selection.ArtifactMSI {
		_ = os.Remove(req.Log) // A leftover log is only wasted space.
	}
}

// msiFailure explains a failed MSI install from its log.
func msiFailure(code int, req InstallerRequest) error {
	log, readErr := ReadMSILog(req.Log)
	failure := &MSIError{Code: code, Diagnosis: DiagnoseMSI(code, log), Log: req.Log}
	if readErr != nil {
		failure.Log = ""
	}
	if setting := LicenseSetting(log); setting != "" && !strings.Contains(strings.ToUpper(req.Args), setting) {
		_ = os.Remove(req.Log)
		return &LicenseRequiredError{Setting: setting}
	}
	return failure
}

// bindEntry finds the entry the installer made and records it. A program the
// list does not show is recorded without one, with the reason why not when the
// list could not be read.
func bindEntry(ctx context.Context, req InstallerRequest, before []platform.Program) (Record, error) {
	rec := Record{Method: MethodInstaller, Asset: req.Asset}
	var previousKey string
	if req.Previous != nil {
		previousKey = req.Previous.UninstallKey
	}
	var found platform.Program
	timing := req.Timing.withDefaults()
	_, err := waitFor(ctx, req.Platform, timing.Appear, timing.Interval,
		func(after []platform.Program) bool {
			var ok bool
			found, ok = FindNew(before, after, req.Item)
			if !ok && previousKey != "" {
				found, ok = Entry(after, previousKey)
			}
			return ok
		})
	if err != nil {
		return rec, fmt.Errorf("list the installed programs: %w", err)
	}
	if found.Key == "" {
		return rec, nil
	}
	rec = AdoptEntry(found, req.Item.Repo)
	rec.Asset = req.Asset
	return rec, nil
}

// RemoveRequest is what removing an Installer install needs to know.
type RemoveRequest struct {
	Record Record
	// Silent removes without an interface where the uninstaller knows how.
	Silent   bool
	Platform platform.Platform
	Timing   Timing
}

// RemoveInstalled runs the program's own uninstaller, or removes its MSIX
// package, and waits for the program to leave the list of installed programs.
// An entry that is not there to begin with is already removed. ErrStillListed
// is an uninstaller that ended with the entry still there, as it does when the
// person cancelled it; ErrCancelled is a declined administrator prompt.
func RemoveInstalled(ctx context.Context, req RemoveRequest) error {
	rec := req.Record
	if rec.Package != "" {
		return req.Platform.RemovePackage(ctx, rec.Package) //nolint:wrapcheck // The platform says what it was removing.
	}
	if rec.UninstallKey == "" {
		return ErrNotListed
	}
	programs, err := req.Platform.Programs(ctx)
	if err != nil {
		return fmt.Errorf("list the installed programs: %w", err)
	}
	entry, ok := Entry(programs, rec.UninstallKey)
	if !ok {
		return nil
	}
	cmd := UninstallCommand(entry, req.Silent, func(file string) Framework {
		framework, _ := DetectFramework(file) // An uninstaller that cannot be read gets no switches.
		return framework
	})
	if _, err := req.Platform.Run(ctx, cmd); errors.Is(err, platform.ErrElevationDeclined) {
		return ErrCancelled
	} else if err != nil {
		return fmt.Errorf("run the uninstaller: %w", err)
	}
	timing := req.Timing.withDefaults()
	remaining, err := waitFor(ctx, req.Platform, timing.Vanish, timing.Interval, func(after []platform.Program) bool {
		_, still := Entry(after, rec.UninstallKey)
		return !still
	})
	if err != nil {
		return fmt.Errorf("list the installed programs: %w", err)
	}
	if _, still := Entry(remaining, rec.UninstallKey); still {
		return ErrStillListed
	}
	return nil
}
