package install

import (
	"testing"

	"github.com/aaronflorey/release-sync/platform"
)

func TestSetupCommandCarriesTheFrameworksSwitches(t *testing.T) {
	for _, tc := range []struct {
		name      string
		framework Framework
		opts      SetupOptions
		want      string
	}{
		{"NSIS silent", FrameworkNSIS, SetupOptions{Silent: true}, "/S"},
		{"NSIS silent in a folder puts the folder last and unquoted", FrameworkNSIS, SetupOptions{Silent: true, Folder: `C:\My Apps\Tool\`}, `/S /D=C:\My Apps\Tool`},
		{"Inno silent", FrameworkInno, SetupOptions{Silent: true}, "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP-"},
		{"Inno in a folder quotes it", FrameworkInno, SetupOptions{Silent: true, Folder: `C:\My Apps\Tool`}, `/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /DIR="C:\My Apps\Tool"`},
		{"WiX bundle silent", FrameworkWiX, SetupOptions{Silent: true}, "/quiet /norestart"},
		{"WiX bundle has no folder switch", FrameworkWiX, SetupOptions{Silent: true, Folder: `C:\Tool`}, "/quiet /norestart"},
		{"Squirrel silent", FrameworkSquirrel, SetupOptions{Silent: true}, "--silent"},
		{"an unknown toolkit shows its own interface", FrameworkUnknown, SetupOptions{Silent: true, Folder: `C:\Tool`}, ""},
		{"not silent adds no switches", FrameworkInno, SetupOptions{}, ""},
		{"not silent still names the folder", FrameworkNSIS, SetupOptions{Folder: `C:\Tool`}, `/D=C:\Tool`},
		{"the Item's arguments replace the silent switches", FrameworkNSIS, SetupOptions{Silent: true, Args: " /S /NCRC "}, "/S /NCRC"},
		{"the Item's arguments are followed by the folder", FrameworkNSIS, SetupOptions{Args: "/allusers", Folder: `C:\Tool`}, `/allusers /D=C:\Tool`},
		{"the Item's arguments apply to an unknown toolkit", FrameworkUnknown, SetupOptions{Silent: true, Args: "--quiet"}, "--quiet"},
	} {
		got := SetupCommand(`C:\dl\tool-setup.exe`, tc.framework, tc.opts)
		if got.Path != `C:\dl\tool-setup.exe` || got.Args != tc.want || got.Elevated {
			t.Errorf("%s: %+v, want arguments %q and no explicit elevation", tc.name, got, tc.want)
		}
	}
}

func TestMSICommand(t *testing.T) {
	const logFile = `C:\logs\tool-install.log`
	for _, tc := range []struct {
		name     string
		opts     MSIOptions
		args     string
		elevated bool
	}{
		{
			"silent",
			MSIOptions{Silent: true, Log: logFile},
			`/i "C:\dl\tool.msi" /qn /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown /L*V "C:\logs\tool-install.log"`, true,
		},
		{
			"with progress",
			MSIOptions{Log: logFile},
			`/i "C:\dl\tool.msi" /passive /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown /L*V "C:\logs\tool-install.log"`, false,
		},
		{
			"in a folder with the Item's properties",
			MSIOptions{Silent: true, Log: logFile, Folder: `D:\Apps\Tool\`, Args: " ACCEPT=1 "},
			`/i "C:\dl\tool.msi" /qn /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown INSTALLDIR="D:\Apps\Tool" ACCEPT=1 /L*V "C:\logs\tool-install.log"`, true,
		},
		{
			"for everyone, even with progress",
			MSIOptions{Log: logFile, Everyone: true},
			`/i "C:\dl\tool.msi" /passive /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown /L*V "C:\logs\tool-install.log" ALLUSERS=1`, true,
		},
	} {
		got := MSICommand(`C:\dl\tool.msi`, tc.opts)
		if got.Path != "msiexec.exe" || got.Args != tc.args || got.Elevated != tc.elevated {
			t.Errorf("%s:\n got %+v\nwant args %q elevated %v", tc.name, got, tc.args, tc.elevated)
		}
	}
}

func TestForEveryone(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed platform.Scope
		wanted    bool
		args      string
		want      bool
	}{
		{"a new install follows the choice: everyone", "", true, "", true},
		{"a new install follows the choice: just me", "", false, "", false},
		{"an update of a copy for everyone stays that way", platform.ScopeMachine, false, "", true},
		{"an update of a copy for one user stays that way", platform.ScopeUser, true, "", false},
		{"the user's own ALLUSERS wins, and nothing is added", "", true, "ALLUSERS=2", false},
		{"the user's own ALLUSERS wins in any case", platform.ScopeMachine, true, "allusers=2", false},
	} {
		if got := ForEveryone(tc.installed, tc.wanted, tc.args); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMSIUninstallCommand(t *testing.T) {
	for _, tc := range []struct {
		silent   bool
		scope    platform.Scope
		args     string
		elevated bool
	}{
		{true, platform.ScopeUser, "/x {ABC-1} /qn /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown", true},
		{false, platform.ScopeMachine, "/x {ABC-1} /passive /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown", true},
		{false, platform.ScopeUser, "/x {ABC-1} /passive /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown", false},
	} {
		got := MSIUninstallCommand("{ABC-1}", tc.silent, tc.scope)
		if got.Path != "msiexec.exe" || got.Args != tc.args || got.Elevated != tc.elevated {
			t.Errorf("silent %v, %s: %+v", tc.silent, tc.scope, got)
		}
	}
}

func TestUninstallCommand(t *testing.T) {
	inno := func(string) Framework { return FrameworkInno }
	none := func(string) Framework { return FrameworkUnknown }
	for _, tc := range []struct {
		name      string
		program   platform.Program
		silent    bool
		framework func(string) Framework
		want      platform.Command
	}{
		{
			"the quiet command is used when asked for quiet",
			platform.Program{UninstallCommand: `"C:\T\unins000.exe"`, QuietUninstallCommand: `"C:\T\unins000.exe" /SILENT`},
			true, inno,
			platform.Command{Path: `C:\T\unins000.exe`, Args: "/SILENT"},
		},
		{
			"the ordinary command when not quiet",
			platform.Program{UninstallCommand: `"C:\T\unins000.exe"`, QuietUninstallCommand: `"C:\T\unins000.exe" /SILENT`},
			false, inno,
			platform.Command{Path: `C:\T\unins000.exe`},
		},
		{
			"a recognised framework with no quiet command gets its switches",
			platform.Program{UninstallCommand: `"C:\T\unins000.exe" /x`},
			true, inno,
			platform.Command{Path: `C:\T\unins000.exe`, Args: "/x /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP-"},
		},
		{
			"an unrecognised uninstaller gets none",
			platform.Program{UninstallCommand: `C:\Program Files\T\uninst.exe /u`},
			true, none,
			platform.Command{Path: `C:\Program Files\T\uninst.exe`, Args: "/u"},
		},
		{
			"only a quiet command is used even when not asked for quiet",
			platform.Program{QuietUninstallCommand: `"C:\T\u.exe" /S`},
			false, none,
			platform.Command{Path: `C:\T\u.exe`, Args: "/S"},
		},
		{
			"an MSI product is removed by its code",
			platform.Program{UninstallCommand: `MsiExec.exe /I{6D0C-3F9E}`, Scope: platform.ScopeMachine},
			true, none,
			platform.Command{Path: "msiexec.exe", Args: "/x {6D0C-3F9E} /qn /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown", Elevated: true},
		},
		{
			"an MSI product recorded with /X",
			platform.Program{UninstallCommand: `msiexec /x {6D0C-3F9E}`, Scope: platform.ScopeUser},
			false, none,
			platform.Command{Path: "msiexec.exe", Args: "/x {6D0C-3F9E} /passive /norestart MSIRESTARTMANAGERCONTROL=DisableShutdown"},
		},
	} {
		if got := UninstallCommand(tc.program, tc.silent, tc.framework); got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestSplitCommandLine(t *testing.T) {
	for raw, want := range map[string][2]string{
		`"C:\Program Files\T\unins000.exe" /SILENT`:   {`C:\Program Files\T\unins000.exe`, "/SILENT"},
		`C:\Program Files\T\uninstall.exe /S /v"/qn"`: {`C:\Program Files\T\uninstall.exe`, `/S /v"/qn"`},
		`  "C:\T\u.exe"  `:                            {`C:\T\u.exe`, ""},
		`tool --remove`:                               {"tool", "--remove"},
		`tool`:                                        {"tool", ""},
		`"unterminated`:                               {`"unterminated`, ""},
	} {
		if file, args := SplitCommandLine(raw); file != want[0] || args != want[1] {
			t.Errorf("SplitCommandLine(%q) = %q, %q; want %q, %q", raw, file, args, want[0], want[1])
		}
	}
}
