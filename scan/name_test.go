package scan

import "testing"

func TestCleanName(t *testing.T) {
	for display, want := range map[string]string{
		"7-Zip 26.02 (x64)":                     "7-Zip",
		"Notepad++ (64-bit x64)":                "Notepad++",
		"Git version 2.45.1":                    "Git version",
		"Foo [beta] Setup":                      "Foo",
		"Some Tool v1.2.3-rc1":                  "Some Tool",
		"Tool x86 Installer":                    "Tool",
		"OBS Studio":                            "OBS Studio",
		"  Spaced   Out   2024  ":               "Spaced Out",
		"VLC media player":                      "VLC media player",
		"Paint.NET":                             "Paint.NET",
		"Python 3.12.1 (64-bit)":                "Python",
		"Windows Terminal Preview 1.20.11781.0": "Windows Terminal Preview",
	} {
		if got := CleanName(display); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", display, got, want)
		}
	}
}

func TestSystemLikeFlagsComponentsAndHardwareVendors(t *testing.T) {
	for _, tc := range []struct {
		name, publisher string
		want            bool
	}{
		{"Microsoft Visual C++ 2015-2022 Redistributable (x64)", "Microsoft Corporation", true},
		{"NVIDIA Graphics Driver 551.23", "NVIDIA Corporation", true},
		{"Microsoft .NET Runtime - 8.0.4", "", true},
		{"Windows SDK AddOn", "", true},
		{"Vulkan Run Time Libraries", "", true},
		{"Realtek Audio", "", true},
		{"Some Tool", "Logitech", true},
		{"Some Tool", "Advanced Micro Devices", true},
		{"Intel(R) Wireless", "", true},
		{"7-Zip", "Igor Pavlov", false},
		{"OBS Studio", "OBS Project", false},
	} {
		if got := SystemLike(Candidate{Name: tc.name, Publisher: tc.publisher}); got != tc.want {
			t.Errorf("SystemLike(%q, %q) = %v, want %v", tc.name, tc.publisher, got, tc.want)
		}
	}
}

func TestNameFromFolderAndVersionIn(t *testing.T) {
	for folder, want := range map[string]string{
		"foo-1.2.3":            "foo",
		"My_Tool_v2":           "My Tool",
		"cool-app-windows-x64": "cool app",
		"Tool Portable":        "Tool",
		"tool":                 "tool",
		"a":                    "",
	} {
		if got := nameFromFolder(folder); got != want {
			t.Errorf("nameFromFolder(%q) = %q, want %q", folder, got, want)
		}
	}
	for text, want := range map[string]string{"foo-1.2.3": "1.2.3", "tool v2.0": "2.0", "1.2.3.4": "1.2.3.4", "tool": "", "v7": "", "0.0.0.0": "0.0.0.0"} {
		if got := versionIn(text); got != want {
			t.Errorf("versionIn(%q) = %q, want %q", text, got, want)
		}
	}
}
