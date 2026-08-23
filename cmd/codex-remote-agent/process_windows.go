//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func hideConsoleWindow(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
