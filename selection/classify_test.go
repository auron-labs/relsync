package selection

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name  string
		item  ItemKind
		class Class
		kind  ArtifactKind
	}{
		// Metadata.
		{"app-1.0.exe.sha256", ItemApp, ClassMetadata, ""},
		{"SHA256SUMS", ItemApp, ClassMetadata, ""},
		{"checksums.txt", ItemApp, ClassMetadata, ""},
		{"app-1.0.zip.sig", ItemApp, ClassMetadata, ""},
		{"app.intoto.jsonl", ItemApp, ClassMetadata, ""},
		{"latest.yml", ItemApp, ClassMetadata, ""},
		{"app-1.0.exe.blockmap", ItemApp, ClassMetadata, ""},
		{"app.sbom.json", ItemApp, ClassMetadata, ""},
		{"multiple.intoto.jsonl", ItemTool, ClassMetadata, ""},
		// Delta.
		{"app-1.0-delta.nupkg", ItemApp, ClassDelta, ""},
		{"RELEASES", ItemApp, ClassDelta, ""},
		{"app-1.0.0.from-0.9.bsdiff", ItemApp, ClassDelta, ""},
		{"app.patch.gz", ItemTool, ClassDelta, ""},
		{"app.zsync", ItemApp, ClassDelta, ""},
		// Debug and source.
		{"app-1.0-x64-symbols.zip", ItemApp, ClassDebugOrSource, ""},
		{"app-debug-x64.zip", ItemApp, ClassDebugOrSource, ""},
		{"app.pdb", ItemTool, ClassDebugOrSource, ""},
		{"app-1.0-src.zip", ItemApp, ClassDebugOrSource, ""},
		{"app-docs.zip", ItemApp, ClassDebugOrSource, ""},
		// Foreign packages.
		{"app_1.0_amd64.deb", ItemApp, ClassForeignPackage, ""},
		{"app-1.0.x86_64.rpm", ItemApp, ClassForeignPackage, ""},
		{"app-1.0.dmg", ItemApp, ClassForeignPackage, ""},
		{"App-1.0.AppImage", ItemApp, ClassForeignPackage, ""},
		{"app-1.0-1-x86_64.pkg.tar.zst", ItemApp, ClassForeignPackage, ""},
		{"app.snap", ItemApp, ClassForeignPackage, ""},
		// Candidates and their kinds.
		{"app-1.0.msi", ItemApp, ClassCandidate, ArtifactMSI},
		{"app-1.0.msi", ItemTool, ClassCandidate, ArtifactMSI},
		{"app-1.0.msixbundle", ItemApp, ClassCandidate, ArtifactMSIX},
		{"app.appx", ItemApp, ClassCandidate, ArtifactMSIX},
		{"App-Setup-1.0.exe", ItemApp, ClassCandidate, ArtifactSetupExe},
		{"app-installer.exe", ItemApp, ClassCandidate, ArtifactSetupExe},
		{"app-portable.exe", ItemApp, ClassCandidate, ArtifactStandaloneExe},
		{"app.exe", ItemApp, ClassCandidate, ArtifactSetupExe}, // Provisional until the file is inspected.
		{"app-setup.exe", ItemTool, ClassCandidate, ArtifactStandaloneExe},
		{"app.exe", ItemTool, ClassCandidate, ArtifactStandaloneExe},
		{"app-1.0.zip", ItemApp, ClassCandidate, ArtifactArchive},
		{"app-1.0.7z", ItemApp, ClassCandidate, ArtifactArchive},
		{"app-1.0.tar.gz", ItemTool, ClassCandidate, ArtifactArchive},
		{"app.tar.zst", ItemApp, ClassCandidate, ArtifactArchive},
		{"app-windows.gz", ItemTool, ClassCandidate, ArtifactArchive},
		{"app-windows.gz", ItemApp, ClassOther, ""},
		{"notes.txt", ItemApp, ClassOther, ""},
		{"app.vsix", ItemApp, ClassOther, ""},
		// Tokens, never substrings: these names only contain a junk word.
		{"resource-pack-1.0-x64.zip", ItemApp, ClassCandidate, ArtifactArchive},
		{"debugger-1.0-x64.zip", ItemApp, ClassCandidate, ArtifactArchive},
		{"docsify-desktop-setup.exe", ItemApp, ClassCandidate, ArtifactSetupExe},
		{"sourcetree-setup.exe", ItemApp, ClassCandidate, ArtifactSetupExe},
		{"attestor-1.0.zip", ItemApp, ClassCandidate, ArtifactArchive},
	}
	for _, tt := range tests {
		class, kind := classify(parseName(tt.name), tt.item)
		if class != tt.class || kind != tt.kind {
			t.Errorf("classify(%q, %q) = %q, %q; want %q, %q", tt.name, tt.item, class, kind, tt.class, tt.kind)
		}
	}
}

func TestTargetFacts(t *testing.T) {
	tests := []struct {
		name                 string
		os, arch, abi, cpu   string
		product, variantWant string
	}{
		{"tool-1.2.3-x86_64-pc-windows-msvc.zip", "windows", "x64", "msvc", "", "tool", ""},
		{"tool-v1.2.3-x86_64-unknown-linux-musl.tar.gz", "linux", "x64", "musl", "", "tool", ""},
		{"tool_1.2.3_Windows_arm64.zip", "windows", "arm64", "", "", "tool", ""},
		{"tool-aarch64-apple-darwin.tar.gz", "darwin", "arm64", "", "", "tool", ""},
		{"tool-win64.zip", "windows", "x64", "", "", "tool", ""},
		{"tool-win32.zip", "windows", "x86", "", "", "tool", ""},
		{"tool-64bit.zip", "", "x64", "", "", "tool", ""},
		{"tool-32-bit.zip", "", "x86", "", "", "tool", ""},
		{"tool-ia32-setup.exe", "windows", "x86", "", "", "tool", ""},
		{"tool-x86-64.zip", "", "x64", "", "", "tool", ""},
		{"tool-x86.zip", "", "x86", "", "", "tool", ""},
		{"tool-windows-amd64-avx2.zip", "windows", "x64", "", "avx2", "tool", ""},
		// Electron calls every Windows build win32; the architecture it names wins.
		{"tool-win32-x64-1.0.0.zip", "windows", "x64", "", "", "tool", ""},
		{"tool-win32-arm64-1.0.0.zip", "windows", "arm64", "", "", "tool", ""},
		{"Tool.Setup.1.2.3.exe", "windows", "", "", "", "tool", ""},
		{"tool-full-1.0-win-x64.zip", "windows", "x64", "", "", "tool-full", ""},
		{"ffmpeg-release-full.7z", "", "", "", "", "ffmpeg", "full"},
		{"tool_1.0.0_x64_en-US.msi", "windows", "x64", "", "", "tool", "en-us"},
		{"tool-1.0.0-beta.2-x64-user-setup.exe", "windows", "x64", "", "", "tool", ""},
		{"tool-cli-1.0-x64.zip", "", "x64", "", "", "tool", ""},
		{"tool-ubuntu64-focal.tar.xz", "linux", "", "", "", "tool", "focal"},
		{"tool-winarm64.zip", "windows", "arm64", "", "", "tool", ""},
		{"tool-x86_64-v3-20240601.7z", "", "x64", "", "x86-64-v3", "tool", ""},
		{"tool-1.0-v3.zip", "", "", "", "", "tool", ""}, // A version, not a CPU level.
	}
	for _, tt := range tests {
		p := parseName(tt.name)
		got := [4]string{joinFacts(p.facts.os), joinFacts(p.facts.arch), joinFacts(p.facts.abi), joinFacts(p.facts.cpu)}
		want := [4]string{tt.os, tt.arch, tt.abi, tt.cpu}
		if got != want || p.product != tt.product || p.variant != tt.variantWant {
			t.Errorf("%q: facts %v product %q variant %q; want %v %q %q", tt.name, got, p.product, p.variant, want, tt.product, tt.variantWant)
		}
	}
}
