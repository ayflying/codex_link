//go:build windows

package main

import (
	"os/exec"
	"testing"
)

func TestHideConsoleWindow(t *testing.T) {
	command := exec.Command("codex", "app-server")
	hideConsoleWindow(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatal("codex app-server process must hide its console window")
	}
}
