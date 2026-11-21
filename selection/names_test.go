package selection

import "testing"

func TestCommandName(t *testing.T) {
	for file, want := range map[string]string{
		"ripgrep.exe":                         "ripgrep",
		"tool-v1.2.0-windows-amd64.exe":       "tool",
		"my_tool_x86_64-pc-windows-msvc.exe":  "my_tool",
		"tool_2.0.1_Windows_x64.exe":          "tool",
		"bin/Tool.CMD":                        "Tool",
		`sub\tool-1.0.bat`:                    "tool",
		"7z.exe":                              "7z",
		"v2ray.exe":                           "v2ray",
		"windows.exe":                         "windows",
		"tool":                                "tool",
		"fd-v10.2.0-i686-pc-windows-msvc.exe": "fd",
	} {
		if got := CommandName(file); got != want {
			t.Errorf("CommandName(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestNamesInstaller(t *testing.T) {
	for file, want := range map[string]bool{
		"app-1.0-x64-setup.exe": true, "AppInstaller.exe": false, "app_install.exe": true, "app-installer-x64.exe": true,
		"app-x64.exe": false, "reinstall.exe": false, "setup.exe": true,
	} {
		if got := NamesInstaller(file); got != want {
			t.Errorf("NamesInstaller(%q) = %v, want %v", file, got, want)
		}
	}
}
