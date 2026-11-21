package scan

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aaronflorey/release-sync/platform"
)

// wingetList is what `winget list` printed on a machine: a progress spinner,
// then the table with an Available column, and rows with and without one.
const wingetList = "\r   - \r   \\ \r   | \r" +
	"Name                              Id                         Version      Available    Source\r\n" +
	"----------------------------------------------------------------------------------------------\r\n" +
	"7-Zip                             7zip.7zip                  24.07        24.08        winget\r\n" +
	"Git                               Git.Git                    2.45.2                    winget\r\n" +
	"PowerToys (Preview) x64           Microsoft.PowerToys        0.82.1.0                  winget\r\n" +
	"Not a package row                 nodots                     1.0                       winget\r\n" +
	"Visual Studio Code                Microsoft.VisualStudioCode 1.90.0       1.90.1       winget\r\n"

func TestParseWingetListCutsTheRowsAtTheHeadersColumns(t *testing.T) {
	got := ParseWingetList(wingetList)
	want := []WingetPackage{
		{"7-Zip", "7zip.7zip", "24.07"},
		{"Git", "Git.Git", "2.45.2"},
		{"PowerToys (Preview) x64", "Microsoft.PowerToys", "0.82.1.0"},
		{"Visual Studio Code", "Microsoft.VisualStudioCode", "1.90.0"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("packages = %+v", got)
	}
}

func TestParseWingetListWithoutAnAvailableColumn(t *testing.T) {
	out := "Name   Id          Version Source\n---\nTool   acme.tool   1.2.3   winget\n"
	if got := ParseWingetList(out); !slices.Equal(got, []WingetPackage{{"Tool", "acme.tool", "1.2.3"}}) {
		t.Errorf("packages = %+v", got)
	}
	for _, out := range []string{"", "No installed package found matching input criteria.", "Name Id Version\n"} {
		if got := ParseWingetList(out); len(got) != 0 {
			t.Errorf("%q gave %+v", out, got)
		}
	}
}

func TestWingetRepoReadsTheLinksOfAPackageMostTrustedFirst(t *testing.T) {
	details := "Found 7-Zip [7zip.7zip]\r\nVersion: 24.07\r\nPublisher: Igor Pavlov\r\n" +
		"Publisher Url: https://github.com/publisher/site\r\nPublisher Support Url: https://github.com/support/x\r\n" +
		"Homepage: https://github.com/ip7z/7zip\r\nLicense: LGPL\r\n"
	if got, ok := WingetRepo(details); !ok || got.String() != "ip7z/7zip" {
		t.Errorf("repository = %v, %v", got, ok)
	}
	if got, ok := WingetRepo("Homepage: https://example.com\nRelease Notes Url: https://github.com/acme/tool/releases\n"); !ok || got.String() != "acme/tool" {
		t.Errorf("a later link = %v, %v", got, ok)
	}
	if _, ok := WingetRepo("Homepage: https://example.com\nDescription: see github.com/acme/tool\n"); ok {
		t.Error("a repository in the description was taken as a link")
	}
}

func TestFromWingetLinksPackagesWhoseDetailsNameARepository(t *testing.T) {
	const args = " --exact --source winget --accept-source-agreements --disable-interactivity"
	f := &platform.Fake{WingetOutputs: map[string]string{
		"list --source winget --accept-source-agreements --disable-interactivity": wingetList,
		"show --id 7zip.7zip" + args:                  "Homepage: https://github.com/ip7z/7zip\n",
		"show --id Git.Git" + args:                    "Homepage: https://git-scm.com\n",
		"show --id Microsoft.PowerToys" + args:        "Homepage: https://github.com/microsoft/PowerToys\n",
		"show --id Microsoft.VisualStudioCode" + args: "Homepage: https://code.visualstudio.com\n",
	}}
	var last [2]int
	got, err := FromWinget(t.Context(), f, func(done, total int) { last = [2]int{done, total} })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(got), []string{"7-Zip", "PowerToys (Preview) x64"}) || last != [2]int{4, 4} {
		t.Fatalf("candidates = %v, progress %v", names(got), last)
	}
	if c := got[0]; c.Origin != OriginWinget || c.Version != "24.07" || c.Link.Ref.String() != "ip7z/7zip" || !c.Link.Kind.Verified() {
		t.Errorf("7-Zip = %+v", c)
	}
}

func TestFromWingetSkipsAPackageItCannotReadAndSaysSo(t *testing.T) {
	f := &platform.Fake{WingetOutputs: map[string]string{
		"list --source winget --accept-source-agreements --disable-interactivity":                        wingetList,
		"show --id 7zip.7zip --exact --source winget --accept-source-agreements --disable-interactivity": "Homepage: https://github.com/ip7z/7zip\n",
	}}
	got, err := FromWinget(t.Context(), f, func(int, int) {})
	if len(got) != 1 || err == nil || !strings.Contains(err.Error(), "read winget package") {
		t.Errorf("candidates = %v, error = %v", names(got), err)
	}
}

func TestFromWingetFailsWhenWingetIsNotThere(t *testing.T) {
	f := &platform.Fake{WingetErr: platform.ErrNotSupported}
	if _, err := FromWinget(t.Context(), f, func(int, int) {}); !errors.Is(err, platform.ErrNotSupported) {
		t.Errorf("error = %v", err)
	}
}
