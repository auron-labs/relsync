package install

import (
	"regexp"
	"strings"

	"github.com/aaronflorey/release-sync/platform"
)

// Command lines are built here, apart from running them, so what an installer
// is asked to do can be read and tested without one.

const (
	msiexec = "msiexec.exe"
	// restartManagerOff keeps Windows Installer from closing the programs that
	// hold files it wants to replace, Explorer among them. The application asks
	// the person before it closes the app itself.
	restartManagerOff = "MSIRESTARTMANAGERCONTROL=DisableShutdown"
)

// SetupOptions are the choices that shape a setup program's command line.
type SetupOptions struct {
	// Silent asks the setup program to install without showing anything, when
	// its framework knows how.
	Silent bool
	// Args are the user's own arguments for this program. They replace the
	// silent switches.
	Args string
	// Folder is the install folder to ask for, empty for the program's own choice.
	Folder string
}

// SetupCommand is the command that runs a setup program. Its own interface is
// shown when it is of no known framework. The setup program asks for
// administrator rights itself when it needs them.
func SetupCommand(file string, framework Framework, o SetupOptions) platform.Command {
	args := strings.TrimSpace(o.Args)
	if args == "" && o.Silent {
		args = framework.silentArgs()
	}
	if o.Folder != "" {
		args = strings.TrimSpace(args + " " + framework.folderArg(o.Folder))
	}
	return platform.Command{Path: file, Args: args}
}

// MSIOptions are the choices that shape the command line of an MSI install.
type MSIOptions struct {
	// Silent installs without any interface; otherwise the progress is shown.
	Silent bool
	// Args are the user's own properties, added to the command line.
	Args string
	// Folder is the install folder to ask for, empty for the package's own choice.
	Folder string
	// Log is the file the installer writes its verbose log to.
	Log string
	// Everyone installs for every user of the PC.
	Everyone bool
}

// MSICommand is the command that installs an MSI package. It is run elevated
// when it is silent, which cannot show the prompt itself, or for everyone.
func MSICommand(file string, o MSIOptions) platform.Command {
	args := []string{"/i", quote(file), msiInterface(o.Silent), "/norestart", restartManagerOff}
	if o.Folder != "" {
		args = append(args, `INSTALLDIR="`+strings.TrimRight(o.Folder, `\/`)+`"`)
	}
	if custom := strings.TrimSpace(o.Args); custom != "" {
		args = append(args, custom)
	}
	args = append(args, "/L*V", quote(o.Log))
	if o.Everyone {
		args = append(args, "ALLUSERS=1")
	}
	return platform.Command{Path: msiexec, Args: strings.Join(args, " "), Elevated: o.Silent || o.Everyone}
}

// MSIUninstallCommand is the command that removes an MSI product by its code.
// A product installed for everyone needs administrator rights to remove.
func MSIUninstallCommand(productCode string, silent bool, scope platform.Scope) platform.Command {
	args := []string{"/x", productCode, msiInterface(silent), "/norestart", restartManagerOff}
	return platform.Command{Path: msiexec, Args: strings.Join(args, " "), Elevated: silent || scope == platform.ScopeMachine}
}

func msiInterface(silent bool) string {
	if silent {
		return "/qn"
	}
	return "/passive"
}

func quote(s string) string { return `"` + s + `"` }

// ForEveryone says whether to install an MSI for every user. An update
// goes where the installed copy is, for an installer that is run quietly takes
// the current user and refuses to update a copy installed for everyone; a new
// install follows the choice. A user who wrote ALLUSERS into the arguments
// decided it themselves, and nothing is added.
func ForEveryone(installed platform.Scope, wanted bool, args string) bool {
	switch {
	case strings.Contains(strings.ToUpper(args), "ALLUSERS"):
		return false
	case installed != "":
		return installed == platform.ScopeMachine
	}
	return wanted
}

// msiProductPattern finds the product code of an entry that removes an MSI
// product, which some record as /I and some as /X.
var msiProductPattern = regexp.MustCompile(`(?i)msiexec(?:\.exe)?\s+/[ix]\s*(\{[0-9a-f-]+\})`)

// UninstallCommand is the command that removes a listed program, by its own
// uninstaller. The quiet command is used when a quiet removal is wanted and the
// program has one; an MSI product is removed by its code; otherwise the
// uninstaller gets its framework's silent switches, which the framework
// function reads from the uninstaller's file.
func UninstallCommand(p platform.Program, silent bool, framework func(file string) Framework) platform.Command {
	raw, quiet := p.UninstallCommand, false
	if (silent && p.QuietUninstallCommand != "") || raw == "" {
		raw, quiet = p.QuietUninstallCommand, true
	}
	if m := msiProductPattern.FindStringSubmatch(raw); m != nil {
		return MSIUninstallCommand(m[1], silent, p.Scope)
	}
	file, args := SplitCommandLine(raw)
	if silent && !quiet {
		if switches := framework(file).silentArgs(); switches != "" {
			args = strings.TrimSpace(args + " " + switches)
		}
	}
	return platform.Command{Path: file, Args: args}
}

// SplitCommandLine splits a registered command line into its program and the
// arguments after it. The program is quoted, or ends at its .exe, or at the
// first space.
func SplitCommandLine(raw string) (file, args string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, `"`) {
		if end := strings.Index(raw[1:], `"`); end >= 0 {
			return raw[1 : end+1], strings.TrimSpace(raw[end+2:])
		}
	}
	if i := strings.Index(strings.ToLower(raw), ".exe"); i > 0 {
		return raw[:i+4], strings.TrimSpace(raw[i+4:])
	}
	if file, args, found := strings.Cut(raw, " "); found {
		return file, strings.TrimSpace(args)
	}
	return raw, ""
}
