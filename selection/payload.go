package selection

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotRunnable says a file is not something the target machine can run.
var ErrNotRunnable = errors.New("not a runnable payload")

// ValidateRunnablePayload checks the bytes of a downloaded file instead of
// trusting its name: a native executable or script for the target, not a
// library, a patch or metadata. It is adapted from the shared release-asset
// resolver (see NOTICE.md), with the target passed in. The file is inspected,
// never run.
func ValidateRunnablePayload(path, sourceName string, target Target) error {
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", sourceName, err)
	}
	notRunnable := func(why string) error { return fmt.Errorf("%w: %s", ErrNotRunnable, why) }
	p := parseName(sourceName)
	if class, _ := classify(p, ItemTool); info.IsDir() || class == ClassMetadata || class == ClassDelta || looksLikeLibrary(p) {
		return notRunnable(sourceName)
	}
	head, err := readHead(path)
	if err != nil {
		return err
	}
	onWindows := target.OS == OSWindows
	switch {
	case bytes.HasPrefix(head, []byte("#!")):
		if onWindows {
			return notRunnable("a Unix script cannot run on Windows")
		}
		return nil
	case isBatchScript(p, head):
		if onWindows {
			return nil
		}
		return notRunnable("a Windows batch script cannot run on this system")
	case isELFExecutable(path, p, target) && (target.OS == OSLinux || target.OS == "android" || strings.HasSuffix(target.OS, "bsd") || target.OS == "dragonfly"):
		return nil
	case isMachOExecutable(path, target) && target.OS == OSDarwin:
		return nil
	case isPEExecutable(path, target) && onWindows:
		return nil
	}
	return notRunnable(sourceName + " is not a compatible executable or script")
}

func looksLikeLibrary(p parsed) bool {
	return hasAnySuffix(p.base, []string{".so", ".dylib", ".dll"})
}

func readHead(path string) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 256)
	n, _ := f.Read(head) // An empty or unreadable file has no head and is simply not runnable.
	return head[:n], nil
}

func isBatchScript(p parsed, head []byte) bool {
	if !strings.HasSuffix(p.base, ".bat") && !strings.HasSuffix(p.base, ".cmd") {
		return false
	}
	return len(bytes.TrimSpace(head)) > 0
}

func isELFExecutable(path string, p parsed, target Target) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if !target.runs(elfArch(f.Machine)) {
		return false
	}
	if f.Type == elf.ET_EXEC {
		return true
	}
	if f.Type != elf.ET_DYN {
		return false
	}
	// A shared library has a SONAME or no entry point. A dynamic object with an
	// entry point and neither is a program, possibly a static PIE.
	sonames, _ := f.DynString(elf.DT_SONAME)
	return len(sonames) == 0 && f.Entry != 0 && !looksLikeLibrary(p)
}

var elfArchs = map[elf.Machine]string{elf.EM_X86_64: ArchX64, elf.EM_386: ArchX86, elf.EM_AARCH64: ArchARM64}

func elfArch(machine elf.Machine) string { return elfArchs[machine] }

func isMachOExecutable(path string, target Target) bool {
	if f, err := macho.Open(path); err == nil {
		defer func() { _ = f.Close() }()
		return f.Type == macho.TypeExec && target.runs(machoArch(f.Cpu))
	}
	fat, err := macho.OpenFat(path)
	if err != nil {
		return false
	}
	defer func() { _ = fat.Close() }()
	for _, a := range fat.Arches {
		if a.Type == macho.TypeExec && target.runs(machoArch(a.Cpu)) {
			return true
		}
	}
	return false
}

var machoArchs = map[macho.Cpu]string{macho.CpuAmd64: ArchX64, macho.Cpu386: ArchX86, macho.CpuArm64: ArchARM64}

func machoArch(cpu macho.Cpu) string { return machoArchs[cpu] }

// peDLL is the PE characteristics flag of a dynamic-link library.
const peDLL = 0x2000

func isPEExecutable(path string, target Target) bool {
	f, err := pe.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return f.OptionalHeader != nil && f.Characteristics&peDLL == 0 && target.runs(peArch(f.Machine))
}

func peArch(machine uint16) string {
	switch machine {
	case pe.IMAGE_FILE_MACHINE_AMD64:
		return ArchX64
	case pe.IMAGE_FILE_MACHINE_I386:
		return ArchX86
	case pe.IMAGE_FILE_MACHINE_ARM64:
		return ArchARM64
	}
	return ""
}
