package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These are the scanner's real outputs, recorded from Windows Security.
const (
	scanClean    = "Scan starting...\r\nScan finished.\r\nScanning C:\\Temp\\app.exe found no threats.\r\n"
	scanDetected = "Scan starting...\r\nScan finished.\r\nScanning C:\\Temp\\app.exe found 1 threats.\r\n<===========================LIST OF DETECTED THREATS==========================>\r\n----------------------------- Threat information ------------------------------\r\nThreat                  : Virus:DOS/EICAR_Test_File\r\nResources               : 1 total\r\n    file                : C:\\Temp\\app.exe\r\n-------------------------------------------------------------------------------\r\n"
)

func TestParseScan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exit   int
		output string
		want   ScanResult
	}{
		{"clean", 0, scanClean, ScanResult{State: ScanClean}},
		{"a named threat", 2, scanDetected, ScanResult{State: ScanThreat, Threat: "Virus:DOS/EICAR_Test_File"}},
		{"a named threat whatever the exit code", 0, scanDetected, ScanResult{State: ScanThreat, Threat: "Virus:DOS/EICAR_Test_File"}},
		{"a counted threat", 2, "Scanning C:\\Temp\\app.exe found 2 threats.\r\n", ScanResult{State: ScanThreat}},
		{"a file name cannot fake a detection", 0, "Scanning C:\\Temp\\found 1 threats.exe found no threats.\r\n", ScanResult{State: ScanClean}},
		{"a detection in another language", 2, "Analyse terminée.\r\nMenaces détectées : 1\r\n", ScanResult{State: ScanThreat}},
		{"a missing file is an error", 2, "[Failed][0x80070002] The system cannot find the file specified.\r\nCmdTool: Failed with hr = 0x80070002. Check C:\\Temp\\MpCmdRun.log for more information\r\n", ScanResult{State: ScanUnavailable}},
		{"another antivirus in charge", 2, "Scan starting...\r\nCmdTool: Failed with hr = 0x800106BA. Check C:\\Temp\\MpCmdRun.log for more information\r\n", ScanResult{State: ScanUnavailable}},
		{"an error code alone", 2, "CmdTool: Failed with hr = 0x80508023.", ScanResult{State: ScanUnavailable}},
		{"no output", 2, "", ScanResult{State: ScanUnavailable}},
		{"any other exit code", 1, "Something else happened.", ScanResult{State: ScanUnavailable}},
	} {
		if got := ParseScan(tc.exit, tc.output); got != tc.want {
			t.Errorf("%s: ParseScan(%d) = %+v, want %+v", tc.name, tc.exit, got, tc.want)
		}
	}
}

func scannedFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.exe")
	if err := os.WriteFile(path, []byte("program"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScannerReadsTheScan(t *testing.T) {
	path := scannedFile(t)
	s := scanner{
		locate: func() (string, bool) { return "MpCmdRun.exe", true }, timeout: time.Second,
		run: func(context.Context, string, string) (int, string, error) { return 2, scanDetected, nil },
	}
	got, err := s.scan(t.Context(), path)
	if err != nil || got.State != ScanThreat || got.Threat != "Virus:DOS/EICAR_Test_File" {
		t.Fatalf("scan = %+v, %v", got, err)
	}
}

func TestAFileThatVanishesIsAThreat(t *testing.T) {
	path := scannedFile(t)
	missing := scanner{locate: func() (string, bool) { t.Fatal("located the scanner for a file that is gone"); return "", false }, timeout: time.Second}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := missing.scan(t.Context(), path); err != nil || got.State != ScanThreat || !got.Removed {
		t.Fatalf("file gone before the scan: %+v, %v", got, err)
	}

	path = scannedFile(t)
	during := scanner{
		locate: func() (string, bool) { return "MpCmdRun.exe", true }, timeout: time.Second,
		run: func(_ context.Context, _, p string) (int, string, error) { return 0, scanClean, os.Remove(p) },
	}
	if got, err := during.scan(t.Context(), path); err != nil || got.State != ScanThreat || !got.Removed {
		t.Fatalf("file removed during the scan: %+v, %v", got, err)
	}
}

func TestAScanThatCannotBeMadeNeverBlocks(t *testing.T) {
	path := scannedFile(t)
	for name, s := range map[string]scanner{
		"no scanner": {locate: func() (string, bool) { return "", false }, timeout: time.Second},
		"a failure": {
			locate: func() (string, bool) { return "MpCmdRun.exe", true }, timeout: time.Second,
			run: func(context.Context, string, string) (int, string, error) { return 0, "", errors.New("cannot start") },
		},
		"a timeout": {
			locate: func() (string, bool) { return "MpCmdRun.exe", true }, timeout: time.Millisecond,
			run: func(ctx context.Context, _, _ string) (int, string, error) { <-ctx.Done(); return 0, "", ctx.Err() },
		},
	} {
		if got, err := s.scan(t.Context(), path); err != nil || got.State != ScanUnavailable {
			t.Errorf("%s: scan = %+v, %v", name, got, err)
		}
	}
}

func TestACanceledScanIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := scanner{
		locate: func() (string, bool) { return "MpCmdRun.exe", true }, timeout: time.Minute,
		run: func(ctx context.Context, _, _ string) (int, string, error) {
			cancel()
			<-ctx.Done()
			return 0, "", ctx.Err()
		},
	}
	if _, err := s.scan(ctx, scannedFile(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestScannerFoldersAreOrderedByTheirNumbers(t *testing.T) {
	if newerScannerDir("4.18.9-0", "4.18.24090.11-0") >= 0 || newerScannerDir("4.18.24090.11-0", "4.18.9-0") <= 0 {
		t.Error("4.18.9 must be older than 4.18.24090")
	}
	if newerScannerDir("4.18.1-0", "4.18.1-0") != 0 {
		t.Error("equal folders must compare equal")
	}
}

func TestRebase(t *testing.T) {
	for _, tc := range []struct {
		path, want string
		ok         bool
	}{
		{`C:\Apps\tool\tool.exe`, `D:\Moved\tool.exe`, true},
		{`c:\apps\TOOL\bin\a.exe`, `D:\Moved\bin\a.exe`, true},
		{`C:\Apps\tool\tool.exe,0`, `D:\Moved\tool.exe,0`, true},
		{`C:\Apps\tool\`, `D:\Moved`, true},
		{`C:\Apps\tool`, `D:\Moved`, true},
		{`C:\Apps\tool-other\tool.exe`, "", false},
		{`C:\Apps\too`, "", false},
		{`C:\Elsewhere\tool.exe`, "", false},
		{"", "", false},
	} {
		got, ok := Rebase(tc.path, `C:\Apps\tool`, `D:\Moved\`)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Rebase(%q) = %q, %v; want %q, %v", tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRetargetsChangesOnlyShortcutsIntoTheMovedFolder(t *testing.T) {
	out := []byte(`[
	  {"path":"C:\\Users\\u\\Desktop\\Tool.lnk","target":"C:\\Apps\\tool\\tool.exe","workdir":"C:\\Apps\\tool","icon":"C:\\Apps\\tool\\tool.exe,0"},
	  {"path":"C:\\Users\\u\\Desktop\\Another.lnk","target":"C:\\Apps\\another\\another.exe","workdir":"C:\\Apps\\another","icon":",0"},
	  {"path":"C:\\Users\\u\\Desktop\\Site.lnk","target":"","workdir":"","icon":",0"}
	]`)
	got, err := retargets(out, `C:\Apps\tool`, `D:\Moved`)
	if err != nil || len(got) != 1 {
		t.Fatalf("retargets = %+v, %v", got, err)
	}
	want := link{Path: `C:\Users\u\Desktop\Tool.lnk`, Target: `D:\Moved\tool.exe`, Workdir: `D:\Moved`, Icon: `D:\Moved\tool.exe,0`}
	if got[0] != want {
		t.Errorf("link = %+v, want %+v", got[0], want)
	}
	if _, err := retargets([]byte("not json"), "a", "b"); err == nil {
		t.Error("garbled output was accepted")
	}
}
