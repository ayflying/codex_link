package main

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const guiLogViewLimit = 24 * 1024

func agentGUILogPath(dataDir string) string {
	return filepath.Join(dataDir, "agent-gui.log")
}

func readAgentGUILogTail(logPath string, limit int) (string, error) {
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return "", err
	}
	truncated := limit > 0 && len(raw) > limit
	if truncated {
		raw = raw[len(raw)-limit:]
		for len(raw) > 0 && !utf8.Valid(raw) {
			raw = raw[1:]
		}
	}
	text := strings.TrimSpace(string(raw))
	if truncated && text != "" {
		return "... 已省略更早日志 ...\r\n" + text, nil
	}
	return text, nil
}
