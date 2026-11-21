// Package platform is the seam to the operating system. Everything that
// depends on the OS goes through Platform, so the rest of the application
// builds and is tested on any system. Each operation has a Windows
// implementation and a stub for other systems.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrNotSupported is the failure of an operation this system has no
// implementation of.
var ErrNotSupported = errors.New("not supported on this system")

// SignatureState is the outcome of a code-signature check.
type SignatureState string

// The signature states.
const (
	// SignatureNotChecked is the state when the system cannot check, or the
	// file is of a kind it does not check. It never blocks anything.
	SignatureNotChecked SignatureState = "not_checked"
	// SignatureTrusted is a valid signature from a publisher the system trusts.
	SignatureTrusted SignatureState = "trusted"
	// SignatureUnsigned is a file with no signature.
	SignatureUnsigned SignatureState = "unsigned"
	// SignatureUntrusted is a signature that is intact but not trusted.
	SignatureUntrusted SignatureState = "untrusted"
	// SignatureInvalid is a signature that does not match the file's contents.
	SignatureInvalid SignatureState = "invalid"
)

// Signature is what the system says about a file's code signature.
type Signature struct {
	State SignatureState
	// Signer is the publisher's name; empty when unsigned or unknown.
	Signer string
}

// Folders are the user's well-known folders the installs care about.
type Folders struct {
	// StartMenu is the per-user Start menu's Programs folder.
	StartMenu string
	// Protected are folders too broad for the application to own or empty: the
	// user's profile, Desktop, Documents and Downloads, the Program Files
	// roots, the Windows folder and the application-data roots.
	Protected []string
	// System are the folders the system and setup programs put programs in:
	// the Windows folder, the Program Files roots and the per-user Programs
	// folder. A program found below one is not a portable program.
	System []string
}

// Shortcut is a link to a program in the Start menu.
type Shortcut struct {
	// Path is the shortcut file, ending in .lnk.
	Path string
	// Target is the program it starts.
	Target string
	// Description is the tooltip.
	Description string
}

// Scope says whom an installed program is installed for.
type Scope string

// The scopes.
const (
	// ScopeUser is a program installed for the current user only.
	ScopeUser Scope = "user"
	// ScopeMachine is a program installed for everyone on the PC.
	ScopeMachine Scope = "machine"
)

// ErrElevationDeclined is the person's answer of no to the system's prompt for
// administrator rights.
var ErrElevationDeclined = errors.New("the administrator prompt was declined")

// Command is a program to run, and wait for.
type Command struct {
	Path string
	// Args is the command line after the program's path, passed exactly as
	// written: some installers read their arguments unquoted.
	Args string
	// Elevated asks for administrator rights. A program whose own manifest asks
	// for them gets the system's prompt without it.
	Elevated bool
	// NoPrompt runs the program with the rights the application has and never
	// raises the administrator prompt, so one that needs more fails instead of
	// waiting for a person who is not there.
	NoPrompt bool
}

// Program is an entry of the system's list of installed programs.
type Program struct {
	// Key identifies the entry, and stays the same while the program is installed.
	Key   string
	Scope Scope
	// Name, Publisher and Version are what the program says about itself.
	Name, Publisher, Version string
	// UninstallCommand and QuietUninstallCommand are command lines; the quiet one
	// is empty when the program has none.
	UninstallCommand, QuietUninstallCommand string
	InstallLocation                         string
	// DisplayIcon is the icon file, usually the program itself, with an optional
	// ",index" the system appends.
	DisplayIcon string
	// AboutURL, UpdateURL, HelpLink and Comments are the entry's own links and
	// notes about the program, which often name its project page.
	AboutURL, UpdateURL, HelpLink, Comments string
}

// VersionInfo is what a program's version resource says about it.
type VersionInfo struct {
	// ProductVersion is the fixed product version, as four numbers; empty when
	// the program has no version resource.
	ProductVersion                                                      string
	Comments, LegalCopyright, LegalTrademarks, ProductName, CompanyName string
}

// Process is a running program.
type Process struct {
	ID int
	// Path is the program file it runs from.
	Path string
}

// Platform is the operating-system seam.
type Platform interface {
	// CheckSignature reports the trust state of the file's code signature.
	CheckSignature(ctx context.Context, path string) (Signature, error)
	// Scan asks the system's antivirus to scan the file without letting it
	// remove anything. A scanner that is missing or fails is a ScanUnavailable
	// result, not an error; only a canceled ctx is.
	Scan(ctx context.Context, path string) (ScanResult, error)

	// Folders reports the user's well-known folders.
	Folders() (Folders, error)
	// CreateShortcut writes a shortcut, with the folder it goes in, replacing one
	// of the same name.
	CreateShortcut(s Shortcut) error
	// RemoveShortcut deletes a shortcut, and the folder it was in when that
	// leaves the folder empty. A shortcut that is not there is not an error.
	RemoveShortcut(path string) error
	// AddToUserPath adds dir to the user's PATH. It reports whether the PATH
	// changed: false means dir was on it already.
	AddToUserPath(dir string) (changed bool, err error)
	// ProcessesIn lists the running programs whose file is inside dir.
	ProcessesIn(ctx context.Context, dir string) ([]Process, error)
	// CloseProcesses asks the programs to quit and, if they are still there
	// after a short wait, ends them.
	CloseProcesses(ctx context.Context, procs []Process) error
	// Launch starts the program at path, from its own folder, without waiting.
	Launch(path string) error
	// RetargetShortcuts points the shortcuts on the Desktop, in the Start menu
	// and pinned to the taskbar that lead into the folder from to the folder to,
	// and reports how many it changed.
	RetargetShortcuts(ctx context.Context, from, to string) (changed int, err error)
	// FreeSpace is how many bytes can be written below dir, which must exist.
	FreeSpace(dir string) (uint64, error)

	// Run starts the command and waits for it to end, then reports its exit
	// code. A declined administrator prompt fails with ErrElevationDeclined.
	Run(ctx context.Context, cmd Command) (exitCode int, err error)
	// Programs lists what the system says is installed, for every user and for
	// the current one. Entries that are not programs a person installed, such as
	// updates and system components, are left out.
	Programs(ctx context.Context) ([]Program, error)
	// FileVersion is the product version in a program's version information,
	// empty when it has none.
	FileVersion(path string) (string, error)
	// VersionInfo reads the version information of a program file without
	// running it. A file with none has an empty VersionInfo.
	VersionInfo(path string) (VersionInfo, error)
	// ListShortcuts lists the shortcuts on the Desktop and in the Start menu, of
	// the current user and of everyone.
	ListShortcuts(ctx context.Context) ([]Shortcut, error)
	// Winget runs winget with args and returns what it printed. It fails with
	// ErrNotSupported when winget is not there.
	Winget(ctx context.Context, args ...string) (string, error)
	// InstallPackage installs an MSIX package for the current user and reports
	// its full name.
	InstallPackage(ctx context.Context, path string) (fullName string, err error)
	// RemovePackage uninstalls an MSIX package by its full name.
	RemovePackage(ctx context.Context, fullName string) error
}

// Current returns the Platform of the running system.
func Current() Platform { return current() }

// signatureFromStatus maps the status text of Windows' Get-AuthenticodeSignature
// to a Signature. A status it does not know is treated as untrusted rather than
// trusted: the user is asked, never waved through.
func signatureFromStatus(status, signer string) Signature {
	signer = strings.TrimSpace(signer)
	switch strings.TrimSpace(status) {
	case "Valid":
		return Signature{State: SignatureTrusted, Signer: signer}
	case "NotSigned":
		return Signature{State: SignatureUnsigned}
	case "HashMismatch":
		return Signature{State: SignatureInvalid, Signer: signer}
	case "NotSupportedFileFormat", "Incompatible":
		return Signature{State: SignatureNotChecked}
	}
	return Signature{State: SignatureUntrusted, Signer: signer}
}

// pathWithEntry appends dir to a PATH value. It reports false, and returns the
// value as it was, when dir is on it already. Entries are compared without
// regard to case or a trailing separator, as Windows does.
func pathWithEntry(path, dir string) (string, bool) {
	want := strings.ToLower(strings.TrimRight(dir, `\/`))
	for entry := range strings.SplitSeq(path, ";") {
		if strings.ToLower(strings.TrimRight(strings.TrimSpace(entry), `\/`)) == want {
			return path, false
		}
	}
	if strings.TrimSpace(path) == "" {
		return dir, true
	}
	return strings.TrimRight(path, ";") + ";" + dir, true
}

// inside reports whether path is a file somewhere below dir, ignoring case.
func inside(dir, path string) bool {
	return strings.HasPrefix(strings.ToLower(path), strings.ToLower(strings.TrimRight(dir, `\/`))+string(filepath.Separator))
}

// uninstallValues are the values of one entry of the system's list of
// installed programs that decide whether it is shown, and what is shown.
type uninstallValues struct {
	DisplayName, Publisher, DisplayVersion, UninstallString, QuietUninstallString string
	InstallLocation, DisplayIcon, ParentKeyName, ReleaseType                      string
	URLInfoAbout, URLUpdateInfo, HelpLink, Comments                               string
	SystemComponent                                                               bool
}

// programFrom makes a Program of an entry, or reports false for one a person
// did not install: no name, nothing to run to remove it, a system component, a
// part of another entry, an update or a hotfix.
func programFrom(key string, scope Scope, v uninstallValues) (Program, bool) {
	name := strings.TrimSpace(v.DisplayName)
	removable := strings.TrimSpace(v.UninstallString) != "" || strings.TrimSpace(v.QuietUninstallString) != ""
	release := strings.ToLower(v.ReleaseType)
	if name == "" || !removable || v.SystemComponent || v.ParentKeyName != "" ||
		strings.Contains(release, "update") || strings.Contains(release, "hotfix") {
		return Program{}, false
	}
	return Program{
		Key: key, Scope: scope, Name: name, Publisher: v.Publisher, Version: v.DisplayVersion,
		UninstallCommand: strings.TrimSpace(v.UninstallString), QuietUninstallCommand: strings.TrimSpace(v.QuietUninstallString),
		InstallLocation: strings.Trim(strings.TrimSpace(v.InstallLocation), `"`), DisplayIcon: strings.TrimSpace(v.DisplayIcon),
		AboutURL: strings.TrimSpace(v.URLInfoAbout), UpdateURL: strings.TrimSpace(v.URLUpdateInfo),
		HelpLink: strings.TrimSpace(v.HelpLink), Comments: strings.TrimSpace(v.Comments),
	}, true
}

// parseShortcuts reads the JSON array of shortcuts the Windows script prints.
func parseShortcuts(out []byte) ([]Shortcut, error) {
	var found []struct {
		Path        string `json:"path"`
		Target      string `json:"target"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &found); err != nil {
		return nil, fmt.Errorf("read the shortcuts: %w", err)
	}
	shortcuts := make([]Shortcut, len(found))
	for i, f := range found {
		shortcuts[i] = Shortcut{Path: f.Path, Target: f.Target, Description: f.Description}
	}
	return shortcuts, nil
}
