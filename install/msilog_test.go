package install

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestTheCauseOfAFailureIsFoundInTheLog(t *testing.T) {
	for _, tc := range []struct {
		name string
		log  string
		want MSICause
	}{
		{"insufficient privileges", "Error 1925. You do not have sufficient privileges to complete this installation for all users.", CauseAdministrator},
		{"an administrator is needed to remove", "Error 1730. You must be an Administrator to remove this application.", CauseAdministrator},
		{"the old version cannot be removed", "Error 1714. The older version of DeckCraft cannot be removed.", CauseOldVersion},
		{"a file is in use", "Error 1306. Another application has exclusive access to the file", CauseFileInUse},
		{"a file is in use, by another code", "Error 1310. Error writing to file", CauseFileInUse},
		{"a custom action failed", "Error 1722. There is a problem with this Windows Installer package.", CauseCustomAction},
		{"blocked by Smart App Control", "CustomAction failed with 0x800711C7", CauseBlocked},
		{"blocked by an application control policy", "blocked by an Application Control policy", CauseBlocked},
		{"out of disk space", "Out of disk space - Volume: C:", CauseDiskSpace},
		{"another version", "Another version of this product is already installed.", CauseOtherVersion},
		{"another installation", "Another installation is in progress.", CauseBusy},
	} {
		got := DiagnoseMSI(1603, "line before\n"+tc.log+"\nline after")
		if got.Cause != tc.want || got.Message != "" {
			t.Errorf("%s: %+v, want cause %q", tc.name, got, tc.want)
		}
	}
}

func TestSomeCodesNeedNoLog(t *testing.T) {
	for code, want := range map[int]MSICause{
		1618: CauseBusy, 1638: CauseOtherVersion, 1925: CauseAdministrator, 1730: CauseAdministrator, 1603: CauseUnknown,
	} {
		if got := DiagnoseMSI(code, ""); got.Cause != want {
			t.Errorf("code %d: %+v, want %q", code, got, want)
		}
	}
}

func TestBareNumbersInAVerboseLogAreNotErrors(t *testing.T) {
	for _, log := range []string{
		"MSI (s) Note: 1: 1925",
		"Process 4551 is being used by the installer",
		"Property(S): ProcessorArchitecture = Intel64 1722",
	} {
		if got := DiagnoseMSI(1603, log); got != (MSIDiagnosis{}) {
			t.Errorf("%q was explained as %+v", log, got)
		}
	}
}

func TestBeingBlockedByWindowsWinsOverAGenericCustomActionError(t *testing.T) {
	if got := DiagnoseMSI(1603, "Error 1722.\nfailed with 0x800711C7"); got.Cause != CauseBlocked {
		t.Errorf("cause = %q", got.Cause)
	}
}

func TestAnInstallersOwnMessageComesFirst(t *testing.T) {
	log := "MSI (s): Product: FakeApp -- This app is already installed for everyone. Choose Everyone to update it.\r\n\r\n" +
		"Action ended: LaunchConditions. Return value 3.\r\nError 1925.\r\nMSI (s): Product: FakeApp -- Installation failed.\r\n"
	got := DiagnoseMSI(1603, log)
	if got.Message != "This app is already installed for everyone. Choose Everyone to update it." || got.Cause != CauseUnknown {
		t.Errorf("diagnosis = %+v", got)
	}
}

func TestTheGenericResultLinesAreNotAReason(t *testing.T) {
	for _, log := range []string{
		"Product: FakeApp -- Installation failed.",
		"Product: FakeApp -- Installation completed successfully.",
		"Product: FakeApp -- Error 1925. You do not have sufficient privileges",
	} {
		if got := DiagnoseMSI(1603, log); got.Message != "" {
			t.Errorf("%q gave the message %q", log, got.Message)
		}
	}
	if got := DiagnoseMSI(1603, "Product: FakeApp -- Error 1925. You do not have sufficient privileges"); got.Cause != CauseAdministrator {
		t.Errorf("the numbered error is still explained: %+v", got)
	}
}

func TestLicenseSettingIsTheOneTheInstallerAsksFor(t *testing.T) {
	for log, want := range map[string]string{
		"MSI (s) (E8:C0) [15:55:04:864]: Product: VeraCrypt 1.26.31 -- Please accept the license agreement by passing ACCEPTLICENSE=YES during installation.": "ACCEPTLICENSE=YES",
		"Please accept the EULA by passing accept_eula=1 on the command line.":                                                                                "ACCEPT_EULA=1",
		"Install by passing LICENSEACCEPTED = TRUE":                                                                                                           "LICENSEACCEPTED=TRUE",
		"Property(S): ACCEPTLICENSE = NO":                                                                                                                     "",
		"Error 1603. A fatal error occurred during installation.":                                                                                             "",
		`Pass INSTALLDIR=C:\Apps to choose a folder.`:                                                                                                         "",
		"": "",
	} {
		if got := LicenseSetting(log); got != want {
			t.Errorf("LicenseSetting(%q) = %q, want %q", log, got, want)
		}
	}
}

func TestReadingALogDecodesUTF16AndKeepsOnlyItsEnd(t *testing.T) {
	dir := t.TempDir()
	units := utf16.Encode([]rune("Error 1925. Café"))
	utf16Log := binary.LittleEndian.AppendUint16(nil, 0xfeff)
	for _, u := range units {
		utf16Log = binary.LittleEndian.AppendUint16(utf16Log, u)
	}
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"utf16.log": {utf16Log, "Error 1925. Café"},
		"utf8.log":  {append([]byte{0xef, 0xbb, 0xbf}, "Error 1722"...), "Error 1722"},
		"plain.log": {[]byte("Error 1306"), "Error 1306"},
	} {
		got, err := ReadMSILog(write(name, tc.data))
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, tc.want)
		}
	}
	long := strings.Repeat("x", logTail) + "Error 1925"
	if got, err := ReadMSILog(write("long.log", []byte(long))); err != nil || !strings.HasSuffix(got, "Error 1925") || len(got) != logTail {
		t.Errorf("a long log was read as %d bytes: %v", len(got), err)
	}
	if _, err := ReadMSILog(filepath.Join(dir, "missing.log")); err == nil {
		t.Error("no error for a log that is not there")
	}
}
