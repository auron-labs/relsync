package selection

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// elfHeader is a minimal 64-bit little-endian ELF header with no program or
// section headers.
func elfHeader(fileType, machine uint16, entry uint64) []byte {
	b := make([]byte, 64)
	copy(b, "\x7fELF")
	b[4], b[5], b[6] = 2, 1, 1 // 64-bit, little-endian, current version.
	binary.LittleEndian.PutUint16(b[16:], fileType)
	binary.LittleEndian.PutUint16(b[18:], machine)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint64(b[24:], entry)
	binary.LittleEndian.PutUint16(b[52:], 64) // Header size.
	return b
}

// peFile is a minimal PE32+ image with no sections.
func peFile(machine, characteristics uint16) []byte {
	const (
		peOffset     = 0x40
		optionalSize = 240
	)
	b := make([]byte, peOffset+4+20+optionalSize)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], peOffset)
	copy(b[peOffset:], "PE\x00\x00")
	file := b[peOffset+4:]
	binary.LittleEndian.PutUint16(file[0:], machine)
	binary.LittleEndian.PutUint16(file[16:], optionalSize)
	binary.LittleEndian.PutUint16(file[18:], characteristics)
	optional := file[20:]
	binary.LittleEndian.PutUint16(optional[0:], 0x20b) // PE32+.
	binary.LittleEndian.PutUint32(optional[108:], 16)  // Data directories.
	return b
}

func TestValidateRunnablePayload(t *testing.T) {
	const (
		machineAMD64, machineARM64, machine386 = 0x8664, 0xaa64, 0x14c
		exeImage, dllImage                     = 0x0002, 0x2002
	)
	windows := func(arch string) Target { return Target{OS: OSWindows, Arch: arch} }
	linux := Target{OS: OSLinux, Arch: ArchX64}
	tests := []struct {
		name   string
		file   string
		bytes  []byte
		target Target
		ok     bool
	}{
		{"x64 program on x64", "tool.exe", peFile(machineAMD64, exeImage), windows(ArchX64), true},
		{"x86 program on x64 under emulation", "tool.exe", peFile(machine386, exeImage), windows(ArchX64), true},
		{"x64 program on arm64 under emulation", "tool.exe", peFile(machineAMD64, exeImage), windows(ArchARM64), true},
		{"arm64 program on x64", "tool.exe", peFile(machineARM64, exeImage), windows(ArchX64), false},
		{"x64 program on x86", "tool.exe", peFile(machineAMD64, exeImage), windows(ArchX86), false},
		{"a DLL is not a program", "tool.exe", peFile(machineAMD64, dllImage), windows(ArchX64), false},
		{"a library by name", "tool.dll", peFile(machineAMD64, exeImage), windows(ArchX64), false},
		{"a PE file on Linux", "tool", peFile(machineAMD64, exeImage), linux, false},
		{"a batch script on Windows", "run.cmd", []byte("@echo off\r\n"), windows(ArchX64), true},
		{"a batch script on Linux", "run.cmd", []byte("@echo off\r\n"), linux, false},
		{"a Unix script on Windows", "run", []byte("#!/bin/sh\n"), windows(ArchX64), false},
		{"a Unix script on Linux", "run", []byte("#!/bin/sh\n"), linux, true},
		{"an ELF program on Linux", "tool", elfHeader(2, 62, 0x401000), linux, true},
		{"an ELF program for the wrong CPU", "tool", elfHeader(2, 183, 0x401000), linux, false},
		{"a static PIE on Linux", "tool", elfHeader(3, 62, 0x1040), linux, true},
		{"a shared object has no entry point", "tool", elfHeader(3, 62, 0), linux, false},
		{"an ELF program on Windows", "tool.exe", elfHeader(2, 62, 0x401000), windows(ArchX64), false},
		{"plain text", "tool.exe", []byte("just text"), windows(ArchX64), false},
		{"a patch", "tool.bsdiff", peFile(machineAMD64, exeImage), windows(ArchX64), false},
		{"a checksum", "tool.exe.sha256", []byte("abc"), windows(ArchX64), false},
		{"an empty file", "tool.exe", nil, windows(ArchX64), false},
	}
	dir := t.TempDir()
	for _, tt := range tests {
		path := filepath.Join(dir, "payload")
		if err := os.WriteFile(path, tt.bytes, 0o600); err != nil {
			t.Fatal(err)
		}
		err := ValidateRunnablePayload(path, tt.file, tt.target)
		if tt.ok && err != nil {
			t.Errorf("%s: rejected: %v", tt.name, err)
		}
		if !tt.ok && !errors.Is(err, ErrNotRunnable) {
			t.Errorf("%s: error = %v, want ErrNotRunnable", tt.name, err)
		}
	}
	if err := ValidateRunnablePayload(dir, "tool.exe", windows(ArchX64)); !errors.Is(err, ErrNotRunnable) {
		t.Errorf("a directory: %v", err)
	}
	if err := ValidateRunnablePayload(filepath.Join(dir, "missing"), "tool.exe", windows(ArchX64)); err == nil || errors.Is(err, ErrNotRunnable) {
		t.Errorf("a missing file is an I/O error, got %v", err)
	}
}
