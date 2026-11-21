//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow keeps a console window from flashing up when a GUI app runs a
// console program.
const createNoWindow = 0x08000000

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
