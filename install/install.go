// Package install puts a verified download on the machine and takes it off
// again. A Portable install unpacks into a folder the application owns; a Tool
// install places commands in one folder that is on the user's PATH. Both work
// from a staging folder beside their destination, so a failed install leaves
// what was there before. The package does not download, ask questions or touch
// the catalog: the caller decides, and records what Apply returns.
//
// An Installer install is different: the publisher's setup program puts the
// files where it chooses and Windows keeps the record, so this package runs the
// setup program, finds out what it did and, later, runs the program's own
// uninstaller.
package install

import (
	"errors"
	"fmt"

	"github.com/aaronflorey/release-sync/platform"
)

// Method is how an Item is on the machine.
type Method string

// The methods this package implements.
const (
	// MethodPortable unpacks into a folder the application owns.
	MethodPortable Method = "portable"
	// MethodTool places commands in the tools folder.
	MethodTool Method = "tool"
	// MethodInstaller runs the publisher's setup program: an MSI package, a
	// setup executable or an MSIX package.
	MethodInstaller Method = "installer"
)

// File is a file an install put in its folder.
type File struct {
	// Path is relative to the folder, written with "/".
	Path string
	// SHA256 is the digest of what was written, kept for programs that are run.
	SHA256 string
	// Command is the name the file is run by when it is a command of a Tool.
	Command string
}

// Record is what an install put on the machine.
type Record struct {
	Method Method
	// Asset is the release file it was made from.
	Asset string
	// Folder is where the files are: a Portable app's own folder, or the tools folder.
	Folder string
	// MainExe is the program a Portable app is started by, relative to Folder;
	// empty when there is none.
	MainExe string
	// Dedicated says Folder was made for this Item in the apps folder, so
	// removing the Item removes the folder; otherwise only Files are removed.
	Dedicated bool
	// Shortcut is the Start menu shortcut, empty when there is none.
	Shortcut string
	// SHA256 is the digest of the main program, or of a Tool's first command.
	SHA256 string
	Files  []File

	// UninstallKey is the program's entry in the system's list of installed
	// programs, empty when the system does not list it. Package is an MSIX
	// package's full name. They are an Installer install's own record: its
	// files are not this application's to list.
	UninstallKey string
	Package      string
	// Scope is whom an Installer install is for.
	Scope platform.Scope
	// EntryVersion is the version the entry reported, kept to notice a change
	// made outside this application.
	EntryVersion string
}

// Removable reports an install this application can take off the machine: any
// but an Installer install whose program the system does not list.
func (r Record) Removable() bool {
	return r.Method != MethodInstaller || r.UninstallKey != "" || r.Package != ""
}

// Outcome is the result of applying an install.
type Outcome struct {
	Record Record
	// PathAdded says the tools folder was added to the PATH by this install.
	PathAdded bool
	// Warning is a problem that does not undo the install, such as a shortcut
	// that could not be made.
	Warning error
	// RestartNeeded says the installer finished but asks for a restart of the PC
	// to complete.
	RestartNeeded bool
	// Shadowed are the commands of a Tool that resolve to another program
	// earlier on the PATH.
	Shadowed []Shadow
}

var (
	// ErrUnsupportedFormat is a download that is not a file this package unpacks.
	ErrUnsupportedFormat = errors.New("unsupported archive format")
	// ErrCorruptArchive is a download that could not be read as the archive it is.
	ErrCorruptArchive = errors.New("the archive could not be read")
	// ErrUnsafeEntry is an archive entry that would be written outside its folder
	// or under a name Windows does not allow.
	ErrUnsafeEntry = errors.New("unsafe archive entry")
	// ErrTooBroad is an install folder that is too broad to manage safely.
	ErrTooBroad = errors.New("install folder is too broad to manage safely")
	// ErrNoProgram is a Tool download with no program in it.
	ErrNoProgram = errors.New("no program in the download")
	// ErrDuplicateCommand is two programs of one download with the same command name.
	ErrDuplicateCommand = errors.New("two programs have the same command name")
)

// ReplaceError is a failure to put the new files in place. Restored says the
// previous files were put back.
type ReplaceError struct {
	Restored bool
	Err      error
}

func (e *ReplaceError) Error() string {
	if e.Restored {
		return fmt.Sprintf("replace the installed files (the previous version was put back): %v", e.Err)
	}
	return fmt.Sprintf("replace the installed files (the previous version could not be put back): %v", e.Err)
}

func (e *ReplaceError) Unwrap() error { return e.Err }

// CommandTakenError is a command another Item owns.
type CommandTakenError struct {
	Command string
	// Owner names the Item that has it.
	Owner string
}

func (e *CommandTakenError) Error() string {
	return fmt.Sprintf("the command %s belongs to %s", e.Command, e.Owner)
}

// ForeignFileError is a file in the tools folder with a command's name that the
// application did not put there. The caller may ask the user and apply again
// with the name in Replace.
type ForeignFileError struct {
	Command string
	Path    string
}

func (e *ForeignFileError) Error() string {
	return e.Path + " already exists and was not installed by this application"
}
